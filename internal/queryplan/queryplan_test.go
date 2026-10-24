package queryplan

import (
	"errors"
	"strings"
	"testing"

	"sqlwise/internal/database"
)

func TestValidateClassifiesStatements(t *testing.T) {
	for _, tc := range []struct {
		sql  string
		mode Mode
		want Kind
	}{
		{"SELECT * FROM customers WHERE id = 1", ModeExplain, Read},
		{"SELECT count(*), lower(name) FROM customers GROUP BY 2", ModeAnalyze, Read},
		{"WITH recent AS (SELECT * FROM orders) SELECT * FROM recent", ModeAnalyze, Read},
		{"SELECT pg_catalog.lower('X')", ModeAnalyze, Read},
		{"INSERT INTO notes VALUES (1)", ModeExplain, Write},
		{"UPDATE customers SET name = 'x' WHERE id = 1", ModeExplain, Write},
		{"DELETE FROM customers WHERE id = 1", ModeExplain, Write},
		{"WITH gone AS (DELETE FROM orders RETURNING *) SELECT count(*) FROM gone", ModeExplain, Write},
		{"SELECT 1;", ModeExplain, Read},
	} {
		got, err := Validate(tc.sql, tc.mode)
		if err != nil || got != tc.want {
			t.Fatalf("Validate(%q, %s) = %v, %v", tc.sql, tc.mode, got, err)
		}
	}
}

func TestValidateRejectsUnsafeStatements(t *testing.T) {
	for _, tc := range []struct {
		sql    string
		mode   Mode
		reason string
	}{
		{"", ModeExplain, "Enter a statement"},
		{"SELECT 1; SELECT 2", ModeExplain, "one statement"},
		{"SELECT 1; DROP TABLE customers", ModeExplain, "one statement"},
		{"CREATE TABLE x (id int)", ModeExplain, "DDL"},
		{"DROP TABLE customers", ModeExplain, "DDL"},
		{"BEGIN", ModeExplain, "transaction"},
		{"COMMIT", ModeExplain, "transaction"},
		{"EXPLAIN SELECT 1", ModeExplain, "Only SELECT"},
		{"SELECT * INTO copy FROM customers", ModeExplain, "SELECT INTO"},
		{"SELEC 1", ModeExplain, "could not be parsed"},
		{"SELECT 1", "visualize", "Choose Explain or Analyze"},
		{"DELETE FROM customers", ModeAnalyze, "limited to SELECT"},
		{"WITH gone AS (DELETE FROM orders RETURNING *) SELECT 1", ModeAnalyze, "limited to SELECT"},
		{"SELECT * FROM customers FOR UPDATE", ModeAnalyze, "read-only"},
		{"SELECT pg_sleep(30)", ModeAnalyze, "pg_sleep"},
		{"SELECT nextval('orders_id_seq')", ModeAnalyze, "nextval"},
		{"SELECT public.wipe_everything()", ModeAnalyze, "public.wipe_everything"},
		{"SELECT * FROM customers WHERE id IN (SELECT dblink_exec('x', 'y'))", ModeAnalyze, "dblink_exec"},
	} {
		_, err := Validate(tc.sql, tc.mode)
		var domain *database.Error
		if !errors.As(err, &domain) || !strings.Contains(domain.Message, tc.reason) {
			t.Fatalf("Validate(%q, %s) = %v, want %q", tc.sql, tc.mode, err, tc.reason)
		}
	}
}

func TestParsePostgresKeepsNestedNodesAndOptionalActuals(t *testing.T) {
	estimated := `[{"Plan": {"Node Type": "Hash Join", "Join Type": "Inner", "Startup Cost": 1.5, "Total Cost": 42.25, "Plan Rows": 300,
		"Plans": [
			{"Node Type": "Seq Scan", "Relation Name": "orders", "Startup Cost": 0, "Total Cost": 10, "Plan Rows": 300},
			{"Node Type": "Hash", "Startup Cost": 1, "Total Cost": 1, "Plan Rows": 250, "Plans": [
				{"Node Type": "Index Scan", "Relation Name": "customers", "Index Name": "customers_pkey", "Startup Cost": 0.1, "Total Cost": 8, "Plan Rows": 250}]}]},
		"Planning Time": 0.2}]`
	root, planning, execution, err := ParsePostgres([]byte(estimated))
	if err != nil {
		t.Fatal(err)
	}
	if root.Type != "Hash Join" || root.Join != "Inner" || root.TotalCost != 42.25 || len(root.Children) != 2 || root.ActualTime != nil {
		t.Fatalf("root = %+v", root)
	}
	if leaf := root.Children[1].Children[0]; leaf.Type != "Index Scan" || leaf.Index != "customers_pkey" || leaf.Relation != "customers" {
		t.Fatalf("leaf = %+v", leaf)
	}
	if planning == nil || *planning != 0.2 || execution != nil {
		t.Fatalf("times = %v %v", planning, execution)
	}
	modify, _, _, err := ParsePostgres([]byte(`[{"Plan": {"Node Type": "ModifyTable", "Operation": "Delete", "Relation Name": "customers", "Plans": [{"Node Type": "Seq Scan", "Relation Name": "customers"}]}}]`))
	if err != nil || modify.Operation != "Delete" || modify.Children[0].Type != "Seq Scan" {
		t.Fatalf("modify = %+v, %v", modify, err)
	}
	analyzed := `[{"Plan": {"Node Type": "Seq Scan", "Relation Name": "customers", "Startup Cost": 0, "Total Cost": 5, "Plan Rows": 250,
		"Actual Total Time": 0.08, "Actual Rows": 250, "Actual Loops": 1}, "Planning Time": 0.1, "Execution Time": 0.3}]`
	root, _, execution, err = ParsePostgres([]byte(analyzed))
	if err != nil || root.ActualRows == nil || *root.ActualRows != 250 || *root.Loops != 1 || execution == nil || *execution != 0.3 {
		t.Fatalf("analyzed = %+v, %v, %v", root, execution, err)
	}
	for _, bad := range []string{"not json", "[]", `[{"Plan": {}}]`} {
		if _, _, _, err := ParsePostgres([]byte(bad)); err == nil {
			t.Fatalf("ParsePostgres(%q) accepted", bad)
		}
	}
}
