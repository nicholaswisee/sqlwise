package auth

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"sqlwise/internal/testdb"
)

func TestAdminLifecycleRequiresFreshAdminAndRevokesEveryFamily(t *testing.T) {
	store := NewStore(testdb.Open(t))
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminCreds, _ := ParseCredentials("admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), adminCreds); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	svc, err := NewService(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	login := func(creds Credentials) IssuedSession {
		t.Helper()
		c, err := svc.LoginChallenge(t.Context(), "")
		if err != nil {
			t.Fatal(err)
		}
		i, err := svc.Login(t.Context(), LoginRequest{Credentials: creds, ChallengeToken: c.Token, CSRFToken: c.CSRFToken})
		if err != nil {
			t.Fatal(err)
		}
		return i
	}
	admin := login(adminCreds)
	adminSession, err := svc.Session(t.Context(), admin.Token)
	if err != nil {
		t.Fatal(err)
	}
	adminCtx, err := svc.BindSession(t.Context(), adminSession)
	if err != nil {
		t.Fatal(err)
	}
	userCreds, _ := ParseCredentials("new.user", "a sufficiently long password")
	user, err := svc.CreateUser(adminCtx, userCreds)
	if err != nil || user.UserID == "" || user.IsPlatformAdmin {
		t.Fatalf("create user = %+v, %v", user, err)
	}
	if _, err := VerifyPassword(mustUserHash(t, store, user.UserID), userCreds.password); err != nil {
		t.Fatal(err)
	}
	first, second := loginForAdminTest(t, svc, userCreds), loginForAdminTest(t, svc, userCreds)
	if _, err := svc.CreateUser(adminCtx, userCreds); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("duplicate create = %v", err)
	}
	if err := svc.SetPlatformAdmin(adminCtx, user.UserID, true); err != nil {
		t.Fatal(err)
	}
	var promotedEpoch int64
	if err := store.pool.QueryRow(t.Context(), `SELECT authz_epoch FROM users WHERE id=$1`, user.UserID).Scan(&promotedEpoch); err != nil {
		t.Fatal(err)
	}
	for _, old := range []IssuedSession{first, second} {
		stale, err := svc.Session(t.Context(), old.Token)
		if err != nil || stale.Replacement == nil {
			t.Fatalf("promotion did not immediately rotate family: %+v %v", stale.Replacement, err)
		}
		if stale.Principal.IsPlatformAdmin || !stale.Replacement.Principal.IsPlatformAdmin {
			t.Fatalf("stale/current principals=%+v successor=%+v", stale.Principal, stale.Replacement.Principal)
		}
		alias, err := svc.Session(t.Context(), old.Token)
		if err != nil || alias.Replacement != nil || alias.Principal.IsPlatformAdmin {
			t.Fatalf("stale alias upgraded: %+v %v", alias, err)
		}
		ctx, err := svc.BindSession(t.Context(), stale)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.ValidateContextFresh(ctx); !errors.Is(err, ErrAuthorizationEpoch) {
			t.Fatalf("triggering proof became fresh: %v", err)
		}
		successor, err := svc.Session(t.Context(), stale.Replacement.Token)
		if err != nil || !successor.Principal.IsPlatformAdmin {
			t.Fatalf("successor admin=%+v err=%v", successor.Principal, err)
		}
	}
	if err := svc.SetPlatformAdmin(adminCtx, user.UserID, true); err != nil {
		t.Fatal("no-op promotion:", err)
	}
	var noOpEpoch int64
	if err := store.pool.QueryRow(t.Context(), `SELECT authz_epoch FROM users WHERE id=$1`, user.UserID).Scan(&noOpEpoch); err != nil {
		t.Fatal(err)
	}
	if noOpEpoch != promotedEpoch {
		t.Fatalf("no-op promotion changed epoch %d -> %d", promotedEpoch, noOpEpoch)
	}
	if err := svc.SetPlatformAdmin(adminCtx, user.UserID, false); err != nil {
		t.Fatal(err)
	}
	for _, old := range []IssuedSession{first, second} {
		stale, err := svc.Session(t.Context(), old.Token)
		if !errors.Is(err, ErrSession) || stale.Replacement != nil {
			t.Fatalf("demoted family survived: %+v %v", stale, err)
		}
	}
	if err := svc.SetUserActive(adminCtx, user.UserID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetUserActive(adminCtx, user.UserID, true); err != nil {
		t.Fatal(err)
	}
	// Neither disable/re-enable nor demotion can revive old token families.
	postDisable := login(userCreds)
	if _, err := svc.Session(t.Context(), postDisable.Token); err != nil {
		t.Fatal("new active login:", err)
	}
	if err := svc.SetUserActive(adminCtx, user.UserID, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetUserActive(adminCtx, user.UserID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session(t.Context(), postDisable.Token); !errors.Is(err, ErrSession) {
		t.Fatalf("disable/re-enable revived family: %v", err)
	}
}

func TestCreateUserRejectsNonAdminWithoutEffects(t *testing.T) {
	store := NewStore(testdb.Open(t))
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminCreds, _ := ParseCredentials("admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), adminCreds); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := svc.LoginChallenge(t.Context(), "")
	i, err := svc.Login(t.Context(), LoginRequest{Credentials: adminCreds, ChallengeToken: c.Token, CSRFToken: c.CSRFToken})
	if err != nil {
		t.Fatal(err)
	}
	adminSession, _ := svc.Session(t.Context(), i.Token)
	adminCtx, _ := svc.BindSession(t.Context(), adminSession)
	userCreds, _ := ParseCredentials("regular", "another valid password")
	user, err := svc.CreateUser(adminCtx, userCreds)
	if err != nil {
		t.Fatal(err)
	}
	regular, err := svc.Session(t.Context(), loginForAdminTest(t, svc, userCreds).Token)
	if err != nil {
		t.Fatal(err)
	}
	var beforeUsers, beforeSessions int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM users`).Scan(&beforeUsers); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_sessions`).Scan(&beforeSessions); err != nil {
		t.Fatal(err)
	}
	regularCtx, err := svc.BindSession(t.Context(), regular)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(regularCtx, mustCredentials(t, "should.not", "a valid password here")); !errors.Is(err, ErrSession) {
		t.Fatalf("nonadmin create error=%v", err)
	}
	var users, sessions int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE username='should.not'`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_sessions WHERE user_id=$1`, user.UserID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	var afterUsers, afterSessions int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM users`).Scan(&afterUsers); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM auth_sessions`).Scan(&afterSessions); err != nil {
		t.Fatal(err)
	}
	if users != 0 || sessions != 1 || afterUsers != beforeUsers || afterSessions != beforeSessions {
		t.Fatalf("denied call effects new-users=%d user's sessions=%d all users=%d->%d sessions=%d->%d", users, sessions, beforeUsers, afterUsers, beforeSessions, afterSessions)
	}
}

type countingFailReader struct{ calls int }

func (r *countingFailReader) Read([]byte) (int, error) {
	r.calls++
	return 0, errors.New("unexpected user ID entropy")
}

type blockingReader struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	for i := range p {
		p[i] = 0x81
	}
	return len(p), nil
}

