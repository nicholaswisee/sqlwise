package access

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sqlwise/internal/auth"

	"github.com/jackc/pgx/v5"
)

func (f *policyFixture) sessionState(t *testing.T) map[string]string {
	t.Helper()
	state := make(map[string]string)
	for _, id := range []string{"admin", "one", "two", "other"} {
		state[id] = f.userState(t, id)
	}
	return state
}

func TestRegisterDatabaseAttachesInSameTransaction(t *testing.T) {
	f := newPolicyFixture(t)
	sessions := f.sessionState(t)
	var attached string
	registration, err := f.service.RegisterDatabase(f.ctx["admin"], "  Analytics 世界  ", func(tx pgx.Tx, databaseID string) error {
		attached = databaseID
		var name string
		var active bool
		if err := tx.QueryRow(t.Context(), `SELECT name,active FROM database_registrations WHERE id=$1`, databaseID).Scan(&name, &active); err != nil {
			return err
		}
		if name != "Analytics 世界" || !active {
			t.Fatalf("attach saw %q active=%v", name, active)
		}
		return nil
	})
	if err != nil || registration.ID == "" || registration.ID != attached || registration.Name != "Analytics 世界" || !registration.Active {
		t.Fatalf("registration=%+v attached=%q err=%v", registration, attached, err)
	}
	for id, before := range sessions {
		if f.userState(t, id) != before {
			t.Fatalf("registering changed %s sessions", id)
		}
	}
	if err := f.service.SetGrant(f.ctx["admin"], "g", registration.ID, View); err != nil {
		t.Fatalf("grant on new registration: %v", err)
	}
	second, err := f.service.RegisterDatabase(f.ctx["admin"], "Analytics 世界", func(pgx.Tx, string) error { return nil })
	if err != nil || second.ID == registration.ID {
		t.Fatalf("second registration=%+v err=%v", second, err)
	}
}

func TestRegisterDatabaseRollsBackWithAttach(t *testing.T) {
	f := newPolicyFixture(t)
	before := f.state(t)
	failure := errors.New("connection details rejected")
	_, err := f.service.RegisterDatabase(f.ctx["admin"], "Rolled back", func(tx pgx.Tx, databaseID string) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO groups(id,name) VALUES('inside','Inside attach')`); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	if f.state(t) != before {
		t.Fatal("failed attach left rows behind")
	}
}

func TestUpdateDatabaseRenamesAndAttaches(t *testing.T) {
	f := newPolicyFixture(t)
	sessions := f.sessionState(t)
	called := false
	if err := f.service.UpdateDatabase(f.ctx["admin"], "db", " Renamed ", func(tx pgx.Tx) error {
		called = true
		return nil
	}); err != nil || !called {
		t.Fatalf("update err=%v called=%v", err, called)
	}
	var name string
	var active bool
	var grants int
	if err := f.pool.QueryRow(t.Context(), `SELECT name,active,(SELECT count(*) FROM group_database_grants WHERE database_id='db') FROM database_registrations WHERE id='db'`).Scan(&name, &active, &grants); err != nil {
		t.Fatal(err)
	}
	if name != "Renamed" || !active || grants != 2 {
		t.Fatalf("registration name=%q active=%v grants=%d", name, active, grants)
	}
	for id, before := range sessions {
		if f.userState(t, id) != before {
			t.Fatalf("renaming changed %s sessions", id)
		}
	}

	before := f.state(t)
	failure := errors.New("physical target changed")
	if err := f.service.UpdateDatabase(f.ctx["admin"], "db", "Not saved", func(pgx.Tx) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("attach failure: %v", err)
	}
	if err := f.service.UpdateDatabase(f.ctx["admin"], "missing", "Missing", func(pgx.Tx) error {
		t.Fatal("attach ran for a missing registration")
		return nil
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing registration: %v", err)
	}
	if f.state(t) != before {
		t.Fatal("failed update changed rows")
	}
}

func TestRegistrationMutationsValidateAndRequireAdmin(t *testing.T) {
	f := newPolicyFixture(t)
	register := func(c context.Context, name string) error {
		_, err := f.service.RegisterDatabase(c, name, func(pgx.Tx, string) error {
			t.Fatal("attach ran")
			return nil
		})
		return err
	}
	update := func(c context.Context, name string) error {
		return f.service.UpdateDatabase(c, "db", name, func(pgx.Tx) error {
			t.Fatal("attach ran")
			return nil
		})
	}
	before := f.state(t)
	for _, name := range []string{"", "   ", "a\x00b", "\xff", strings.Repeat("a", 129)} {
		if err := register(f.ctx["admin"], name); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("register %q: %v", name, err)
		}
		if err := update(f.ctx["admin"], name); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("update %q: %v", name, err)
		}
	}
	if f.state(t) != before {
		t.Fatal("invalid names had effects")
	}
	stale := f.ctx["admin"]
	f.exec(t, `UPDATE users SET authz_epoch=authz_epoch+1 WHERE id='admin'`)
	before = f.state(t)
	for name, tc := range map[string]struct {
		actor context.Context
		want  error
	}{
		"missing":  {t.Context(), auth.ErrSession},
		"nonadmin": {f.ctx["one"], auth.ErrSession},
		"stale":    {stale, auth.ErrAuthorizationEpoch},
	} {
		if err := register(tc.actor, "Denied"); !errors.Is(err, tc.want) {
			t.Fatalf("%s register: %v want %v", name, err, tc.want)
		}
		if err := update(tc.actor, "Denied"); !errors.Is(err, tc.want) {
			t.Fatalf("%s update: %v want %v", name, err, tc.want)
		}
	}
	if f.state(t) != before {
		t.Fatal("denied registration mutation had effects")
	}
}
