package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"sqlwise/internal/testdb"
)

func TestBindSessionRejectsForgedSession(t *testing.T) {
	store := NewStore(testdb.Open(t))
	if err := store.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	credentials, err := ParseCredentials("admin", "test login passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.BindSession(context.Background(), Session{Principal: Principal{UserID: "forged"}}); err == nil {
		t.Fatal("forged public principal accepted as session evidence")
	}
	challenge, err := service.LoginChallenge(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := service.Login(t.Context(), LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Session(t.Context(), issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	session.Principal.UserID = "forged"
	session.Principal.Username = "forged"
	session.Principal.IsPlatformAdmin = false
	session.Replacement = &IssuedSession{Session: Session{Principal: Principal{UserID: "forged"}}}
	ctx, err := service.BindSession(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := UserIDFromContext(ctx); !ok || id != issued.Principal.UserID {
		t.Fatalf("context identity=(%q,%v)", id, ok)
	}
	principal, err := service.ValidateContextFresh(ctx)
	if err != nil || principal.UserID != issued.Principal.UserID || principal.Username != "admin" || !principal.IsPlatformAdmin {
		t.Fatalf("fresh validation=%+v, %v", principal, err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE users SET authz_epoch=authz_epoch+1 WHERE id=$1`, issued.Principal.UserID); err != nil {
		t.Fatal(err)
	}
	digest, err := digestToken(issued.Token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(t.Context(), `UPDATE auth_sessions SET created_at=created_at-interval '16 minutes' WHERE token_hash=$1`, digest); err != nil {
		t.Fatal(err)
	}
	_, err = service.Session(t.Context(), issued.Token)
	if err != nil {
		t.Fatalf("stale epoch session refresh failed: %v", err)
	}
	if _, err := service.ValidateContextFresh(ctx); !errors.Is(err, ErrAuthorizationEpoch) {
		t.Fatalf("stale epoch error=%v", err)
	}
	if err := service.Logout(t.Context(), issued.Token, issued.CSRFToken); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateContextFresh(ctx); err == nil {
		t.Fatal("retained context survived logout")
	}
}

type evidenceClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *evidenceClock) Now() time.Time    { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *evidenceClock) Set(now time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.now = now }

func TestRetainedContextBoundariesAndFamilyIdentity(t *testing.T) {
	for _, scenario := range []string{"idle", "absolute", "grace", "revoked", "disabled", "epoch", "token-epoch", "family", "lock"} {
		t.Run(scenario, func(t *testing.T) {
			store := NewStore(testdb.Open(t))
			if err := store.ApplySchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			credentials, _ := ParseCredentials("admin", "test login passphrase")
			if err := store.BootstrapAdmin(t.Context(), credentials); err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
			clock := &evidenceClock{now: start}
			svc, err := NewService(store, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			login := func() IssuedSession {
				challenge, err := svc.LoginChallenge(t.Context(), "")
				if err != nil {
					t.Fatal(err)
				}
				issued, err := svc.Login(t.Context(), LoginRequest{Credentials: credentials, ChallengeToken: challenge.Token, CSRFToken: challenge.CSRFToken})
				if err != nil {
					t.Fatal(err)
				}
				return issued
			}
			issued := login()
			session, err := svc.Session(t.Context(), issued.Token)
			if err != nil {
				t.Fatal(err)
			}
			ctx, err := svc.BindSession(t.Context(), session)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.ValidateContextFresh(context.Background()); !errors.Is(err, ErrSession) {
				t.Fatalf("absent proof=%v", err)
			}
			if _, ok := UserIDFromContext(context.Background()); ok {
				t.Fatal("absent proof exposed identity")
			}
			deadline := start
			want := ErrSession
			switch scenario {
			case "idle":
				deadline = start.Add(IdleTimeout)
			case "absolute":
				deadline = start.Add(time.Minute)
				_, err = store.pool.Exec(t.Context(), `UPDATE auth_session_families SET absolute_expires_at=$1`, deadline)
			case "grace":
				clock.Set(start.Add(RotationInterval))
				rotated, rotateErr := svc.Session(t.Context(), issued.Token)
				if rotateErr != nil || rotated.Replacement == nil {
					t.Fatalf("rotation=%v %v", rotated.Replacement, rotateErr)
				}
				if _, bindErr := svc.BindSession(t.Context(), rotated.Replacement.Session); !errors.Is(bindErr, ErrSession) {
					t.Fatalf("unresolved successor proof accepted: %v", bindErr)
				}
				deadline = start.Add(RotationInterval + AliasGrace)
			case "revoked":
				err = svc.Logout(t.Context(), issued.Token, issued.CSRFToken)
			case "disabled":
				_, err = store.pool.Exec(t.Context(), `UPDATE users SET active=false`)
			case "epoch":
				_, err = store.pool.Exec(t.Context(), `UPDATE users SET authz_epoch=authz_epoch+1`)
				want = ErrAuthorizationEpoch
			case "token-epoch":
				_, err = store.pool.Exec(t.Context(), `UPDATE auth_sessions SET authz_epoch=authz_epoch+1 WHERE token_hash=$1`, mustDigest(t, issued.Token))
				want = ErrAuthorizationEpoch
			case "family":
				other := login()
				if _, err := store.pool.Exec(t.Context(), `UPDATE auth_sessions SET rotated_at=$1,alias_until=$2 WHERE token_hash=$3`, start, start.Add(AliasGrace), mustDigest(t, issued.Token)); err != nil {
					t.Fatal(err)
				}
				_, err = store.pool.Exec(t.Context(), `UPDATE auth_sessions SET family_id=(SELECT family_id FROM auth_sessions WHERE token_hash=$1) WHERE token_hash=$2`, mustDigest(t, other.Token), mustDigest(t, issued.Token))
			case "lock":
				tx, err := store.pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				if err := LockAuthz(ctx, tx, true); err != nil {
					t.Fatal(err)
				}
				// Access mutations already hold the exclusive lock before validating proof.
				if _, err := svc.ValidateContext(ctx, tx); err != nil {
					t.Fatal(err)
				}
				conn, err := store.pool.Acquire(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Release()
				waitingTx, err := conn.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer waitingTx.Rollback(context.Background())
				waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				pid := conn.Conn().PgConn().PID()
				result := make(chan error, 1)
				go func() { _, err := svc.ValidateContext(waitCtx, waitingTx); result <- err }()
				for {
					var waiting bool
					if err := store.pool.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND NOT granted)`, pid).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					select {
					case err := <-result:
						t.Fatalf("validation bypassed exclusive lock: %v", err)
					default:
					}
				}
				clock.Set(start.Add(IdleTimeout))
				if err := tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := <-result; !errors.Is(err, ErrSession) {
					t.Fatalf("validation sampled pre-lock clock: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if deadline.After(start) {
				clock.Set(deadline.Add(-time.Nanosecond))
				if _, err := svc.ValidateContextFresh(ctx); err != nil {
					t.Fatalf("before exact deadline: %v", err)
				}
				clock.Set(deadline)
			}
			if _, err := svc.ValidateContextFresh(ctx); !errors.Is(err, want) {
				t.Fatalf("retained %s context error=%v want %v", scenario, err, want)
			}
		})
	}
}
