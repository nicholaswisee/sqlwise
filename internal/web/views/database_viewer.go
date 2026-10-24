package views

import (
	"net/url"
	"strconv"
)

type DatabaseListRow struct {
	ID, Name, Permission string
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
}

func databaseURL(databaseID string) string { return "/databases/" + databaseID }

func schemaURL(databaseID, schema string) string {
	return databaseURL(databaseID) + "?" + url.Values{"schema": {schema}}.Encode()
}

func TableURL(databaseID, tableID string, offset, limit int) string {
	return databaseURL(databaseID) + "/tables/" + tableID + "?" + url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}.Encode()
}
