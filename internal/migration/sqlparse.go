package migration

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"github.com/pganalyze/pg_query_go/v6/parser"
)

const (
	MaxSQLBytes   = 256 << 10
	MaxStatements = 200
	// ReceiptSchema is reserved for execution receipts on every target.
	ReceiptSchema = "sqlwise_internal"
)

// Statement is one top-level PostgreSQL statement without its terminating semicolon.
type Statement struct {
	SQL  string
	node *pg_query.Node
}

// SQLError identifies the rejected statement (1-based, 0 for the whole script).
type SQLError struct {
	Statement int
	Reason    string
}

func (e *SQLError) Error() string {
	if e.Statement == 0 {
		return e.Reason
	}
	return fmt.Sprintf("statement %d: %s", e.Statement, e.Reason)
}

// SplitPostgres parses a script with PostgreSQL's own parser (libpg_query) and
// returns its top-level statements. Syntax errors are reported before anything
// reaches a target.
func SplitPostgres(sql string) ([]Statement, error) {
	if !utf8.ValidString(sql) || strings.ContainsRune(sql, 0) {
		return nil, &SQLError{Reason: "SQL must be valid UTF-8 text without NUL bytes"}
	}
	if len(sql) > MaxSQLBytes {
		return nil, &SQLError{Reason: fmt.Sprintf("SQL exceeds %d KiB", MaxSQLBytes>>10)}
	}
	tree, err := pg_query.Parse(sql)
	if err != nil {
		return nil, syntaxError(sql, err)
	}
	if len(tree.Stmts) == 0 {
		return nil, &SQLError{Reason: "SQL contains no statements"}
	}
	if len(tree.Stmts) > MaxStatements {
		return nil, &SQLError{Reason: fmt.Sprintf("SQL contains more than %d statements", MaxStatements)}
	}
	statements := make([]Statement, len(tree.Stmts))
	bounds := make([][2]int, len(tree.Stmts))
	for i, raw := range tree.Stmts {
		start, end := int(raw.StmtLocation), len(sql)
		if raw.StmtLen > 0 {
			end = start + int(raw.StmtLen)
		}
		bounds[i] = [2]int{start, end}
		statements[i] = Statement{SQL: strings.TrimSpace(sql[start:end]), node: raw.Stmt}
	}
	if n, err := reservedStatement(sql, bounds); err != nil || n > 0 {
		if err != nil {
			return nil, syntaxError(sql, err)
		}
		return nil, &SQLError{Statement: n, Reason: "the " + ReceiptSchema + " schema is reserved for execution receipts"}
	}
	return statements, nil
}

// reservedStatement returns the 1-based statement whose identifiers, strings or
// dollar-quoted bodies mention the receipt schema, or 0. Comments are ignored;
// bodies are checked as text because dynamic SQL is invisible to the parser.
func reservedStatement(sql string, bounds [][2]int) (int, error) {
	scan, err := pg_query.Scan(sql)
	if err != nil {
		return 0, err
	}
	for _, token := range scan.Tokens {
		if token.Token == pg_query.Token_SQL_COMMENT || token.Token == pg_query.Token_C_COMMENT {
			continue
		}
		text := strings.ReplaceAll(sql[token.Start:token.End], `""`, `"`)
		if !strings.Contains(strings.ToLower(text), ReceiptSchema) {
			continue
		}
		for i, b := range bounds {
			if int(token.Start) >= b[0] && int(token.Start) < b[1] {
				return i + 1, nil
			}
		}
		return 1, nil
	}
	return 0, nil
}