func TestUnauthorizedCreateUserDoesNotConsumeSessionEntropy(t *testing.T) {
	store := NewStore(testdb.Open(t))
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminCreds := mustCredentials(t, "admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), adminCreds); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	admin := loginForAdminTest(t, svc, adminCreds)
	adminSession, err := svc.Session(t.Context(), admin.Token)
	if err != nil {
		t.Fatal(err)
	}
	adminCtx, err := svc.BindSession(t.Context(), adminSession)
	if err != nil {
		t.Fatal(err)
	}
	nonAdminCreds := mustCredentials(t, "regular", "another valid password")
	if _, err := svc.CreateUser(adminCtx, nonAdminCreds); err != nil {
		t.Fatal(err)
	}
	nonAdmin := loginForAdminTest(t, svc, nonAdminCreds)
	nonAdminSession, err := svc.Session(t.Context(), nonAdmin.Token)
	if err != nil {
		t.Fatal(err)
	}
	nonAdminCtx, err := svc.BindSession(t.Context(), nonAdminSession)
	if err != nil {
		t.Fatal(err)
	}
	staleCtx := adminCtx
	if _, err := store.pool.Exec(t.Context(), `UPDATE users SET authz_epoch=authz_epoch+1 WHERE id=$1`, admin.Principal.UserID); err != nil {
		t.Fatal(err)
	}
	reader := &countingFailReader{}
	svc.random = reader
	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "missing proof", ctx: t.Context()},
		{name: "nonadmin actor", ctx: nonAdminCtx},
		{name: "stale proof", ctx: staleCtx},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creds := mustCredentials(t, "target."+strings.ReplaceAll(tc.name, " ", "."), "a sufficiently long password")
			_, err := svc.CreateUser(tc.ctx, creds)
			want := ErrSession
			if tc.name == "stale proof" {
				want = ErrAuthorizationEpoch
			}
			if !errors.Is(err, want) {
				t.Fatalf("CreateUser error=%v, want %v", err, want)
			}
			if reader.calls != 0 {
				t.Fatalf("unauthorized call consumed session RNG %d times", reader.calls)
			}
		})
	}
}

