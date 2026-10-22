package access

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sqlwise/internal/auth"
	"sqlwise/internal/testdb"

	"github.com/jackc/pgx/v5/pgxpool"
)

type policyMutator interface {
	SetGroupActive(context.Context, string, bool) error
	AddMembership(context.Context, string, string) error
	RemoveMembership(context.Context, string, string) error
	SetGrant(context.Context, string, string, Permission) error
	RemoveGrant(context.Context, string, string) error
	SetDatabaseActive(context.Context, string, bool) error
}

type policyFixture struct {
	pool      *pgxpool.Pool
	sessions  *auth.Service
	service   *Service
	mutations policyMutator
	clock     atomic.Int64
	ctx       map[string]context.Context
	tokens    map[string][]string
}

func newPolicyFixture(t *testing.T) *policyFixture {
	t.Helper()
	f := &policyFixture{pool: testdb.Open(t), ctx: make(map[string]context.Context), tokens: make(map[string][]string)}
	f.clock.Store(time.Now().UTC().Truncate(time.Microsecond).UnixNano())
	store := auth.NewStore(f.pool)
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(f.pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("sufficiently long evaluator password")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"admin", "one", "two", "other"} {
		if _, err := f.pool.Exec(t.Context(), `INSERT INTO users(id,username,password_hash,is_platform_admin) VALUES($1,$1,$2,$3)`, id, hash, id == "admin"); err != nil {
			t.Fatal(err)
		}
	}
	f.sessions, err = auth.NewService(store, func() time.Time { return time.Unix(0, f.clock.Load()).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	f.service = NewService(NewStore(f.pool), f.sessions)
	for _, id := range []string{"admin", "one", "two", "other"} {
		for range 2 {
			issued := evaluatorLogin(t, f.sessions, evaluatorCredentials(t, id))
			f.tokens[id] = append(f.tokens[id], issued.Token)
		}
	}
	// Actual periodic rotation supplies an old alias and a current proof in each family.
	f.clock.Add(int64(auth.RotationInterval))
	for id, tokens := range f.tokens {
		for _, token := range tokens {
			s := mustSession(t, f.sessions, token)
			if s.Replacement == nil {
				t.Fatal("fixture did not rotate")
			}
			f.tokens[id] = append(f.tokens[id], s.Replacement.Token)
			f.ctx[id], err = f.sessions.BindSession(t.Context(), mustSession(t, f.sessions, s.Replacement.Token))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	f.exec(t, `INSERT INTO groups(id,name) VALUES('g','Primary'),('overlap','Overlap')`)
	f.exec(t, `INSERT INTO database_registrations(id,name,active) VALUES('db','Database',true),('db2','Second',true)`)
	f.exec(t, `INSERT INTO group_memberships(user_id,group_id) VALUES('one','g'),('two','g'),('one','overlap'),('two','overlap')`)
	f.exec(t, `INSERT INTO group_database_grants(group_id,database_id,permission) VALUES('g','db','view'),('overlap','db','edit'),('g','db2','view')`)
	var ok bool
	f.mutations, ok = any(f.service).(policyMutator)
	if !ok {
		t.Fatal("access.Service has no protected group/membership/grant/registration mutation API")
	}
	return f
}

func (f *policyFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func (f *policyFixture) state(t *testing.T) string {
	t.Helper()
	var state string
	// Complete row snapshots catch unintended timestamps, aliases, identities and epochs too.
	if err := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
		(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM users x),
		(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM groups x),
		(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM database_registrations x),
		(SELECT jsonb_agg(to_jsonb(x) ORDER BY user_id,group_id) FROM group_memberships x),
		(SELECT jsonb_agg(to_jsonb(x) ORDER BY group_id,database_id) FROM group_database_grants x),
		(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM auth_session_families x),
		(SELECT jsonb_agg(to_jsonb(x) ORDER BY token_hash) FROM auth_sessions x))::text`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func (f *policyFixture) userState(t *testing.T, id string) string {
	t.Helper()
	var state string
	if err := f.pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
		(SELECT to_jsonb(u) FROM users u WHERE id=$1),
		(SELECT jsonb_agg(to_jsonb(f) ORDER BY id) FROM auth_session_families f WHERE user_id=$1),
		(SELECT jsonb_agg(to_jsonb(s) ORDER BY token_hash) FROM auth_sessions s WHERE user_id=$1))::text`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestPolicyMutationInvalidationMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, setup string
		increase    bool
		users       []string
		mutate      func(policyMutator, context.Context) error
	}{
		{"add-membership", `DELETE FROM group_memberships WHERE group_id='g' AND user_id='one'`, true, []string{"one"}, func(m policyMutator, c context.Context) error { return m.AddMembership(c, "one", "g") }},
		{"remove-membership", "", false, []string{"one"}, func(m policyMutator, c context.Context) error { return m.RemoveMembership(c, "one", "g") }},
		{"insert-grant", `DELETE FROM group_database_grants WHERE group_id='g' AND database_id='db'`, true, []string{"one", "two"}, func(m policyMutator, c context.Context) error { return m.SetGrant(c, "g", "db", View) }},
		{"upgrade-grant", "", true, []string{"one", "two"}, func(m policyMutator, c context.Context) error { return m.SetGrant(c, "g", "db", Edit) }},
		{"downgrade-grant", `UPDATE group_database_grants SET permission='edit' WHERE group_id='g' AND database_id='db'`, false, []string{"one", "two"}, func(m policyMutator, c context.Context) error { return m.SetGrant(c, "g", "db", View) }},
		{"remove-grant", "", false, []string{"one", "two"}, func(m policyMutator, c context.Context) error { return m.RemoveGrant(c, "g", "db") }},
		{"activate-group", `UPDATE groups SET active=false WHERE id='g'`, true, []string{"one", "two"}, func(m policyMutator, c context.Context) error { return m.SetGroupActive(c, "g", true) }},
		{"disable-group", "", false, []string{"one", "two"}, func(m policyMutator, c context.Context) error { return m.SetGroupActive(c, "g", false) }},
		{"activate-registration", `UPDATE database_registrations SET active=false WHERE id='db'`, true, []string{"one", "two"}, func(m policyMutator, c context.Context) error { return m.SetDatabaseActive(c, "db", true) }},
		{"disable-registration", "", false, []string{"one", "two"}, func(m policyMutator, c context.Context) error { return m.SetDatabaseActive(c, "db", false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPolicyFixture(t)
			if tc.setup != "" {
				f.exec(t, tc.setup)
			}
			before := f.state(t)
			userBefore := make(map[string]string)
			for _, id := range []string{"admin", "one", "two", "other"} {
				userBefore[id] = f.userState(t, id)
			}
			if err := tc.mutate(f.mutations, f.ctx["admin"]); err != nil {
				t.Fatal(err)
			}
			if f.state(t) == before {
				t.Fatal("business mutation had no effect")
			}
			after := f.state(t)
			if err := tc.mutate(f.mutations, f.ctx["admin"]); err != nil || after != f.state(t) {
				t.Fatalf("repeated mutation was not a no-op: %v", err)
			}
			var member, groupActive, dbActive bool
			var grant string
			if err := f.pool.QueryRow(t.Context(), `SELECT
				EXISTS(SELECT 1 FROM group_memberships WHERE user_id='one' AND group_id='g'),
				(SELECT active FROM groups WHERE id='g'),
				(SELECT active FROM database_registrations WHERE id='db'),
				COALESCE((SELECT permission FROM group_database_grants WHERE group_id='g' AND database_id='db'),'none')`).Scan(&member, &groupActive, &dbActive, &grant); err != nil {
				t.Fatal(err)
			}
			wantGrant := "view"
			if tc.name == "upgrade-grant" {
				wantGrant = "edit"
			}
			if tc.name == "remove-grant" {
				wantGrant = "none"
			}
			if member != (tc.name != "remove-membership") || groupActive != (tc.name != "disable-group") || dbActive != (tc.name != "disable-registration") || grant != wantGrant {
				t.Fatalf("business state: member=%v group=%v db=%v grant=%s", member, groupActive, dbActive, grant)
			}
			for _, id := range []string{"admin", "one", "two", "other"} {
				affected := false
				for _, u := range tc.users {
					affected = affected || id == u
				}
				var epoch, revoked int
				if err := f.pool.QueryRow(t.Context(), `SELECT authz_epoch,(SELECT count(*) FROM auth_session_families WHERE user_id=$1 AND revoked_at IS NOT NULL) FROM users WHERE id=$1`, id).Scan(&epoch, &revoked); err != nil {
					t.Fatal(err)
				}
				wantEpoch, wantRevoked := 0, 0
				if affected {
					if tc.increase {
						wantEpoch = 1
					} else {
						wantRevoked = 2
					}
				}
				if epoch != wantEpoch || revoked != wantRevoked {
					t.Fatalf("%s epoch/revoked=%d/%d want %d/%d", id, epoch, revoked, wantEpoch, wantRevoked)
				}
				_, err := f.sessions.ValidateContextFresh(f.ctx[id])
				if !affected {
					if userBefore[id] != f.userState(t, id) {
						t.Fatalf("unrelated %s rows changed", id)
					}
					if err != nil {
						t.Fatal("unrelated proof:", err)
					}
					continue
				}
				if tc.increase {
					if !errors.Is(err, auth.ErrAuthorizationEpoch) {
						t.Fatalf("old proof: %v", err)
					}
					// Existing aliases cannot mint new authority; current tokens rotate immediately,
					// well before the next periodic deadline, and only successors authorize.
					for i, token := range f.tokens[id] {
						s := mustSession(t, f.sessions, token)
						if i < 2 && s.Replacement != nil {
							t.Fatal("alias upgraded")
						}
						c, err := f.sessions.BindSession(t.Context(), s)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := f.service.EffectivePermission(c, id, "db"); !errors.Is(err, auth.ErrAuthorizationEpoch) {
							t.Fatalf("old token authorized: %v", err)
						}
						if i >= 2 {
							if s.Replacement == nil {
								t.Fatal("no immediate epoch rotation")
							}
							fresh, err := f.sessions.BindSession(t.Context(), mustSession(t, f.sessions, s.Replacement.Token))
							if err != nil {
								t.Fatal(err)
							}
							if _, err := f.service.EffectivePermission(fresh, id, "db"); err != nil {
								t.Fatal("successor denied:", err)
							}
						}
					}
				} else {
					if !errors.Is(err, auth.ErrSession) {
						t.Fatalf("revoked proof: %v", err)
					}
					for _, token := range f.tokens[id] {
						if _, err := f.sessions.Session(t.Context(), token); !errors.Is(err, auth.ErrSession) {
							t.Fatalf("family token survived: %v", err)
						}
					}
				}
			}
			// A new login sees the new effective policy, independent of revoked/stale
			// families. The overlap keeps db editable except when its registration is off.
			issued := evaluatorLogin(t, f.sessions, evaluatorCredentials(t, "one"))
			fresh, err := f.sessions.BindSession(t.Context(), mustSession(t, f.sessions, issued.Token))
			if err != nil {
				t.Fatal(err)
			}
			wantDB, wantDB2 := Edit, View
			if tc.name == "disable-registration" {
				wantDB = None
			}
			if tc.name == "disable-group" || tc.name == "remove-membership" {
				wantDB2 = None
			}
			for db, want := range map[string]Permission{"db": wantDB, "db2": wantDB2} {
				if got, err := f.service.EffectivePermission(fresh, "one", db); err != nil || got != want {
					t.Fatalf("fresh %s permission=%s,%v want %s", db, got, err, want)
				}
			}
			if !tc.increase {
				// All removed/disabled policies can be re-enabled, never resurrecting a family.
				for _, err := range []error{f.mutations.AddMembership(f.ctx["admin"], "one", "g"), f.mutations.SetDatabaseActive(f.ctx["admin"], "db", true), f.mutations.SetGrant(f.ctx["admin"], "g", "db", Edit), f.mutations.SetGroupActive(f.ctx["admin"], "g", true)} {
					if err != nil {
						t.Fatal(err)
					}
				}
				for _, id := range tc.users {
					for _, token := range f.tokens[id] {
						if _, err := f.sessions.Session(t.Context(), token); !errors.Is(err, auth.ErrSession) {
							t.Fatalf("reenable revived token: %v", err)
						}
					}
				}
				// The post-revocation login survives re-enable only through epoch rotation.
				if _, err := f.sessions.ValidateContextFresh(fresh); !errors.Is(err, auth.ErrAuthorizationEpoch) {
					t.Fatalf("reenable survivor did not require fresh authorization: %v", err)
				}
			}
			var name string
			var grants int
			if err := f.pool.QueryRow(t.Context(), `SELECT name,(SELECT count(*) FROM group_database_grants WHERE database_id='db2') FROM database_registrations WHERE id='db'`).Scan(&name, &grants); err != nil || name != "Database" || grants != 1 {
				t.Fatalf("identity/multiDB changed: %q %d %v", name, grants, err)
			}
		})
	}
}

// Synchronize on actual PostgreSQL lock queues, not elapsed sleeps.
func waitPolicyLock(t *testing.T, pool *pgxpool.Pool, mode string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=$1 AND NOT granted AND mode=$2)`, auth.AuthzLockKey, mode).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		runtime.Gosched()
	}
}

func TestPolicyNoopsValidationAndFreshAdmin(t *testing.T) {
	f := newPolicyFixture(t)
	c := f.ctx["admin"]
	for _, mutate := range []func(context.Context) error{
		func(c context.Context) error { return f.mutations.SetGroupActive(c, "g", true) },
		func(c context.Context) error { return f.mutations.SetDatabaseActive(c, "db", true) },
		func(c context.Context) error { return f.mutations.AddMembership(c, "one", "g") },
		func(c context.Context) error { return f.mutations.RemoveMembership(c, "other", "g") },
		func(c context.Context) error { return f.mutations.SetGrant(c, "g", "db", View) },
		func(c context.Context) error { return f.mutations.RemoveGrant(c, "overlap", "db2") },
	} {
		before := f.state(t)
		if err := mutate(c); err != nil {
			t.Fatal(err)
		}
		if before != f.state(t) {
			t.Fatal("no-op changed policy/session rows")
		}
	}
	for _, tc := range []struct {
		name string
		want error
		call func() error
	}{
		{"missing-group", ErrNotFound, func() error { return f.mutations.SetGroupActive(c, "missing", false) }},
		{"missing-registration", ErrNotFound, func() error { return f.mutations.SetDatabaseActive(c, "missing", false) }},
		{"add-missing-user", ErrNotFound, func() error { return f.mutations.AddMembership(c, "missing", "g") }},
		{"remove-missing-user", ErrNotFound, func() error { return f.mutations.RemoveMembership(c, "missing", "g") }},
		{"add-missing-group", ErrNotFound, func() error { return f.mutations.AddMembership(c, "one", "missing") }},
		{"remove-missing-group", ErrNotFound, func() error { return f.mutations.RemoveMembership(c, "one", "missing") }},
		{"grant-missing-group", ErrNotFound, func() error { return f.mutations.SetGrant(c, "missing", "db", Edit) }},
		{"grant-missing-registration", ErrNotFound, func() error { return f.mutations.SetGrant(c, "g", "missing", Edit) }},
		{"remove-grant-missing-group", ErrNotFound, func() error { return f.mutations.RemoveGrant(c, "missing", "db") }},
		{"remove-grant-missing-registration", ErrNotFound, func() error { return f.mutations.RemoveGrant(c, "g", "missing") }},
		{"none-grant", ErrInvalidPermission, func() error { return f.mutations.SetGrant(c, "g", "db", None) }},
		{"unknown-grant", ErrInvalidPermission, func() error { return f.mutations.SetGrant(c, "g", "db", Permission(99)) }},
		{"opaque-injection", ErrNotFound, func() error { return f.mutations.SetGroupActive(c, "g'; DELETE FROM users; --", false) }},
		{"invalid-id", ErrInvalidInput, func() error { return f.mutations.SetGroupActive(c, "\x00", false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := f.state(t)
			if err := tc.call(); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v want %v", err, tc.want)
			}
			if before != f.state(t) {
				t.Fatal("failed mutation changed rows")
			}
		})
	}
	// Public principal fields cannot promote an authenticated regular user.
	forged := mustSession(t, f.sessions, f.tokens["one"][2])
	forged.Principal.IsPlatformAdmin = true
	forged.Principal.UserID = "admin"
	forgedCtx, err := f.sessions.BindSession(t.Context(), forged)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE users SET authz_epoch=authz_epoch+1 WHERE id='admin'`)
	for name, actor := range map[string]context.Context{"missing": t.Context(), "nonadmin": f.ctx["one"], "forged": forgedCtx, "stale": c} {
		t.Run(name, func(t *testing.T) {
			before := f.state(t)
			for _, call := range []func() error{
				func() error { _, err := f.service.CreateGroup(actor, "Unauthorized"); return err },
				func() error { return f.mutations.SetGroupActive(actor, "g", false) },
				func() error { return f.mutations.AddMembership(actor, "other", "g") },
				func() error { return f.mutations.RemoveMembership(actor, "one", "g") },
				func() error { return f.mutations.SetGrant(actor, "g", "db", Edit) },
				func() error { return f.mutations.RemoveGrant(actor, "g", "db") },
				func() error { return f.mutations.SetDatabaseActive(actor, "db", false) },
			} {
				want := auth.ErrSession
				if name == "stale" {
					want = auth.ErrAuthorizationEpoch
				}
				if err := call(); !errors.Is(err, want) {
					t.Fatalf("denied actor: %v want %v", err, want)
				}
			}
			if before != f.state(t) {
				t.Fatal("unauthorized actor had effects")
			}
		})
	}
}

func TestPolicyCreateGroupAndSelfInvalidation(t *testing.T) {
	f := newPolicyFixture(t)
	c := f.ctx["admin"]
	g, err := f.service.CreateGroup(c, "  Operators 世界  ")
	if err != nil || g.ID == "" || g.Name != "Operators 世界" || !g.Active {
		t.Fatalf("group=%+v err=%v", g, err)
	}
	if _, err := f.service.CreateGroup(c, g.Name); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	for _, name := range []string{"", " \t\n", strings.Repeat("界", 129), string([]byte{255}), "zero\x00byte"} {
		before := f.state(t)
		if _, err := f.service.CreateGroup(c, name); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid name %q: %v", name, err)
		}
		if before != f.state(t) {
			t.Fatal("invalid name effects")
		}
	}
	if _, err := f.service.CreateGroup(c, strings.Repeat("界", 128)); err != nil {
		t.Fatal("valid 128 Unicode characters:", err)
	}
	if err := f.mutations.AddMembership(c, "admin", g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sessions.ValidateContextFresh(c); !errors.Is(err, auth.ErrAuthorizationEpoch) {
		t.Fatalf("self increase proof=%v", err)
	}
	s := mustSession(t, f.sessions, f.tokens["admin"][2])
	if s.Replacement == nil {
		t.Fatal("no self rotation")
	}
	c, err = f.sessions.BindSession(t.Context(), mustSession(t, f.sessions, s.Replacement.Token))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.mutations.RemoveMembership(c, "admin", g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sessions.ValidateContextFresh(c); !errors.Is(err, auth.ErrSession) {
		t.Fatalf("self revoke=%v", err)
	}
}

func TestPolicyRollbackAfterBusinessWrite(t *testing.T) {
	for _, increase := range []bool{true, false} {
		t.Run(fmt.Sprint(increase), func(t *testing.T) {
			f := newPolicyFixture(t)
			// Raising only when invalidation executes proves the business UPDATE preceded
			// the injected failure. Both the policy write and any invalidation roll back.
			table, field := "auth_session_families", "revoked_at"
			if increase {
				table, field = "users", "authz_epoch"
			}
			f.exec(t, `CREATE FUNCTION reject_policy() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
				IF NOT EXISTS(SELECT 1 FROM group_database_grants WHERE group_id='g' AND database_id='db' AND permission='edit') THEN RAISE EXCEPTION 'business write missing'; END IF;
				RAISE EXCEPTION 'forced after business write'; END $$`)
			if !increase {
				f.exec(t, `UPDATE group_database_grants SET permission='edit' WHERE group_id='g' AND database_id='db'`)
			}
			// Downgrade checks the new view value, escalation checks edit.
			if !increase {
				f.exec(t, `CREATE OR REPLACE FUNCTION reject_policy() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
				IF NOT EXISTS(SELECT 1 FROM group_database_grants WHERE group_id='g' AND database_id='db' AND permission='view') THEN RAISE EXCEPTION 'business write missing'; END IF;
				RAISE EXCEPTION 'forced after business write'; END $$`)
			}
			f.exec(t, "CREATE TRIGGER reject_policy BEFORE UPDATE OF "+field+" ON "+table+" FOR EACH ROW EXECUTE FUNCTION reject_policy()")
			before := f.state(t)
			p := View
			if increase {
				p = Edit
			}
			err := f.mutations.SetGrant(f.ctx["admin"], "g", "db", p)
			if err == nil || !strings.Contains(err.Error(), "forced after business write") {
				t.Fatalf("did not reach post-write trigger: %v", err)
			}
			if before != f.state(t) {
				t.Fatal("business/session effects survived rollback")
			}
			for _, id := range []string{"admin", "one", "two", "other"} {
				if _, err := f.sessions.ValidateContextFresh(f.ctx[id]); err != nil {
					t.Fatal("rollback damaged proof:", err)
				}
			}
		})
	}
}

func TestPolicyExclusiveLockBeforeValidationAndWrites(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprint(expire), func(t *testing.T) {
			f := newPolicyFixture(t)
			tx, err := f.pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if err := auth.LockAuthz(t.Context(), tx, false); err != nil {
				t.Fatal(err)
			}
			before := f.state(t)
			done := make(chan error, 1)
			go func() { done <- f.mutations.SetDatabaseActive(f.ctx["admin"], "db", false) }()
			waitPolicyLock(t, f.pool, "ExclusiveLock")
			if before != f.state(t) {
				t.Fatal("mutation wrote before exclusive lock")
			}
			if expire {
				f.clock.Add(int64(auth.IdleTimeout))
			} else {
				f.clock.Add(int64(time.Second))
			}
			if err := tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			err = <-done
			if expire {
				if !errors.Is(err, auth.ErrSession) || before != f.state(t) {
					t.Fatalf("pre-lock clock/validation: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var revokedAt time.Time
			if err := f.pool.QueryRow(t.Context(), `SELECT revoked_at FROM auth_session_families WHERE user_id='one' LIMIT 1`).Scan(&revokedAt); err != nil || !revokedAt.Equal(time.Unix(0, f.clock.Load())) {
				t.Fatalf("revocation sampled pre-lock time: %v %v", revokedAt, err)
			}
			if _, err := f.service.EffectivePermission(f.ctx["one"], "one", "db"); !errors.Is(err, auth.ErrSession) {
				t.Fatalf("post-commit stale authorization: %v", err)
			}
		})
	}
}

func TestPolicyAndSeparateSessionRotationLockOrder(t *testing.T) {
	for _, sessionFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(sessionFirst), func(t *testing.T) {
			f := newPolicyFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer func() { once.Do(func() { close(release) }) }()
			blockingClock := func() time.Time {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return time.Unix(0, f.clock.Load()).UTC()
			}
			// Only the first clock sample blocks/signals; ValidateContext and Now both sample.
			var sampled atomic.Bool
			clock := func() time.Time {
				if sampled.CompareAndSwap(false, true) {
					return blockingClock()
				}
				return time.Unix(0, f.clock.Load()).UTC()
			}
			separate, err := auth.NewService(auth.NewStore(f.pool), clock)
			if err != nil {
				t.Fatal(err)
			}
			policy := f.service
			rotation := f.sessions
			if sessionFirst {
				rotation = separate
				f.clock.Add(int64(auth.RotationInterval))
			} else {
				policy = NewService(NewStore(f.pool), separate)
			}
			rotated := make(chan struct {
				s   auth.Session
				err error
			}, 1)
			changed := make(chan error, 1)
			rotate := func() {
				s, err := rotation.Session(ctx, f.tokens["one"][2])
				rotated <- struct {
					s   auth.Session
					err error
				}{s, err}
			}
			change := func() { changed <- policy.SetGrant(f.ctx["admin"], "g", "db", Edit) }
			before := f.state(t)
			if sessionFirst {
				go rotate()
			} else {
				go change()
			}
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first operation did not take locks")
			}
			if sessionFirst {
				go change()
				waitPolicyLock(t, f.pool, "ExclusiveLock")
			} else {
				go rotate()
				waitPolicyLock(t, f.pool, "ShareLock")
			}
			if before != f.state(t) {
				t.Fatal("writes before first locked clock released")
			}
			once.Do(func() { close(release) })
			if err := <-changed; err != nil {
				t.Fatal(err)
			}
			result := <-rotated
			if result.err != nil || result.s.Replacement == nil {
				t.Fatalf("rotation=%+v,%v", result.s.Replacement, result.err)
			}
			old, err := f.sessions.BindSession(t.Context(), result.s)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.EffectivePermission(old, "one", "db"); !errors.Is(err, auth.ErrAuthorizationEpoch) {
				t.Fatalf("old proof gained authority: %v", err)
			}
			s := mustSession(t, f.sessions, result.s.Replacement.Token)
			if sessionFirst { // Rotation linearized before the policy; that successor is stale too.
				if s.Replacement == nil {
					t.Fatal("pre-policy successor failed to rotate again")
				}
				s = mustSession(t, f.sessions, s.Replacement.Token)
			}
			fresh, err := f.sessions.BindSession(t.Context(), s)
			if err != nil {
				t.Fatal(err)
			}
			if p, err := f.service.EffectivePermission(fresh, "one", "db"); err != nil || p != Edit {
				t.Fatalf("fresh permission=%v,%v", p, err)
			}
		})
	}
}

func TestGrantCannotBeSetForInactiveRegistrationButCanBeRemoved(t *testing.T) {
	f := newPolicyFixture(t)
	if err := f.mutations.SetDatabaseActive(f.ctx["admin"], "db", false); err != nil {
		t.Fatal(err)
	}
	if err := f.mutations.SetGrant(f.ctx["admin"], "g", "db", Edit); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetGrant for inactive database error=%v, want ErrNotFound", err)
	}
	var permission string
	if err := f.pool.QueryRow(t.Context(), `SELECT permission FROM group_database_grants WHERE group_id='g' AND database_id='db'`).Scan(&permission); err != nil {
		t.Fatal(err)
	}
	if permission != "view" {
		t.Fatalf("rejected grant changed existing permission to %q", permission)
	}
	if err := f.mutations.RemoveGrant(f.ctx["admin"], "g", "db"); err != nil {
		t.Fatalf("RemoveGrant for inactive database: %v", err)
	}
	var exists bool
	if err := f.pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM group_database_grants WHERE group_id='g' AND database_id='db')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("grant remains after cleanup")
	}
}
