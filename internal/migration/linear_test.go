package migration

import (
	"errors"
	"reflect"
	"testing"
)

func committed(events ...[3]string) []Attempt {
	out := make([]Attempt, 0, len(events))
	for i, e := range events {
		a := Attempt{ID: e[0], MigrationID: e[1], Operation: OpUp, Status: StatusSucceeded, AppliedVersion: int64(i + 1)}
		switch {
		case e[2] == "forward":
			a.Operation = OpForward
		case e[2] != "":
			a.Operation, a.CancelsExecutionID = OpRollback, e[2]
		}
		out = append(out, a)
	}
	return out
}

func ids(attempts []Attempt) []string {
	out := []string{}
	for _, a := range attempts {
		out = append(out, a.ID)
	}
	return out
}

func reversible(id string) Draft { return Draft{ID: id, SQLDown: "SELECT 1"} }

func TestHideRollbacksPairsByExecutionID(t *testing.T) {
	// E1=A, E2=B, E3=rollback(E2), E4=forward(B), E5=C: E4 shares B's name
	// with E2 but is not the cancelled execution, so it stays.
	history := committed([3]string{"E1", "A"}, [3]string{"E2", "B"}, [3]string{"E3", "B", "E2"}, [3]string{"E4", "B", "forward"}, [3]string{"E5", "C"})
	if got := ids(HideRollbacks(history)); !reflect.DeepEqual(got, []string{"E1", "E4", "E5"}) {
		t.Fatalf("hidden history = %v", got)
	}
	if len(history) != 5 {
		t.Fatal("filter changed the input")
	}
	// A failed rollback cancels nothing.
	failed := append(committed([3]string{"E1", "A"}), Attempt{ID: "E2", MigrationID: "A", Operation: OpRollback, CancelsExecutionID: "E1", Status: StatusFailed})
	if got := ids(HideRollbacks(failed)); !reflect.DeepEqual(got, []string{"E1", "E2"}) {
		t.Fatalf("failed rollback hid %v", got)
	}
	head := replay(committed([3]string{"E1", "A"}, [3]string{"E2", "B"}, [3]string{"E3", "B", "E2"}, [3]string{"E4", "B", "forward"}, [3]string{"E5", "C"}))
	if want := []HeadEntry{{"A", "E1"}, {"B", "E4"}, {"C", "E5"}}; !reflect.DeepEqual(head.Active, want) || len(head.Pending) != 0 {
		t.Fatalf("head = %+v", head)
	}
}

func TestLinearRollbackAndForwardOrder(t *testing.T) {
	a, b, c, d := reversible("A"), reversible("B"), reversible("C"), reversible("D")
	a.AppliedVersion, b.AppliedVersion, c.AppliedVersion = 1, 2, 3
	steps := []struct {
		events  [][3]string
		draft   Draft
		op      Operation
		cancels string // expected cancelled execution, or "!" for refusal
	}{
		{nil, d, OpUp, ""},
		{nil, a, OpRollback, "!"},
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}}, b, OpRollback, "!"}, // C first
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}}, c, OpRollback, "E3"},
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}}, d, OpUp, ""},
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}}, c, OpUp, "!"}, // applied once
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}}, c, OpForward, "!"},
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}, {"E4", "C", "E3"}}, b, OpRollback, "E2"},
		// After C then B rolled back, only B may move forward, and nothing new
		// is applied before them.
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}, {"E4", "C", "E3"}, {"E5", "B", "E2"}}, c, OpForward, "!"},
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}, {"E4", "C", "E3"}, {"E5", "B", "E2"}}, b, OpForward, ""},
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}, {"E4", "C", "E3"}, {"E5", "B", "E2"}}, d, OpUp, "!"},
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}, {"E4", "C", "E3"}, {"E5", "B", "E2"}, {"E6", "B", "forward"}}, c, OpForward, ""},
		{[][3]string{{"E1", "A"}, {"E2", "B"}, {"E3", "C"}, {"E4", "C", "E3"}, {"E5", "B", "E2"}, {"E6", "B", "forward"}}, b, OpRollback, "E6"},
		{[][3]string{{"E1", "A"}}, Draft{ID: "A", IrreversibleReason: "drops data"}, OpRollback, "!"},
		{[][3]string{{"E1", "A"}}, Draft{ID: "A"}, OpRollback, "!"}, // no SQL down
	}
	for i, step := range steps {
		cancels, err := eligible(replay(committed(step.events...)), step.draft, step.op)
		if step.cancels == "!" {
			if !errors.Is(err, ErrConflict) {
				t.Errorf("step %d: %s %s allowed, want conflict", i, step.op, step.draft.ID)
			}
			continue
		}
		if err != nil || cancels != step.cancels {
			t.Errorf("step %d: %s %s = %q, %v; want %q", i, step.op, step.draft.ID, cancels, err, step.cancels)
		}
	}
	if _, err := eligible(Head{}, a, "sideways"); !errors.Is(err, ErrValidation) {
		t.Fatalf("unknown operation = %v", err)
	}
}

func TestDefinitionInputAndHash(t *testing.T) {
	for _, in := range []DraftInput{
		{Title: "t", SQL: "SELECT 1", SQLDown: "SELECT 2", Irreversible: true, IrreversibleReason: "why"},
		{Title: "t", SQL: "SELECT 1", Irreversible: true, IrreversibleReason: "  "},
	} {
		if _, err := in.normalize(); !errors.Is(err, ErrValidation) {
			t.Errorf("normalize(%+v) = %v, want validation", in, err)
		}
	}
	in, err := DraftInput{Title: "t", SQL: "SELECT 1", SQLDown: "\r\n", IrreversibleReason: "ignored"}.normalize()
	if err != nil || in.SQLDown != "" || in.IrreversibleReason != "" {
		t.Fatalf("incomplete draft = %+v, %v", in, err)
	}
	base := Draft{SQL: "SELECT 1", SQLDown: "SELECT 2"}
	for _, changed := range []Draft{{SQL: "SELECT 1", SQLDown: "SELECT 3"}, {SQL: "SELECT 1", IrreversibleReason: "SELECT 2"}, {SQL: "SELECT 1SELECT 2"}} {
		if changed.DefinitionHash() == base.DefinitionHash() {
			t.Errorf("hash ignores change %+v", changed)
		}
	}
}