func TestCreateUserExpensiveWorkDoesNotHoldAuthzLockAndRevalidates(t *testing.T) {
	store := NewStore(testdb.Open(t))
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminCreds := mustCredentials(t, "admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), adminCreds); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	admin := loginForAdminTest(t, svc, adminCreds)
	adminSession, err := svc.Session(t.Context(), admin.Token)
	if err != nil {
		t.Fatal(err)
	}
	adminCtx, err := svc.BindSession(t.Context(), adminSession)
	if err != nil {
		t.Fatal(err)
	}
	otherCreds := mustCredentials(t, "other.admin", "another valid password")
	other, err := svc.CreateUser(adminCtx, otherCreds)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetPlatformAdmin(adminCtx, other.UserID, true); err != nil {
		t.Fatal(err)
	}
	otherLogin := loginForAdminTest(t, svc, otherCreds)
	otherSession, err := svc.Session(t.Context(), otherLogin.Token)
	if err != nil {
		t.Fatal(err)
	}
	otherCtx, err := svc.BindSession(t.Context(), otherSession)
	if err != nil {
		t.Fatal(err)
	}
	reader := &blockingReader{started: make(chan struct{}), release: make(chan struct{})}
	svc.random = reader
	newUserCreds := mustCredentials(t, "must.not.exist", "a sufficiently long password")
	done := make(chan error, 1)
	go func() {
		_, err := svc.CreateUser(adminCtx, newUserCreds)
		done <- err
	}()
	<-reader.started

	lockTx, err := store.pool.Begin(t.Context())
	if err != nil {
		close(reader.release)
		t.Fatal(err)
	}
	var sharedLockAvailable bool
	if err := lockTx.QueryRow(t.Context(), `SELECT pg_try_advisory_xact_lock_shared($1)`, AuthzLockKey).Scan(&sharedLockAvailable); err != nil {
		_ = lockTx.Rollback(t.Context())
		close(reader.release)
		t.Fatal(err)
	}
	_ = lockTx.Rollback(t.Context())
	if !sharedLockAvailable {
		close(reader.release)
		<-done
		t.Fatal("CreateUser held exclusive authz lock during ID generation")
	}
	if err := svc.SetPlatformAdmin(otherCtx, admin.Principal.UserID, false); err != nil {
		close(reader.release)
		t.Fatal(err)
	}
	close(reader.release)
	if err := <-done; !errors.Is(err, ErrSession) {
		t.Fatalf("creation after actor demotion=%v, want denial", err)
	}
	var count int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE username='must.not.exist'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("unauthorized insert count=%d", count)
	}
}

