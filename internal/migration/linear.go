package migration

import (
	"fmt"
)

// Head is the active linear position derived from committed history.
// Active is the applied stack, oldest first; Pending lists rolled-back
// migrations in their original order, which only forward may re-apply.
type Head struct {
	Active  []HeadEntry
	Pending []HeadEntry
}

// HeadEntry is a migration and the successful execution that applied it
// (for Pending: the execution that the rollback cancelled).
type HeadEntry struct {
	MigrationID string
	ExecutionID string
}

// Top returns the last active entry, if any.
func (h Head) Top() (HeadEntry, bool) {
	if len(h.Active) == 0 {
		return HeadEntry{}, false
	}
	return h.Active[len(h.Active)-1], true
}

// IsActive reports whether a migration is currently applied.
func (h Head) IsActive(migrationID string) bool {
	for _, e := range h.Active {
		if e.MigrationID == migrationID {
			return true
		}
	}
	return false
}

// replay folds committed executions, ordered by version, into the head.
func replay(committed []Attempt) Head {
	var h Head
	for _, a := range committed {
		switch a.Operation {
		case OpRollback:
			if top, ok := h.Top(); ok && top.ExecutionID == a.CancelsExecutionID {
				h.Active = h.Active[:len(h.Active)-1]
				h.Pending = append([]HeadEntry{top}, h.Pending...)
			}
		case OpForward:
			if len(h.Pending) > 0 && h.Pending[0].MigrationID == a.MigrationID {
				h.Pending = h.Pending[1:]
			}
			h.Active = append(h.Active, HeadEntry{a.MigrationID, a.ID})
		default:
			h.Active = append(h.Active, HeadEntry{a.MigrationID, a.ID})
		}
	}
	return h
}

// eligible checks the strict linear rules for running op on d at head h and
// returns the execution a rollback cancels. Up never runs while rolled-back
// migrations wait for forward, so history cannot branch.
func eligible(h Head, d Draft, op Operation) (string, error) {
	switch op {
	case OpUp:
		if d.Applied() {
			return "", fail(ErrConflict, fmt.Sprintf("This migration was already applied as version %d; use rollback or forward instead.", d.AppliedVersion))
		}
		if len(h.Pending) > 0 {
			return "", fail(ErrConflict, "A rolled-back migration must be forwarded before a new migration is applied.")
		}
		return "", nil
	case OpRollback:
		top, ok := h.Top()
		if !ok || top.MigrationID != d.ID {
			return "", fail(ErrConflict, "Only the last active migration can be rolled back.")
		}
		if d.Irreversible() {
			return "", fail(ErrConflict, "This migration is irreversible: "+d.IrreversibleReason)
		}
		if d.SQLDown == "" {
			return "", fail(ErrConflict, "This migration has no SQL down, so it cannot be rolled back.")
		}
		return top.ExecutionID, nil
	case OpForward:
		if len(h.Pending) == 0 || h.Pending[0].MigrationID != d.ID {
			return "", fail(ErrConflict, "Only the next rolled-back migration can be forwarded.")
		}
		return "", nil
	}
	return "", fail(ErrValidation, "Operation must be up, rollback or forward.")
}

// Eligible lists the operations allowed for d at head h.
func (h Head) Eligible(d Draft) []Operation {
	ops := []Operation{}
	for _, op := range []Operation{OpUp, OpRollback, OpForward} {
		if _, err := eligible(h, d, op); err == nil {
			ops = append(ops, op)
		}
	}
	return ops
}

// HideRollbacks drops each successful rollback and the execution it cancels,
// matched by execution ID. It filters a copy; nothing is deleted.
func HideRollbacks(committed []Attempt) []Attempt {
	hidden := map[string]bool{}
	for _, a := range committed {
		if a.Operation == OpRollback && a.Status == StatusSucceeded {
			hidden[a.ID], hidden[a.CancelsExecutionID] = true, true
		}
	}
	out := make([]Attempt, 0, len(committed))
	for _, a := range committed {
		if !hidden[a.ID] {
			out = append(out, a)
		}
	}
	return out
}
