package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConnectionAPIResponseNeverSerializesPassword(t *testing.T) {
	body, err := json.Marshal(apiConnectionDetail{Form: apiConnectionForm{ID: "d1", Username: "dbuser"}, ReceiptSQL: "CREATE ROLE dbuser"})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(body))
	if strings.Contains(text, "password") || strings.Contains(text, "secret") {
		t.Fatalf("unsafe response: %s", body)
	}
	for _, field := range []string{`"receipt_sql"`, `"username"`, `"capabilities"`} {
		if !strings.Contains(string(body), field) {
			t.Fatalf("response %s missing %s", body, field)
		}
	}
}
