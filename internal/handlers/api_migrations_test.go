package handlers

import (
	"encoding/json"
	"strings"
	"testing"

	"sqlwise/internal/migration"
)

func TestMigrationAPIDTOsAllowlistImmutableSafeEvidence(t *testing.T) {
	d := apiDraftDTO(migration.Draft{ID: "draft", DatabaseID: "db", Title: "safe", SQL: "select 1", Revision: 2})
	a := apiAttemptDTO(migration.Attempt{ID: "attempt", DatabaseID: "db", MigrationID: "draft", SQL: "select 1", Status: migration.StatusFailed, ExecutorName: "editor"})
	body, err := json.Marshal(struct {
		Draft   apiDraft   `json:"draft"`
		Attempt apiAttempt `json:"attempt"`
	}{d, a})
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, key := range []string{`"sql_hash"`, `"revision"`, `"status"`, `"executor_name"`, `"started_at"`} {
		if !strings.Contains(text, key) {
			t.Fatalf("API DTO missing %s: %s", key, text)
		}
	}
	for _, forbidden := range []string{"password", "secret", "connection_string", "store"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("unexpected private field %q in %s", forbidden, text)
		}
	}
}
