package schemadiff

import (
	"fmt"
	"slices"
	"strings"
)

type ChangeKind string

const (
	AddTable         ChangeKind = "add_table"
	RemoveTable      ChangeKind = "remove_table"
	AddColumn        ChangeKind = "add_column"
	RemoveColumn     ChangeKind = "remove_column"
	AlterType        ChangeKind = "alter_type"
	AlterNullability ChangeKind = "alter_nullability"
	AlterDefault     ChangeKind = "alter_default"
	AlterPrimaryKey  ChangeKind = "alter_primary_key"
	AddIndex         ChangeKind = "add_index"
	RemoveIndex      ChangeKind = "remove_index"
	AlterIndex       ChangeKind = "alter_index"
	AddConstraint    ChangeKind = "add_constraint"
	RemoveConstraint ChangeKind = "remove_constraint"
	AlterConstraint  ChangeKind = "alter_constraint"
)

// Change is one detected difference; From/To describe the current and desired
// values. Destructive changes can lose data if applied.
type Change struct {
	Kind        ChangeKind
	Table       string
	Column      string
	Object      string
	From        string
	To          string
	Destructive bool
}

func (c Change) String() string {
	subject := QuoteIdent(c.Table)
	if c.Column != "" {
		subject += "." + QuoteIdent(c.Column)
	}
	if c.Object != "" {
		subject += " " + QuoteIdent(c.Object)
	}
	switch c.Kind {
	case AddTable:
		return "Add table " + subject
	case RemoveTable:
		return "Remove table " + subject
	case AddColumn:
		return fmt.Sprintf("Add column %s %s", subject, c.To)
	case RemoveColumn:
		return "Remove column " + subject
	case AlterType:
		return fmt.Sprintf("Change type of %s from %s to %s", subject, c.From, c.To)
	case AlterNullability:
		return fmt.Sprintf("Change %s from %s to %s", subject, c.From, c.To)
	case AlterDefault:
		return fmt.Sprintf("Change default of %s from %s to %s", subject, c.From, c.To)
	case AlterPrimaryKey:
		return fmt.Sprintf("Change primary key of %s from (%s) to (%s)", QuoteIdent(c.Table), c.From, c.To)
	case AddIndex, AddConstraint:
		return fmt.Sprintf("Add %s on %s", objectNoun(c.Kind)+" "+QuoteIdent(c.Object), QuoteIdent(c.Table))
	case RemoveIndex, RemoveConstraint:
		return fmt.Sprintf("Remove %s from %s", objectNoun(c.Kind)+" "+QuoteIdent(c.Object), QuoteIdent(c.Table))
	}
	return fmt.Sprintf("Change %s on %s", objectNoun(c.Kind)+" "+QuoteIdent(c.Object), QuoteIdent(c.Table))
}

func objectNoun(kind ChangeKind) string {
	if strings.HasSuffix(string(kind), "index") {
		return "index"
	}
	return "constraint"
}

// Compare lists changes from current to desired in a deterministic order:
// tables by name, then table-level, column, key, index and constraint changes.
// Renames are never inferred; they appear as a removal plus an addition.
func Compare(current, desired Snapshot) []Change {
	from, to := byName(current.normalize()), desired.normalize()
	var changes []Change
	names := make([]string, 0, len(from)+len(to))
	for name := range from {
		names = append(names, name)
	}
	desiredByName := byName(to)
	for name := range desiredByName {
		if _, ok := from[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		was, hadTable := from[name]
		want, wantTable := desiredByName[name]
		switch {
		case !wantTable:
			changes = append(changes, Change{Kind: RemoveTable, Table: name, Destructive: true})
		case !hadTable:
			changes = append(changes, Change{Kind: AddTable, Table: name})
		default:
			changes = append(changes, compareTable(was, want)...)
		}
	}
	return changes
}

func byName(tables []normalTable) map[string]normalTable {
	out := make(map[string]normalTable, len(tables))
	for _, t := range tables {
		out[t.Name] = t
	}
	return out
}

func compareTable(was, want normalTable) []Change {
	var changes []Change
	current := make(map[string]normalColumn, len(was.Columns))
	for _, c := range was.Columns {
		current[c.Name] = c
	}
	desired := make(map[string]bool, len(want.Columns))
	for _, c := range want.Columns {
		desired[c.Name] = true
		old, ok := current[c.Name]
		if !ok {
			changes = append(changes, Change{Kind: AddColumn, Table: want.Name, Column: c.Name, To: columnSummary(c)})
			continue
		}
		if old.Type != c.Type {
			changes = append(changes, Change{Kind: AlterType, Table: want.Name, Column: c.Name, From: old.Type, To: c.Type, Destructive: true})
		}
		if old.Nullable != c.Nullable {
			changes = append(changes, Change{Kind: AlterNullability, Table: want.Name, Column: c.Name,
				From: nullability(old.Nullable), To: nullability(c.Nullable), Destructive: !c.Nullable})
		}
		if defaultText(old.Default) != defaultText(c.Default) {
			changes = append(changes, Change{Kind: AlterDefault, Table: want.Name, Column: c.Name, From: defaultText(old.Default), To: defaultText(c.Default)})
		}
	}
	for _, c := range was.Columns {
		if !desired[c.Name] {
			changes = append(changes, Change{Kind: RemoveColumn, Table: want.Name, Column: c.Name, From: columnSummary(c), Destructive: true})
		}
	}
	if !slices.Equal(was.PrimaryKey, want.PrimaryKey) {
		changes = append(changes, Change{Kind: AlterPrimaryKey, Table: want.Name, From: strings.Join(quoteAll(was.PrimaryKey), ", "),
			To: strings.Join(quoteAll(want.PrimaryKey), ", "), Destructive: len(was.PrimaryKey) > 0})
	}
	changes = append(changes, compareDefinitions(want.Name, was.Indexes, want.Indexes, AddIndex, RemoveIndex, AlterIndex)...)
	changes = append(changes, compareDefinitions(want.Name, was.Constraints, want.Constraints, AddConstraint, RemoveConstraint, AlterConstraint)...)
	return changes
}

func compareDefinitions(table string, was, want []Definition, add, remove, alter ChangeKind) []Change {
	var changes []Change
	current := make(map[string]string, len(was))
	for _, d := range was {
		current[d.Name] = d.Definition
	}
	desired := make(map[string]bool, len(want))
	for _, d := range want {
		desired[d.Name] = true
		old, ok := current[d.Name]
		switch {
		case !ok:
			changes = append(changes, Change{Kind: add, Table: table, Object: d.Name, To: d.Definition})
		case old != d.Definition:
			changes = append(changes, Change{Kind: alter, Table: table, Object: d.Name, From: old, To: d.Definition, Destructive: true})
		}
	}
	for _, d := range was {
		if !desired[d.Name] {
			changes = append(changes, Change{Kind: remove, Table: table, Object: d.Name, From: d.Definition, Destructive: true})
		}
	}
	return changes
}

func columnSummary(c normalColumn) string {
	summary := c.Type
	if !c.Nullable {
		summary += " NOT NULL"
	}
	if c.Default != nil {
		summary += " DEFAULT " + *c.Default
	}
	return summary
}

func nullability(nullable bool) string {
	if nullable {
		return "NULL"
	}
	return "NOT NULL"
}

func defaultText(d *string) string {
	if d == nil {
		return "no default"
	}
	return *d
}

func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = QuoteIdent(n)
	}
	return out
}
