package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAdminAPIDTOUsesSafeSnakeCase(t *testing.T) {
	data, err := json.Marshal(adminSnapshotDTO{Users: []apiAdminUser{{ID: "u1", Username: "alice", IsPlatformAdmin: true}}})
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{`"is_platform_admin":true`, `"username":"alice"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("JSON %s missing %s", body, want)
		}
	}
	for _, forbidden := range []string{"password", "csrf", "CSRF", "secret"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Fatalf("unsafe field in %s", body)
		}
	}
}
