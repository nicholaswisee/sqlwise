package views

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

type DatabaseListRow struct {
	ID, Name, Permission string
	// Engine is the DBMS label, such as PostgreSQL or MySQL; empty when unknown.
	Engine string
}

type DatabaseListData struct {
	Username  string
	Databases []DatabaseListRow
}

type SchemaColumn struct {
	Name, Type, Default  string
	Nullable, HasDefault bool
	PrimaryKey           bool
}

type SchemaDefinition struct {
	Name, Kind, Definition string
}

type SchemaTable struct {
	ID, Name    string
	Columns     []SchemaColumn
	Constraints []SchemaDefinition
	Indexes     []SchemaDefinition
}

type SchemaExplorerData struct {
	Nav     DatabaseNav
	Schemas []string
	Schema  string
	Tables  []SchemaTable
	// Selected indexes Tables; the detail pane shows that table.
	Selected int
}

// selected returns the table shown in the detail pane, if any.
func (d SchemaExplorerData) selected() (SchemaTable, bool) {
	if len(d.Tables) == 0 {
		return SchemaTable{}, false
	}
	if d.Selected < 0 || d.Selected >= len(d.Tables) {
		return d.Tables[0], true
	}
	return d.Tables[d.Selected], true
}

type TableColumn struct {
	Name, Type string
}

type TableCell struct {
	Text                    string
	Null, Binary, Truncated bool
}

type TablePageData struct {
	Nav                DatabaseNav
	Schema, Table      string
	Columns            []TableColumn
	Rows               [][]TableCell
	From, To           int
	PrevURL, NextURL   string
	Ordered, Truncated bool
	// Limit is the current page size; BaseURL is the table URL without a query.
	Limit   int
	BaseURL string
}

func databaseURL(databaseID string) string { return "/databases/" + databaseID }

func schemaURL(databaseID, schema string) string {
	return databaseURL(databaseID) + "?" + url.Values{"schema": {schema}}.Encode()
}

func schemaTableURL(databaseID, schema, table string) string {
	return databaseURL(databaseID) + "?" + url.Values{"schema": {schema}, "table": {table}}.Encode()
}

// TableBaseURL is the data page of a table without paging parameters.
func TableBaseURL(databaseID, tableID string) string {
	return databaseURL(databaseID) + "/tables/" + tableID
}

func TableURL(databaseID, tableID string, offset, limit int) string {
	return TableBaseURL(databaseID, tableID) + "?" + url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}.Encode()
}

func countLabel(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func primaryKeySummary(columns []SchemaColumn) string {
	var keys []string
	for _, c := range columns {
		if c.PrimaryKey {
			keys = append(keys, c.Name)
		}
	}
	if len(keys) == 0 {
		return "No primary key"
	}
	return "Primary key on " + strings.Join(keys, ", ")
}

// pageSizes lists the rows-per-page choices, keeping a custom current size.
func pageSizes(current int) []int {
	sizes := []int{25, 50, 100, 200}
	for i, s := range sizes {
		if s == current {
			return sizes
		}
		if current > 0 && current < s {
			return append(append(append([]int{}, sizes[:i]...), current), sizes[i:]...)
		}
	}
	if current > 0 {
		return append(sizes, current)
	}
	return sizes
}

// railItemClass styles a table link in the explorer rail.
func railItemClass(selected bool) string {
	base := "flex min-h-11 items-center justify-between gap-3 rounded-lg px-3 text-sm no-underline " + wsFocus
	if selected {
		return base + " bg-white font-semibold text-cyan-900 ring-1 ring-slate-200"
	}
	return base + " text-slate-800 hover:bg-slate-200/70 hover:text-slate-900"
}

func longCell(cell TableCell) bool {
	return !cell.Null && (utf8.RuneCountInString(cell.Text) > 48 || strings.ContainsRune(cell.Text, '\n'))
}

// cellClass keeps short values on one line and gives long text room to wrap.
func cellClass(cell TableCell) string {
	base := "px-3 py-2 align-top font-mono text-[13px] leading-5 text-slate-900"
	if longCell(cell) {
		return base + " min-w-64 max-w-md"
	}
	return base + " whitespace-nowrap"
}

// cellTextClass preserves the value's own spacing and line breaks.
func cellTextClass(cell TableCell) string {
	switch {
	case !longCell(cell):
		return "whitespace-pre"
	case cell.Binary:
		return "break-all"
	}
	return "whitespace-pre-wrap break-words"
}
