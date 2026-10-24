package migration

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// SplitMySQL splits a script into top-level statements at semicolons outside
// quoted text, quoted identifiers and comments. MySQL checks the syntax of
// each statement when it runs.
func SplitMySQL(sql string) ([]Statement, error) {
	if !utf8.ValidString(sql) || strings.ContainsRune(sql, 0) {
		return nil, &SQLError{Reason: "SQL must be valid UTF-8 text without NUL bytes"}
	}
	if len(sql) > MaxSQLBytes {
		return nil, &SQLError{Reason: fmt.Sprintf("SQL exceeds %d KiB", MaxSQLBytes>>10)}
	}
	var statements []Statement
	add := func(text string) {
		if firstWords(text, 1) != "" {
			statements = append(statements, Statement{SQL: strings.TrimSpace(text)})
		}
	}
	start := 0
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			end := closingQuote(sql, i)
			if end < 0 {
				return nil, &SQLError{Statement: len(statements) + 1, Reason: "unterminated quoted text"}
			}
			i = end + 1
		case lineComment(sql, i):
			if end := strings.IndexByte(sql[i:], '\n'); end >= 0 {
				i += end + 1
			} else {
				i = len(sql)
			}
		case strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return nil, &SQLError{Statement: len(statements) + 1, Reason: "unterminated comment"}
			}
			i += end + 4
		case c == ';':
			add(sql[start:i])
			start = i + 1
			i++
		default:
			i++
		}
	}
	add(sql[start:])
	if len(statements) == 0 {
		return nil, &SQLError{Reason: "SQL contains no statements"}
	}
	if len(statements) > MaxStatements {
		return nil, &SQLError{Reason: fmt.Sprintf("SQL contains more than %d statements", MaxStatements)}
	}
	return statements, nil
}

func closingQuote(sql string, open int) int {
	quote := sql[open]
	for i := open + 1; i < len(sql); i++ {
		switch {
		case sql[i] == '\\' && quote != '`':
			i++
		case sql[i] == quote && i+1 < len(sql) && sql[i+1] == quote:
			i++
		case sql[i] == quote:
			return i
		}
	}
	return -1
}

func lineComment(sql string, i int) bool {
	if sql[i] == '#' {
		return true
	}
	return strings.HasPrefix(sql[i:], "--") && (i+2 == len(sql) || strings.ContainsRune(" \t\r\n", rune(sql[i+2])))
}

// firstWords returns up to n upper-cased leading words of a statement,
// skipping whitespace and comments.
func firstWords(text string, n int) string {
	var words []string
	for i := 0; i < len(text) && len(words) < n; {
		switch c := text[i]; {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case lineComment(text, i):
			if end := strings.IndexByte(text[i:], '\n'); end >= 0 {
				i += end + 1
			} else {
				i = len(text)
			}
		case strings.HasPrefix(text[i:], "/*") && !strings.HasPrefix(text[i:], "/*!"):
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				return strings.Join(words, " ")
			}
			i += end + 4
		default:
			j := i
			for j < len(text) && (text[j] == '_' || text[j] >= 'a' && text[j] <= 'z' || text[j] >= 'A' && text[j] <= 'Z') {
				j++
			}
			if j == i {
				words = append(words, string(c))
				i++
				continue
			}
			words = append(words, strings.ToUpper(text[i:j]))
			i = j
		}
	}
	return strings.Join(words, " ")
}

// ValidateMySQL rejects statements that manage transactions or change the
// session of the pinned target connection.
func ValidateMySQL(statements []Statement) error {
	for i, s := range statements {
		if reason := rejectMySQL(s.SQL); reason != "" {
			return &SQLError{Statement: i + 1, Reason: reason}
		}
	}
	return nil
}

func rejectMySQL(sql string) string {
	const transaction = "transaction control is not supported; MySQL commits each statement on its own"
	words := firstWords(sql, 2)
	first, _, _ := strings.Cut(words, " ")
	switch first {
	case "DELIMITER":
		return "DELIMITER is a client command; stored routines with multi-statement bodies are not supported"
	case "START", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "XA":
		return transaction
	case "BEGIN":
		if words == "BEGIN" || words == "BEGIN WORK" {
			return transaction
		}
	case "SET":
		return "SET changes the target session and is not supported"
	case "USE":
		return "USE changes the target database and is not supported"
	case "LOCK", "UNLOCK":
		return "table locks are not supported in migrations"
	case "PREPARE", "EXECUTE", "DEALLOCATE":
		return "prepared statements create session state on the target connection and are not supported"
	case "LOAD":
		return "LOAD DATA is not supported in migrations; use INSERT statements"
	}
	return ""
}

func ParseMySQL(sql string) ([]Statement, error) {
	statements, err := SplitMySQL(sql)
	if err != nil {
		return nil, err
	}
	if err := ValidateMySQL(statements); err != nil {
		return nil, err
	}
	return statements, nil
}
