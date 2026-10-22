package testfixture

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/testdb"
)

const (
	PrimaryTargetID   = "fixture-primary"
	SecondaryTargetID = "fixture-secondary"
	password          = "fixture passphrase 2026"
)

// Actor carries a real session and its bound proof for HTTP and service tests.
type Actor struct {
	UserID       string
	Username     string
	Password     string
	SessionToken string
	CSRFToken    string
	Context      context.Context
}

// Fixture uses an isolated PostgreSQL schema and credential-free target identities.
// It never opens a target database connection.
type Fixture struct {
	Pool       *pgxpool.Pool
	Auth       *auth.Service
	Access     *access.Service
	Admin      Actor
	NoAccess   Actor
	Viewer     Actor
	Editor     Actor
	MultiGroup Actor
	ViewGroup  string
	EditGroup  string
}

func New(t *testing.T) *Fixture {
	t.Helper()
	pool := testdb.Open(t)
	authStore := auth.NewStore(pool)
	if err := authStore.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	accessStore := access.NewStore(pool)
	if err := accessStore.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminCredentials := credentials(t, "fixture.admin")
	if err := authStore.BootstrapAdmin(t.Context(), adminCredentials); err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewService(authStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	policy := access.NewService(accessStore, sessions)
	fixture := &Fixture{Pool: pool, Auth: sessions, Access: policy}
	fixture.Admin = login(t, sessions, adminCredentials)
	fixture.NoAccess = createUser(t, sessions, fixture.Admin.Context, "fixture.none")
	fixture.Viewer = createUser(t, sessions, fixture.Admin.Context, "fixture.view")
	fixture.Editor = createUser(t, sessions, fixture.Admin.Context, "fixture.edit")
	fixture.MultiGroup = createUser(t, sessions, fixture.Admin.Context, "fixture.multi")

	if _, err := pool.Exec(t.Context(), `INSERT INTO database_registrations(id,name,active)
		VALUES ($1,'Fixture primary',true),($2,'Fixture secondary',true)`, PrimaryTargetID, SecondaryTargetID); err != nil {
		t.Fatal(err)
	}
	view, err := policy.CreateGroup(fixture.Admin.Context, "Fixture view")
	if err != nil {
		t.Fatal(err)
	}
	edit, err := policy.CreateGroup(fixture.Admin.Context, "Fixture edit")
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := policy.CreateGroup(fixture.Admin.Context, "Fixture secondary")
	if err != nil {
		t.Fatal(err)
	}
	fixture.ViewGroup, fixture.EditGroup = view.ID, edit.ID
	for _, grant := range []struct {
		groupID, targetID string
		permission        access.Permission
	}{
		{view.ID, PrimaryTargetID, access.View},
		{edit.ID, PrimaryTargetID, access.Edit},
		{secondary.ID, SecondaryTargetID, access.View},
	} {
		if err := policy.SetGrant(fixture.Admin.Context, grant.groupID, grant.targetID, grant.permission); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range []struct{ userID, groupID string }{
		{fixture.Viewer.UserID, view.ID},
		{fixture.Editor.UserID, edit.ID},
		{fixture.MultiGroup.UserID, view.ID},
		{fixture.MultiGroup.UserID, edit.ID},
		{fixture.MultiGroup.UserID, secondary.ID},
	} {
		if err := policy.AddMembership(fixture.Admin.Context, member.userID, member.groupID); err != nil {
			t.Fatal(err)
		}
	}
	// Policy additions advance user epochs. Re-login after setup to issue fresh proof.
	fixture.NoAccess = login(t, sessions, credentials(t, fixture.NoAccess.Username))
	fixture.Viewer = login(t, sessions, credentials(t, fixture.Viewer.Username))
	fixture.Editor = login(t, sessions, credentials(t, fixture.Editor.Username))
	fixture.MultiGroup = login(t, sessions, credentials(t, fixture.MultiGroup.Username))
	return fixture
}

func credentials(t *testing.T, username string) auth.Credentials {
	t.Helper()
	value, err := auth.ParseCredentials(username, password)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func createUser(t *testing.T, sessions *auth.Service, adminContext context.Context, username string) Actor {
	t.Helper()
	principal, err := sessions.CreateUser(adminContext, credentials(t, username))
	if err != nil {
		t.Fatal(err)
	}
	return Actor{UserID: principal.UserID, Username: principal.Username, Password: password}
}

func login(t *testing.T, sessions *auth.Service, credentials auth.Credentials) Actor {
	t.Helper()
	challenge, err := sessions.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := sessions.Login(t.Context(), auth.LoginRequest{
		Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := sessions.Session(t.Context(), issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := sessions.BindSession(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	return Actor{
		UserID: session.Principal.UserID, Username: session.Principal.Username,
		Password: password, SessionToken: issued.Token, CSRFToken: session.CSRFToken, Context: bound,
	}
}
