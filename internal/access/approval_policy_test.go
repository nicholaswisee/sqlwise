package access

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"sqlwise/internal/auth"
)

func TestGroupLeaderAndApprovalPolicy(t *testing.T) {
	f := newPolicyFixture(t)
	if err := f.service.SetGroupLeader(f.ctx["one"], "g", "one"); !errors.Is(err, auth.ErrSession) {
		t.Fatalf("non-admin assignment = %v", err)
	}
	if err := f.service.SetGroupLeader(f.ctx["admin"], "missing", "one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing group = %v", err)
	}
	if err := f.service.SetGroupLeader(f.ctx["admin"], "g", "other"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("non-member assignment = %v", err)
	}
	if err := f.service.SetGroupLeader(f.ctx["admin"], "g", "one"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.SetGroupLeader(f.ctx["admin"], "overlap", "two"); err != nil {
		t.Fatal(err)
	}
	read := func() []ApprovalGroup {
		t.Helper()
		var groups []ApprovalGroup
		err := pgx.BeginFunc(t.Context(), f.pool, func(tx pgx.Tx) error {
			var err error
			groups, err = f.service.ApprovalPolicy(t.Context(), tx, "db")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return groups
	}
	wantMembers := []ApprovalMember{{UserID: "one", Active: true}, {UserID: "two", Active: true}}
	want := []ApprovalGroup{
		{GroupID: "g", Permission: View, LeaderUserID: "one", LeaderActive: true, Members: wantMembers},
		{GroupID: "overlap", Permission: Edit, LeaderUserID: "two", LeaderActive: true, Members: wantMembers},
	}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("approval policy = %+v, want %+v", got, want)
	}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Fatalf("approval policy is unstable: %+v", got)
	}
	if err := pgx.BeginFunc(t.Context(), f.pool, func(tx pgx.Tx) error {
		permission, err := permissionFor(t.Context(), tx, "one", "db")
		if err != nil {
			return err
		}
		if permission != Edit {
			t.Fatalf("existing grant unexpectedly changed: %s", permission)
		}
		permission, err = permissionFor(t.Context(), tx, "one", "db2")
		if err != nil {
			return err
		}
		if permission != View {
			t.Fatalf("leader gained edit permission: %s", permission)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE users SET active=false WHERE id='one'`)
	if err := f.service.SetGroupLeader(f.ctx["admin"], "g", "one"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("inactive leader assignment = %v", err)
	}
	if got := read(); got[0].LeaderUserID != "one" || got[0].LeaderActive || got[0].Members[0].Active {
		t.Fatalf("inactive leader not visible: %+v", got[0])
	}
	f.exec(t, `UPDATE users SET active=true WHERE id='one'`)
	if err := f.service.RemoveMembership(f.ctx["admin"], "one", "g"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got[0].LeaderUserID != "" || got[0].LeaderActive || len(got[0].Members) != 1 {
		t.Fatalf("removed leader not cleared: %+v", got[0])
	}
	if err := f.service.SetGroupLeader(f.ctx["admin"], "overlap", ""); err != nil {
		t.Fatal(err)
	}
	if got := read(); got[1].LeaderUserID != "" {
		t.Fatalf("leader not cleared: %+v", got[1])
	}
	if err := f.service.SetGrant(f.ctx["admin"], "g", "db", Edit); err != nil {
		t.Fatal(err)
	}
	if got := read(); got[0].Permission != Edit {
		t.Fatalf("grant change not reflected: %+v", got[0])
	}
	if err := f.service.SetGroupActive(f.ctx["admin"], "g", false); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 1 || got[0].GroupID != "overlap" {
		t.Fatalf("inactive group included: %+v", got)
	}
	if err := pgx.BeginFunc(t.Context(), f.pool, func(tx pgx.Tx) error {
		groups, err := f.service.ApprovalPolicy(t.Context(), tx, "db2")
		if err != nil {
			return err
		}
		if len(groups) != 0 {
			t.Fatalf("expected no eligible groups, got %+v", groups)
		}
		_, err = f.service.ApprovalPolicy(t.Context(), tx, "missing")
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing database = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
