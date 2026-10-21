package access

import (
	"errors"
	"testing"
	"time"

	"sqlwise/internal/auth"
	"sqlwise/internal/testdb"
)

func TestEvaluatorUsesFreshEvidenceAndMaximumActiveGrant(t *testing.T) {
	pool := testdb.Open(t)
	authStore := auth.NewStore(pool)
	if err := authStore.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminCreds := evaluatorCredentials(t, "evaluator.admin")
	if err := authStore.BootstrapAdmin(t.Context(), adminCreds); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	sessions, err := auth.NewService(authStore, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	admin := evaluatorLogin(t, sessions, adminCreds)
	adminCtx, err := sessions.BindSession(t.Context(), mustSession(t, sessions, admin.Token))
	if err != nil {
		t.Fatal(err)
	}
	userCreds := evaluatorCredentials(t, "evaluator.user")
	user, err := sessions.CreateUser(adminCtx, userCreds)
	if err != nil {
		t.Fatal(err)
	}
	userIssued := evaluatorLogin(t, sessions, userCreds)
	userSession := mustSession(t, sessions, userIssued.Token)
	userSession.Principal.UserID = "forged-user"
	userSession.Principal.Username = "forged"
	userSession.Principal.IsPlatformAdmin = true
	userCtx, err := sessions.BindSession(t.Context(), userSession)
	if err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{
		`INSERT INTO database_registrations (id,name,active) VALUES ('db-a','Alpha',true),('db-b','Beta',true),('db-off','Off',false)`,
		`INSERT INTO groups (id,name) VALUES ('g-view','Viewers'),('g-edit','Editors'),('g-off','Inactive')`,
		`UPDATE groups SET active=false WHERE id='g-off'`,
	} {
		if _, err = pool.Exec(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO group_memberships (user_id,group_id) VALUES ($1,'g-view'),($1,'g-edit'),($1,'g-off')`, user.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(t.Context(), `INSERT INTO group_database_grants (group_id,database_id,permission) VALUES ('g-view','db-a','view'),('g-edit','db-a','edit'),('g-edit','db-b','view'),('g-off','db-b','edit')`); err != nil {
		t.Fatal(err)
	}
	evaluator := NewService(store, sessions)
	for _, left := range []Permission{None, View, Edit} {
		for _, right := range []Permission{None, View, Edit} {
			dbID := "pair-" + left.String() + "-" + right.String()
			if _, err = pool.Exec(t.Context(), `INSERT INTO database_registrations (id,name,active) VALUES ($1,$1,true)`, dbID); err != nil {
				t.Fatal(err)
			}
			for i, permission := range []Permission{left, right} {
				if permission == None {
					continue
				}
				groupID := []string{"g-view", "g-edit"}[i]
				if _, err = pool.Exec(t.Context(), `INSERT INTO group_database_grants (group_id,database_id,permission) VALUES ($1,$2,$3)`, groupID, dbID, permission.String()); err != nil {
					t.Fatal(err)
				}
			}
			want := left
			if right > want {
				want = right
			}
			got, err := evaluator.EffectivePermission(userCtx, user.UserID, dbID)
			if err != nil || got != want {
				t.Errorf("pair %s/%s = %s,%v want %s", left, right, got, err, want)
			}
		}
	}
	for _, tc := range []struct {
		db   string
		want Permission
	}{{"db-a", Edit}, {"db-b", View}, {"missing", None}, {"db-off", None}} {
		got, err := evaluator.EffectivePermission(userCtx, user.UserID, tc.db)
		if err != nil || got != tc.want {
			t.Errorf("EffectivePermission(%s)=(%s,%v), want %s", tc.db, got, err, tc.want)
		}
	}
	if _, err := evaluator.EffectivePermission(userCtx, "different-user", "db-a"); !errors.Is(err, auth.ErrSession) {
		t.Errorf("mismatched user error=%v", err)
	}
	if _, err := evaluator.EffectivePermission(t.Context(), user.UserID, "db-a"); !errors.Is(err, auth.ErrSession) {
		t.Errorf("missing proof error=%v", err)
	}
	if _, err := evaluator.EffectivePermission(userCtx, user.UserID, "db-a"); err != nil {
		t.Fatal("forged public principal affected proof:", err)
	}

	if err := evaluator.Require(userCtx, user.UserID, "db-b", View); err != nil {
		t.Errorf("require view: %v", err)
	}
	if err := evaluator.Require(userCtx, user.UserID, "db-b", Edit); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("require edit error=%v", err)
	}
	if err := evaluator.Require(userCtx, user.UserID, "db-b", None); !errors.Is(err, ErrInvalidPermission) {
		t.Errorf("require none error=%v", err)
	}
	if err := evaluator.Require(userCtx, user.UserID, "db-b", Permission(99)); !errors.Is(err, ErrInvalidPermission) {
		t.Errorf("require invalid error=%v", err)
	}
	if _, err := evaluator.EffectivePermission(userCtx, "unknown-user", "db-a"); !errors.Is(err, auth.ErrSession) {
		t.Errorf("unknown caller identity error=%v", err)
	}
	listed, err := evaluator.ListDatabases(userCtx, user.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 10 || listed[0] != (DatabasePermission{ID: "db-a", Name: "Alpha", Permission: Edit}) || listed[1] != (DatabasePermission{ID: "db-b", Name: "Beta", Permission: View}) {
		t.Errorf("ListDatabases=%+v", listed)
	}

	adminCtx, err = sessions.BindSession(t.Context(), mustSession(t, sessions, admin.Token))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := evaluator.EffectivePermission(adminCtx, admin.Principal.UserID, "db-a"); err != nil || got != None {
		t.Errorf("admin bypass=(%s,%v), want none,nil", got, err)
	}
	if _, err := evaluator.ListDatabases(adminCtx, admin.Principal.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE users SET authz_epoch=authz_epoch+1 WHERE id=$1`, user.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.EffectivePermission(userCtx, user.UserID, "db-a"); !errors.Is(err, auth.ErrAuthorizationEpoch) {
		t.Errorf("stale retained context error=%v", err)
	}
}

func evaluatorCredentials(t *testing.T, username string) auth.Credentials {
	t.Helper()
	c, err := auth.ParseCredentials(username, "sufficiently long evaluator password")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func evaluatorLogin(t *testing.T, service *auth.Service, credentials auth.Credentials) auth.IssuedSession {
	t.Helper()
	challenge, err := service.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Login(t.Context(), auth.LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken})
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func mustSession(t *testing.T, service *auth.Service, token string) auth.Session {
	t.Helper()
	session, err := service.Session(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	return session
}