func TestRevokeUserSessionsRevokesAllFamiliesAndSelfProof(t *testing.T) {
	store := NewStore(testdb.Open(t))
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminCreds := mustCredentials(t, "admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), adminCreds); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	admin := loginForAdminTest(t, svc, adminCreds)
	session, err := svc.Session(t.Context(), admin.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := svc.BindSession(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeUserSessions(ctx, admin.Principal.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ValidateContextFresh(ctx); !errors.Is(err, ErrSession) {
		t.Fatalf("self-revoked proof remained valid: %v", err)
	}
	if err := svc.RevokeUserSessions(ctx, admin.Principal.UserID); !errors.Is(err, ErrSession) {
		t.Fatalf("revocation by stale actor=%v", err)
	}
	if _, err := svc.CreateUser(ctx, mustCredentials(t, "after.revoke", "long enough password")); !errors.Is(err, ErrSession) {
		t.Fatalf("mutation by self-revoked actor=%v", err)
	}
}

func TestAdminMutationWaitsForSharedAuthzLockBeforeEffects(t *testing.T) {
	store := NewStore(testdb.Open(t))
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	creds := mustCredentials(t, "admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), creds); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	admin := loginForAdminTest(t, svc, creds)
	session, err := svc.Session(t.Context(), admin.Token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := svc.BindSession(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err := LockAuthz(t.Context(), tx, false); err != nil {
		t.Fatal(err)
	}
	want := mustCredentials(t, "lock.wait", "a valid long password")
	done := make(chan error, 1)
	go func() { _, err := svc.CreateUser(ctx, want); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		err := store.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%pg_advisory_xact_lock%' AND pid<>pg_backend_pid())`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("admin mutation did not wait for shared authz lock")
		}
		runtime.Gosched()
	}
	var before int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE username='lock.wait'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatal("mutation had effects while blocked")
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var after int
	if err := store.pool.QueryRow(t.Context(), `SELECT count(*) FROM users WHERE username='lock.wait'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 1 {
		t.Fatalf("post-lock mutation count=%d", after)
	}
}

func TestStaleEpochRotatesImmediatelyAndSuppressesPublicAdmin(t *testing.T) {
	store := NewStore(testdb.Open(t))
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	creds := mustCredentials(t, "admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), creds); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	svc, err := NewService(store, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	issued := loginForAdminTest(t, svc, creds)
	if _, err := store.pool.Exec(t.Context(), `UPDATE users SET authz_epoch=authz_epoch+1 WHERE id=$1`, issued.Principal.UserID); err != nil {
		t.Fatal(err)
	}
	stale, err := svc.Session(t.Context(), issued.Token)
	if err != nil || stale.Replacement == nil {
		t.Fatalf("stale epoch did not rotate before interval: replacement=%v err=%v", stale.Replacement, err)
	}
	if stale.Principal.IsPlatformAdmin || !stale.Replacement.Principal.IsPlatformAdmin {
		t.Fatalf("public authority stale=%+v successor=%+v", stale.Principal, stale.Replacement.Principal)
	}
	ctx, err := svc.BindSession(t.Context(), stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ValidateContextFresh(ctx); !errors.Is(err, ErrAuthorizationEpoch) {
		t.Fatalf("original proof validation=%v", err)
	}
}

func mustCredentials(t *testing.T, username, password string) Credentials {
	t.Helper()
	c, err := ParseCredentials(username, password)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func loginForAdminTest(t *testing.T, svc *Service, creds Credentials) IssuedSession {
	t.Helper()
	c, err := svc.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	i, err := svc.Login(t.Context(), LoginRequest{Credentials: creds, ChallengeToken: c.Token, CSRFToken: c.CSRFToken})
	if err != nil {
		t.Fatal(err)
	}
	return i
}
func mustUserHash(t *testing.T, store *Store, id string) string {
	t.Helper()
	var hash string
	if err := store.pool.QueryRow(t.Context(), `SELECT password_hash FROM users WHERE id=$1`, id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return hash
}

func TestAdminRenameAndPasswordChangeInvalidateSessionEvidence(t *testing.T) {
	store := NewStore(testdb.Open(t))
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	adminCreds := mustCredentials(t, "admin", "test login passphrase")
	if err := store.BootstrapAdmin(t.Context(), adminCreds); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	admin := loginForAdminTest(t, svc, adminCreds)
	adminSession, err := svc.Session(t.Context(), admin.Token)
	if err != nil {
		t.Fatal(err)
	}
	adminCtx, err := svc.BindSession(t.Context(), adminSession)
	if err != nil {
		t.Fatal(err)
	}
	userCreds := mustCredentials(t, "managed.user", "old password long enough")
	user, err := svc.CreateUser(adminCtx, userCreds)
	if err != nil {
		t.Fatal(err)
	}
	issued := loginForAdminTest(t, svc, userCreds)
	if err := svc.RenameUser(adminCtx, user.UserID, "renamed.user"); err != nil {
		t.Fatal(err)
	}
	users, err := svc.ListAdminUsers(adminCtx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range users {
		if u.ID == user.UserID && u.Username == "renamed.user" {
			found = true
		}
	}
	if !found {
		t.Fatalf("renamed user absent: %+v", users)
	}
	stale, err := svc.Session(t.Context(), issued.Token)
	if err != nil || stale.Replacement == nil || stale.Principal.Username != "renamed.user" || stale.Replacement.Principal.Username != "renamed.user" {
		t.Fatalf("rename did not epoch-refresh session identity: %+v %v", stale, err)
	}
	if err := svc.UpdateUserPassword(adminCtx, user.UserID, "new password long enough"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Session(t.Context(), issued.Token); !errors.Is(err, ErrSession) {
		t.Fatalf("password change preserved old family: %v", err)
	}
	if _, err := svc.LoginChallenge(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.RenameUser(adminCtx, user.UserID, "!"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid username error=%v", err)
	}
	if _, err := svc.ListAdminUsers(t.Context()); !errors.Is(err, ErrSession) {
		t.Fatalf("unauthenticated user list error=%v", err)
	}
}
