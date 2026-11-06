package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"sqlwise/internal/queryplan"
)

func TestAPIPlanPreservesRecursiveStructuredPlan(t *testing.T) {
	plan := apiNode(queryplan.Node{Type: "Nested Loop", Children: []queryplan.Node{{Type: "Seq Scan", Relation: "items", PlanRows: 4}}})
	body, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{`"type":"Nested Loop"`, `"children":[{"type":"Seq Scan"`, `"relation":"items"`, `"plan_rows":4`} {
		if !strings.Contains(got, want) {
			t.Fatalf("plan JSON %s missing %s", got, want)
		}
	}
	if strings.Contains(strings.ToLower(got), "<div") {
		t.Fatalf("plan contains HTML: %s", got)
	}
}

func TestAPICellUsesWireKeysAndPreservesFlags(t *testing.T) {
	body, err := json.Marshal(apiCell{Text: `\\xAB`, Null: false, Binary: true, Truncated: true})
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, key := range []string{`"text":"\\\\xAB"`, `"null":false`, `"binary":true`, `"truncated":true`} {
		if !strings.Contains(got, key) {
			t.Fatalf("cell JSON %s missing %s", got, key)
		}
	}
	for _, key := range []string{"Text", "Null", "Binary", "Truncated"} {
		if strings.Contains(got, key) {
			t.Fatalf("Go field name leaked: %s", got)
		}
	}
}
