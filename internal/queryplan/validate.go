// Package queryplan validates statements from the migration editor, asks the
// target for its execution plan and normalizes it for display. It never
// executes a migration or changes history.
package queryplan

import (
	"encoding/json"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"sqlwise/internal/database"
)

type Mode string

const (
	ModeExplain Mode = "explain"
	ModeAnalyze Mode = "analyze"
)

// Kind decides the permission a statement needs: reads need view access,
// writes (only planned, never analyzed) need edit access.
type Kind int

const (
	Read Kind = iota
	Write
)

func invalid(message string) error {
	return &database.Error{Kind: database.ErrUnsupportedStatement, Message: message}
}

// analyzeFunctions are built-ins without side effects that ANALYZE may run.
var analyzeFunctions = map[string]bool{
	"count": true, "sum": true, "avg": true, "min": true, "max": true, "coalesce": true, "nullif": true,
	"lower": true, "upper": true, "length": true, "char_length": true, "substring": true, "substr": true,
	"trim": true, "btrim": true, "ltrim": true, "rtrim": true, "replace": true, "concat": true, "concat_ws": true,
	"abs": true, "round": true, "floor": true, "ceil": true, "ceiling": true, "greatest": true, "least": true,
	"date_trunc": true, "date_part": true, "extract": true, "now": true, "to_char": true, "age": true,
	"string_agg": true, "array_agg": true, "bool_and": true, "bool_or": true, "row_number": true, "rank": true,
	"dense_rank": true, "lag": true, "lead": true, "first_value": true, "last_value": true,
}

// Validate parses sql with PostgreSQL's parser and accepts exactly one
// SELECT, INSERT, UPDATE or DELETE. ANALYZE additionally requires a plain
// SELECT without data-modifying CTEs, row locks or unlisted functions.
func Validate(sql string, mode Mode) (Kind, error) {
	if mode != ModeExplain && mode != ModeAnalyze {
		return Read, &database.Error{Kind: database.ErrValidation, Message: "Choose Explain or Analyze."}
	}
	if strings.TrimSpace(sql) == "" {
		return Read, &database.Error{Kind: database.ErrValidation, Message: "Enter a statement to explain."}
	}
	tree, err := pg_query.Parse(sql)
	if err != nil {
		return Read, &database.Error{Kind: database.ErrValidation, Message: "The statement could not be parsed: " + err.Error() + "."}
	}
	if len(tree.Stmts) != 1 {
		return Read, invalid("Explain one statement at a time.")
	}
	node := tree.Stmts[0].Stmt
	kind := Read
	switch {
	case node.GetSelectStmt() != nil:
		s := node.GetSelectStmt()
		if s.IntoClause != nil {
			return Read, invalid("SELECT INTO creates a table and cannot be explained here.")
		}
		if writesInCTE(s.WithClause) {
			kind = Write
		}
	case node.GetInsertStmt() != nil, node.GetUpdateStmt() != nil, node.GetDeleteStmt() != nil:
		kind = Write
	default:
		return Read, invalid("Only SELECT, INSERT, UPDATE and DELETE statements can be explained; DDL, transaction and utility commands are not supported.")
	}
	if mode == ModeAnalyze {
		if kind == Write {
			return kind, invalid("ANALYZE executes the statement, so it is limited to SELECT without data-modifying CTEs.")
		}
		if s := node.GetSelectStmt(); len(s.LockingClause) > 0 {
			return kind, invalid("ANALYZE runs read-only; remove FOR UPDATE/FOR SHARE.")
		}
		if name := disallowedFunction(sql); name != "" {
			return kind, invalid("ANALYZE does not run the function " + name + "; only common built-in functions are allowed.")
		}
	}
	return kind, nil
}

func writesInCTE(with *pg_query.WithClause) bool {
	if with == nil {
		return false
	}
	for _, cte := range with.Ctes {
		q := cte.GetCommonTableExpr().GetCtequery()
		if q.GetInsertStmt() != nil || q.GetUpdateStmt() != nil || q.GetDeleteStmt() != nil || q.GetMergeStmt() != nil {
			return true
		}
	}
	return false
}

// disallowedFunction returns the first called function outside
// analyzeFunctions, including any schema-qualified call.
func disallowedFunction(sql string) string {
	raw, err := pg_query.ParseToJSON(sql)
	if err != nil {
		return "unknown"
	}
	var tree any
	if err := json.Unmarshal([]byte(raw), &tree); err != nil {
		return "unknown"
	}
	var found string
	var walk func(any)
	walk = func(v any) {
		if found != "" {
			return
		}
		switch v := v.(type) {
		case map[string]any:
			if call, ok := v["FuncCall"].(map[string]any); ok {
				var parts []string
				for _, part := range asList(call["funcname"]) {
					if s, ok := part.(map[string]any)["String"].(map[string]any); ok {
						parts = append(parts, strings.ToLower(s["sval"].(string)))
					}
				}
				name := strings.Join(parts, ".")
				if len(parts) == 2 && parts[0] == "pg_catalog" {
					name = parts[1]
				}
				if !analyzeFunctions[name] {
					found = name
					return
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(tree)
	return found
}

func asList(v any) []any {
	list, _ := v.([]any)
	return list
}
