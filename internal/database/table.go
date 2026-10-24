package database

import (
	"encoding/hex"
	"strconv"
	"unicode/utf8"
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 200
	MaxCellBytes    = 4 << 10
	MaxPageBytes    = 1 << 20
)

type TableRequest struct {
	Table  TableRef
	Offset int
	Limit  int
}

func ParsePage(offset, limit string) (int, int, error) {
	o, l := 0, DefaultPageSize
	var err error
	if offset != "" {
		if o, err = strconv.Atoi(offset); err != nil || o < 0 {
			return 0, 0, fail(ErrValidation, "Offset must be a whole number from 0.")
		}
	}
	if limit != "" {
		if l, err = strconv.Atoi(limit); err != nil || l < 1 || l > MaxPageSize {
			return 0, 0, fail(ErrValidation, "Rows per page must be from 1 to "+strconv.Itoa(MaxPageSize)+".")
		}
	}
	return o, l, nil
}

type TablePage struct {
	Table     TableRef
	Columns   []ResultColumn
	Rows      [][]Cell
	Offset    int
	Limit     int
	HasNext   bool
	Ordered   bool
	Truncated bool
}

type ResultColumn struct {
	Name string
	Type string
}

type Cell struct {
	Null      bool
	Binary    bool
	Truncated bool
	Text      string
}

func NullCell() Cell { return Cell{Null: true} }

func TextCell(s string, max int) Cell {
	if len(s) <= max {
		return Cell{Text: s}
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return Cell{Text: s[:cut], Truncated: true}
}

func BinaryCell(b []byte, max int) Cell {
	n := max/2 - 1
	truncated := len(b) > n
	if truncated {
		b = b[:n]
	}
	return Cell{Binary: true, Text: `\x` + hex.EncodeToString(b), Truncated: truncated}
}

func scanTargets(binary []bool) []any {
	values := make([]any, len(binary))
	for i, b := range binary {
		if b {
			values[i] = new([]byte)
		} else {
			values[i] = new(*string)
		}
	}
	return values
}

func cells(values []any) []Cell {
	row := make([]Cell, len(values))
	for i, v := range values {
		switch v := v.(type) {
		case *[]byte:
			if *v == nil {
				row[i] = NullCell()
			} else {
				row[i] = BinaryCell(*v, MaxCellBytes)
			}
		case **string:
			if *v == nil {
				row[i] = NullCell()
			} else {
				row[i] = TextCell(**v, MaxCellBytes)
			}
		}
	}
	return row
}

func addRow(page *TablePage, size *int, row []Cell) bool {
	rowSize := 0
	for _, c := range row {
		rowSize += len(c.Text)
	}
	if *size+rowSize > MaxPageBytes && len(page.Rows) > 0 {
		page.HasNext, page.Truncated = true, true
		return false
	}
	*size += rowSize
	page.Rows = append(page.Rows, row)
	return true
}
