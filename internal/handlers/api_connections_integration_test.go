package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/migration"
	"sqlwise/internal/testdb"
	"sqlwise/internal/web/views"
)

func TestConnectionsAPIPostgresCreateEditActiveAndSecretRedaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pool := testdb.Open(t)
	authStore := auth.NewStore(pool)
	if err := authStore.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	accessStore := access.NewStore(pool)
	if err := accessStore.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := migration.NewStore(pool).ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	cipher, err := database.NewCipher(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	connectionStore := database.NewStore(pool, cipher)
	if err := connectionStore.ApplySchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	creds, err := auth.ParseCredentials("connection-admin", "a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}
	if err := authStore.BootstrapAdmin(t.Context(), creds); err != nil {
		t.Fatal(err)
	}
	sessions, err := auth.NewService(authStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	policy := access.NewService(accessStore, sessions)
	authHandler := NewAuth(sessions, views.Assets{}, false, policy)
	r := gin.New()
	authHandler.RegisterAPI(r)
	h := NewConnectionAdmin(authHandler, policy, connectionStore, database.NewPools(connectionStore), views.Assets{})
	h.RegisterAPI(r)
	cookie, csrf := apiIntegrationLogin(t, r, "connection-admin", "a sufficiently long password")

	payload := `{"name":"managed target","dbms":"postgresql","host":"db.invalid","port":"5432","database":"app","username":"wire_user","password":"sentinel-secret","tls":"disable","sandbox":true}`
	created := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/databases", payload, &cookie, csrf)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body)
	}
	if strings.Contains(created.Body.String(), "sentinel-secret") || strings.Contains(strings.ToLower(created.Body.String()), "password") {
		t.Fatalf("secret field leaked: %s", created.Body)
	}
	var detail struct {
		Data struct {
			Form struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Active bool   `json:"active"`
			} `json:"form"`
			Capabilities []json.RawMessage `json:"capabilities"`
			ReceiptSQL   string            `json:"receipt_sql"`
		} `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	id := detail.Data.Form.ID
	if id == "" || detail.Data.Form.Name != "managed target" || len(detail.Data.Capabilities) == 0 || detail.Data.ReceiptSQL == "" {
		t.Fatalf("incomplete detail: %s", created.Body)
	}
	tested := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/databases/"+id+"/test", `{}`, &cookie, csrf)
	if tested.Code != http.StatusOK || !strings.Contains(tested.Body.String(), `"ran":true`) || !strings.Contains(tested.Body.String(), `"ok":false`) {
		t.Fatalf("test result status=%d body=%s", tested.Code, tested.Body)
	}
	active := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/databases/"+id+"/active", `{"active":false}`, &cookie, csrf)
	if active.Code != http.StatusOK || !strings.Contains(active.Body.String(), `"active":false`) {
		t.Fatalf("active status=%d body=%s", active.Code, active.Body)
	}
	edit := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/databases/"+id, `{"name":"renamed target","dbms":"postgresql","host":"db.invalid","port":"5432","database":"app","username":"wire_user","password":"rotated-secret","tls":"disable","sandbox":false}`, &cookie, csrf)
	if edit.Code != http.StatusOK || !strings.Contains(edit.Body.String(), "renamed target") {
		t.Fatalf("edit status=%d body=%s", edit.Code, edit.Body)
	}
	if strings.Contains(edit.Body.String(), "rotated-secret") || strings.Contains(strings.ToLower(edit.Body.String()), "password") {
		t.Fatalf("secret field leaked: %s", edit.Body)
	}
	bad := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/databases", `{"name":"bad","dbms":"sqlite"}`, &cookie, csrf)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("validation status=%d body=%s", bad.Code, bad.Body)
	}
	if strings.Contains(strings.ToLower(bad.Body.String()), "sentinel-secret") || strings.Contains(strings.ToLower(bad.Body.String()), "password") {
		t.Fatalf("validation response leaked input: %s", bad.Body)
	}
}
