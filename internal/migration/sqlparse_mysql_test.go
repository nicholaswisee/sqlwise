package migration

import (
	"errors"
	"testing"
)

func TestSplitMySQLRespectsQuotesAndComments(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		want []string
	}{
		{"plain", "CREATE TABLE a (id INT);\nINSERT INTO a VALUES (1)", []string{"CREATE TABLE a (id INT)", "INSERT INTO a VALUES (1)"}},
		{"semicolon in string", "INSERT INTO a VALUES ('x;y'); SELECT 1;", []string{"INSERT INTO a VALUES ('x;y')", "SELECT 1"}},
		{"doubled quote", "INSERT INTO a VALUES ('it''s; fine')", []string{"INSERT INTO a VALUES ('it''s; fine')"}},
		{"backslash escape", `INSERT INTO a VALUES ('a\'; b')`, []string{`INSERT INTO a VALUES ('a\'; b')`}},
		{"double quoted", `INSERT INTO a VALUES ("x;y")`, []string{`INSERT INTO a VALUES ("x;y")`}},
		{"backtick", "CREATE TABLE `we;ird``x` (id INT); SELECT 2", []string{"CREATE TABLE `we;ird``x` (id INT)", "SELECT 2"}},
		{"line comments", "-- note; here\nSELECT 1; # trailing; comment\nSELECT 2", []string{"-- note; here\nSELECT 1", "# trailing; comment\nSELECT 2"}},
		{"block comment", "/* a; b */ SELECT 1; SELECT 2", []string{"/* a; b */ SELECT 1", "SELECT 2"}},
		{"empty statements", ";;SELECT 1;; -- only a comment\n;", []string{"SELECT 1"}},
		{"dash without space", "SELECT 3--2; SELECT 4", []string{"SELECT 3--2", "SELECT 4"}},
	} {
		got, err := SplitMySQL(tc.sql)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%s: %d statements, want %d: %+v", tc.name, len(got), len(tc.want), got)
		}
		for i := range got {
			if got[i].SQL != tc.want[i] {
				t.Fatalf("%s statement %d = %q, want %q", tc.name, i+1, got[i].SQL, tc.want[i])
			}
		}
	}
}

func TestSplitMySQLRejectsMalformedScripts(t *testing.T) {
	for name, sql := range map[string]string{
		"empty":                 "  -- nothing\n",
		"unterminated string":   "SELECT 'abc",
		"unterminated backtick": "SELECT `abc",
		"unterminated comment":  "SELECT 1 /* open",
		"nul":                   "SELECT 1\x00",
	} {
		var sqlErr *SQLError
		if _, err := SplitMySQL(sql); !errors.As(err, &sqlErr) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestParseMySQLRejectsSessionAndTransactionControl(t *testing.T) {
	for _, sql := range []string{
		"START TRANSACTION", "BEGIN", "begin work", "COMMIT", "ROLLBACK", "SAVEPOINT s", "RELEASE SAVEPOINT s", "XA START 'x'",
		"SET foreign_key_checks = 0", "set autocommit=0", "USE other", "LOCK TABLES a WRITE", "UNLOCK TABLES",
		"PREPARE s FROM 'SELECT 1'", "EXECUTE s", "DEALLOCATE PREPARE s", "LOAD DATA INFILE 'x' INTO TABLE a", "DELIMITER //",
		"/* leading */ COMMIT",
	} {
		_, err := ParseMySQL("CREATE TABLE ok (id INT);\n" + sql)
		var sqlErr *SQLError
		if !errors.As(err, &sqlErr) || sqlErr.Statement != 2 {
			t.Fatalf("%q: %v", sql, err)
		}
	}
	statements, err := ParseMySQL("CREATE TABLE a (id INT);\nALTER TABLE a ADD COLUMN begin_at DATETIME;\nINSERT INTO a (id) VALUES (1);\nCREATE TRIGGER t BEFORE INSERT ON a FOR EACH ROW SET NEW.id = NEW.id")
	if err != nil || len(statements) != 4 {
		t.Fatalf("accepted script = %d, %v", len(statements), err)
	}
}