// syntaxError reports a parser error with its line; psql meta-commands get a
// dedicated message since they are client commands, not SQL.
func syntaxError(sql string, err error) error {
	var pgErr *parser.Error
	if !errors.As(err, &pgErr) {
		return &SQLError{Reason: "SQL could not be parsed"}
	}
	line, text := 1, sql
	if runes := []rune(sql); pgErr.Cursorpos > 0 && pgErr.Cursorpos <= len(runes) {
		before := string(runes[:pgErr.Cursorpos-1])
		line = strings.Count(before, "\n") + 1
		text = sql[strings.LastIndex(before, "\n")+1:]
	}
	if strings.HasPrefix(strings.TrimSpace(text), `\`) {
		return &SQLError{Reason: fmt.Sprintf("line %d: psql meta-commands are not SQL and cannot be executed", line)}
	}
	return &SQLError{Reason: fmt.Sprintf("line %d: %s", line, pgErr.Message)}
}

// ValidatePostgres rejects statements that cannot run inside the single
// migration transaction or would leak state onto a pooled target connection.
func ValidatePostgres(statements []Statement) error {
	for i, statement := range statements {
		if reason := rejectPostgres(statement.node); reason != "" {
			return &SQLError{Statement: i + 1, Reason: reason}
		}
	}
	return nil
}

// ParsePostgres splits and validates a PostgreSQL migration script.
func ParsePostgres(sql string) ([]Statement, error) {
	statements, err := SplitPostgres(sql)
	if err != nil {
		return nil, err
	}
	if err := ValidatePostgres(statements); err != nil {
		return nil, err
	}
	return statements, nil
}

func rejectPostgres(n *pg_query.Node) string {
	const transaction = "transaction control is managed by sqlwise; remove BEGIN/COMMIT/ROLLBACK/SAVEPOINT statements"
	const outside = " cannot run inside the migration transaction"
	switch {
	case n.GetTransactionStmt() != nil:
		return transaction
	case n.GetPrepareStmt() != nil:
		return "PREPARE creates session state on the target connection and is not supported"
	case n.GetVariableSetStmt() != nil:
		s := n.GetVariableSetStmt()
		switch {
		case s.Kind == pg_query.VariableSetKind_VAR_SET_MULTI:
			return transaction
		case s.Name == "role" || s.Name == "session_authorization":
			return "changing the executing role is not supported"
		case s.Kind == pg_query.VariableSetKind_VAR_RESET || s.Kind == pg_query.VariableSetKind_VAR_RESET_ALL:
			return "RESET changes target session state and is not supported"
		case !s.IsLocal:
			return "session settings persist on the target connection; use SET LOCAL instead"
		}
	case n.GetDiscardStmt() != nil:
		return "DISCARD changes target session state and is not supported"
	case n.GetVacuumStmt() != nil && n.GetVacuumStmt().IsVacuumcmd:
		return "VACUUM" + outside
	case n.GetCheckPointStmt() != nil:
		return "CHECKPOINT" + outside
	case n.GetLoadStmt() != nil:
		return "LOAD" + outside
	case n.GetCopyStmt() != nil:
		return "COPY is not supported in migrations; use INSERT statements"
	case n.GetClusterStmt() != nil && n.GetClusterStmt().Relation == nil:
		return "CLUSTER without a table" + outside
	case n.GetReindexStmt() != nil:
		s := n.GetReindexStmt()
		if s.Kind == pg_query.ReindexObjectType_REINDEX_OBJECT_DATABASE || s.Kind == pg_query.ReindexObjectType_REINDEX_OBJECT_SYSTEM {
			return "REINDEX DATABASE/SYSTEM" + outside
		}
		if hasOption(s.Params, "concurrently") {
			return "CONCURRENTLY operations" + outside
		}
	case n.GetAlterSystemStmt() != nil:
		return "ALTER SYSTEM" + outside
	case n.GetAlterDatabaseStmt() != nil && hasOption(n.GetAlterDatabaseStmt().Options, "tablespace"):
		return "ALTER DATABASE SET TABLESPACE" + outside
	case n.GetCreatedbStmt() != nil, n.GetDropdbStmt() != nil:
		return "CREATE/DROP DATABASE" + outside
	case n.GetCreateTableSpaceStmt() != nil, n.GetDropTableSpaceStmt() != nil:
		return "CREATE/DROP TABLESPACE" + outside
	case n.GetCreateSubscriptionStmt() != nil, n.GetAlterSubscriptionStmt() != nil, n.GetDropSubscriptionStmt() != nil:
		return "subscription changes are not supported in migrations"
	case n.GetIndexStmt() != nil && n.GetIndexStmt().Concurrent,
		n.GetDropStmt() != nil && n.GetDropStmt().Concurrent:
		return "CONCURRENTLY operations" + outside
	case n.GetAlterTableStmt() != nil:
		for _, cmd := range n.GetAlterTableStmt().Cmds {
			c := cmd.GetAlterTableCmd()
			if c != nil && c.Subtype == pg_query.AlterTableType_AT_DetachPartition && c.Def.GetPartitionCmd().GetConcurrent() {
				return "CONCURRENTLY operations" + outside
			}
		}
	}
	return ""
}

func hasOption(options []*pg_query.Node, name string) bool {
	for _, option := range options {
		if option.GetDefElem().GetDefname() == name {
			return true
		}
	}
	return false
}
