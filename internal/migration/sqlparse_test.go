package migration

import (
	"errors"
	"strings"
	"testing"
)

func TestSplitPostgresKeepsQuotedBodiesIntact(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
		want []string
	}{
		{"simple", "CREATE TABLE a (id int); INSERT INTO a VALUES (1);", []string{"CREATE TABLE a (id int)", "INSERT INTO a VALUES (1)"}},
		{"no-trailing-semicolon", "SELECT 1", []string{"SELECT 1"}},
		{"string", "INSERT INTO a VALUES ('x;y'), ('it''s; ok');", []string{"INSERT INTO a VALUES ('x;y'), ('it''s; ok')"}},
		{"escape-string", `INSERT INTO a VALUES (E'a\';b'); SELECT 2`, []string{`INSERT INTO a VALUES (E'a\';b')`, "SELECT 2"}},
		{"identifier", `CREATE TABLE "semi;colon" ("a""b;" int); SELECT 1`, []string{`CREATE TABLE "semi;colon" ("a""b;" int)`, "SELECT 1"}},
		{"line-comment", "SELECT 1; -- trailing; comment\nSELECT 2;", []string{"SELECT 1", "-- trailing; comment\nSELECT 2"}},
		{"nested-block-comment", "SELECT /* a /* b; */ c; */ 1; SELECT 2", []string{"SELECT /* a /* b; */ c; */ 1", "SELECT 2"}},
		{"dollar-body", "CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END; $$; SELECT f();",
			[]string{"CREATE FUNCTION f() RETURNS int LANGUAGE plpgsql AS $$ BEGIN RETURN 1; END; $$", "SELECT f()"}},
		{"tagged-dollar", "DO $body$ BEGIN PERFORM '$$;'; END $body$; SELECT 1", []string{"DO $body$ BEGIN PERFORM '$$;'; END $body$", "SELECT 1"}},
		{"identifier-with-dollar", "SELECT a$b$c FROM t; SELECT 2", []string{"SELECT a$b$c FROM t", "SELECT 2"}},
		{"positional", "SELECT a FROM t WHERE b = $1; SELECT 2", []string{"SELECT a FROM t WHERE b = $1", "SELECT 2"}},
		{"reserved-in-comment", "-- not sqlwise_internal\nSELECT 1 /* sqlwise_internal */", []string{"-- not sqlwise_internal\nSELECT 1 /* sqlwise_internal */"}},
		{"begin-atomic", "CREATE OR REPLACE FUNCTION f(x int) RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT CASE WHEN x > 0 THEN 1 ELSE 0 END; SELECT 2; END; SELECT 3",
			[]string{"CREATE OR REPLACE FUNCTION f(x int) RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT CASE WHEN x > 0 THEN 1 ELSE 0 END; SELECT 2; END", "SELECT 3"}},
		{"empty-statements", ";; SELECT 1;;\n-- only comment;\n", []string{"SELECT 1"}},
		{"unicode", "INSERT INTO t VALUES ('界;'); SELECT 'ok'", []string{"INSERT INTO t VALUES ('界;')", "SELECT 'ok'"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SplitPostgres(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d statements %q, want %q", len(got), texts(got), tc.want)
			}
			for i := range got {
				if got[i].SQL != tc.want[i] {
					t.Errorf("statement %d = %q, want %q", i+1, got[i].SQL, tc.want[i])
				}
			}
		})
	}
}

