package access

import (
	"errors"
	"testing"
	"time"

	"sqlwise/internal/auth"
	"sqlwise/internal/testdb"
)

func TestAdminSnapshotIsFreshAndCredentialFree(t *testing.T) {
	pool := testdb.Open(t)
	authStore := auth.NewStore(pool)
	if err := authStore.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	store := NewStore(pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminCreds := evaluatorCredentials(t, "snapshot.admin")
	if err := authStore.BootstrapAdmin(t.Context(), adminCreds); err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewService(authStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	admin := evaluatorLogin(t, sessions, adminCreds)
	adminSession, err := sessions.Session(t.Context(), admin.Token)
	if err != nil {
		t.Fatal(err)
	}
	adminCtx, err := sessions.BindSession(t.Context(), adminSession)
	if err != nil {
		t.Fatal(err)
	}
	userCreds := evaluatorCredentials(t, "snapshot.user")
	user, err := sessions.CreateUser(adminCtx, userCreds)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store, sessions)
	group, err := service.CreateGroup(adminCtx, "Operators")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AddMembership(adminCtx, user.UserID, group.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO database_registrations(id,name,active) VALUES('snap-active','Active',true),('snap-inactive','Inactive',false)`); err != nil {
		t.Fatal(err)
	}
	if err := service.SetGrant(adminCtx, group.ID, "snap-active", Edit); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.AdminSnapshot(adminCtx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Users) != 2 || len(snapshot.Groups) != 1 || len(snapshot.Memberships) != 1 || len(snapshot.Grants) != 1 || len(snapshot.Databases) != 2 {
		t.Fatalf("unexpected snapshot sizes: users=%d groups=%d memberships=%d grants=%d dbs=%d", len(snapshot.Users), len(snapshot.Groups), len(snapshot.Memberships), len(snapshot.Grants), len(snapshot.Databases))
	}
	if snapshot.Users[1].ID != user.UserID || snapshot.Grants[0].Permission != Edit || snapshot.Databases[0].ID != "snap-active" || !snapshot.Databases[0].Active || snapshot.Databases[1].Active {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if err := service.RenameGroup(adminCtx, group.ID, "Operators renamed"); err != nil {
		t.Fatal(err)
	}
	if err := service.RenameGroup(adminCtx, group.ID, "Operators renamed"); err != nil {
		t.Fatal(err)
	}
	regularIssued := evaluatorLogin(t, sessions, userCreds)
	regularSession, err := sessions.Session(t.Context(), regularIssued.Token)
	if err != nil {
		t.Fatal(err)
	}
	regularCtx, err := sessions.BindSession(t.Context(), regularSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AdminSnapshot(regularCtx); !errors.Is(err, auth.ErrSession) {
		t.Fatalf("non-admin snapshot error=%v", err)
	}
}