func TestSplitPostgresRejectsMalformedScripts(t *testing.T) {
	for _, tc := range []struct{ name, sql, reason string }{
		{"empty", "  \n", "no statements"},
		{"comments-only", "-- nothing\n/* here */", "no statements"},
		{"string", "SELECT 'oops", "unterminated quoted string"},
		{"identifier", `SELECT "oops`, "unterminated quoted identifier"},
		{"comment", "SELECT 1 /* /* */", "unterminated /* comment"},
		{"dollar", "DO $$ BEGIN", "unterminated dollar"},
		{"atomic", "CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1;", "syntax error at end of input"},
		{"meta", `\connect other`, "meta-command"},
		{"meta-later-line", "SELECT 1;\n\\dt", "line 2: psql meta-command"},
		{"syntax", "CREATE TABLE a (id int);\nALTER TABLE a ADD COLUM b int;", "line 2: syntax error at or near"},
		{"keyword-typo", "SELEC 1", "syntax error"},
		{"nul", "SELECT 1\x00", "NUL"},
		{"utf8", "SELECT '\xff'", "UTF-8"},
		{"size", "SELECT '" + strings.Repeat("x", MaxSQLBytes) + "'", "exceeds"},
		{"count", strings.Repeat("SELECT 1;", MaxStatements+1), "more than"},
		{"reserved", "DELETE FROM sqlwise_internal.migration_receipts", "reserved"},
		{"reserved-quoted", `DROP TABLE "SQLWISE_INTERNAL"."x"; SELECT 1`, "reserved"},
		{"reserved-mixed-case", "drop schema SqlWise_Internal cascade", "reserved"},
		{"reserved-dynamic", "DO $$ BEGIN EXECUTE 'drop schema sqlwise_internal cascade'; END $$", "reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SplitPostgres(tc.sql)
			var sqlErr *SQLError
			if !errors.As(err, &sqlErr) || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("error = %v, want reason containing %q", err, tc.reason)
			}
		})
	}
}

func TestValidatePostgresRejectsUnsupportedStatements(t *testing.T) {
	rejected := []string{
		"BEGIN", "begin transaction", "START TRANSACTION", "COMMIT", "END", "ROLLBACK", "ABORT",
		"SAVEPOINT s", "RELEASE SAVEPOINT s", "ROLLBACK TO SAVEPOINT s", "PREPARE TRANSACTION 'x'",
		"COMMIT PREPARED 'x'", "PREPARE q AS SELECT 1", "SET TRANSACTION ISOLATION LEVEL SERIALIZABLE",
		"SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY", "SET search_path = other", "SET ROLE admin",
		"SET LOCAL ROLE admin", "SET SESSION AUTHORIZATION admin", "RESET ALL", "DISCARD ALL",
		"VACUUM", "VACUUM FULL t", "CHECKPOINT", "LOAD 'x'", "COPY t FROM STDIN", "CLUSTER", "CLUSTER VERBOSE",
		"REINDEX DATABASE d", "REINDEX SYSTEM d", "REINDEX TABLE CONCURRENTLY t", "ALTER SYSTEM SET x = 1",
		"ALTER DATABASE d SET TABLESPACE t", "CREATE DATABASE d", "DROP DATABASE d", "CREATE TABLESPACE t LOCATION '/x'",
		"DROP TABLESPACE t", "CREATE SUBSCRIPTION s CONNECTION 'x' PUBLICATION p", "DROP SUBSCRIPTION s",
		"CREATE INDEX CONCURRENTLY i ON t (a)", "create unique index concurrently i on t (a)", "DROP INDEX CONCURRENTLY i",
		"ALTER TABLE p DETACH PARTITION c CONCURRENTLY",
	}
	for _, sql := range rejected {
		t.Run(sql, func(t *testing.T) {
			if _, err := ParsePostgres("SELECT 1;\n" + sql); err == nil || !strings.HasPrefix(err.Error(), "statement 2:") {
				t.Fatalf("ParsePostgres(%q) error = %v, want statement 2 rejection", sql, err)
			}
		})
	}
	accepted := []string{
		"CREATE TABLE t (id bigint PRIMARY KEY)", "ALTER TABLE t ADD COLUMN c text", "CREATE INDEX i ON t (c)",
		"SET LOCAL lock_timeout = '1s'", "SET CONSTRAINTS ALL DEFERRED", "CLUSTER t USING i", "REINDEX TABLE t",
		"REFRESH MATERIALIZED VIEW CONCURRENTLY m", "ALTER TYPE mood ADD VALUE 'ok'", "INSERT INTO t (c) VALUES ('begin; commit')",
		"WITH x AS (SELECT 1) SELECT * FROM x", "COMMENT ON TABLE t IS 'VACUUM; COMMIT'", `CREATE TABLE "Begin" (id int)`,
	}
	for _, sql := range accepted {
		t.Run(sql, func(t *testing.T) {
			if _, err := ParsePostgres(sql); err != nil {
				t.Fatalf("ParsePostgres(%q) = %v", sql, err)
			}
		})
	}
}

func texts(statements []Statement) []string {
	out := make([]string, len(statements))
	for i, s := range statements {
		out[i] = s.SQL
	}
	return out
}
