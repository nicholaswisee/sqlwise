package handlers

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
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
	t.Cleanup(h.pools.Close)
	h.RegisterAPI(r)
	NewAccessAdmin(authHandler, policy, views.Assets{}).RegisterAPI(r)
	r.GET("/api/v1/test-target/:databaseID", authHandler.RequireAPISession(), authHandler.RequireAPIDatabasePermission(policy, "databaseID", access.View), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	cookie, csrf := apiIntegrationLogin(t, r, "connection-admin", "a sufficiently long password")

	pg, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf(`{"name":"managed target","dbms":"postgresql","host":%q,"port":%q,"database":%q,"username":%q,"password":%q,"tls":"disable","sandbox":true}`, pg.ConnConfig.Host, fmt.Sprint(pg.ConnConfig.Port), pg.ConnConfig.Database, pg.ConnConfig.User, pg.ConnConfig.Password)
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
	deniedTarget := apiIntegrationRequest(t, r, http.MethodGet, "/api/v1/test-target/"+id, "", &cookie, csrf)
	if deniedTarget.Code != http.StatusForbidden {
		t.Fatalf("platform admin without grants status=%d body=%s", deniedTarget.Code, deniedTarget.Body)
	}
	group := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/groups", `{"name":"database readers"}`, &cookie, csrf)
	var groupData struct {
		Data struct {
			Groups []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"groups"`
		} `json:"data"`
	}
	if group.Code != http.StatusOK || json.Unmarshal(group.Body.Bytes(), &groupData) != nil {
		t.Fatalf("create group status=%d body=%s", group.Code, group.Body)
	}
	var groupID string
	for _, g := range groupData.Data.Groups {
		if g.Name == "database readers" {
			groupID = g.ID
		}
	}
	grant := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/groups/"+groupID+"/databases", `{"database_id":"`+id+`","permission":"view"}`, &cookie, csrf)
	if grant.Code != http.StatusOK || !strings.Contains(grant.Body.String(), id) {
		t.Fatalf("set grant status=%d body=%s", grant.Code, grant.Body)
	}
	removeGrant := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/groups/"+groupID+"/databases/remove", `{"database_id":"`+id+`"}`, &cookie, csrf)
	var grantsAfter struct {
		Data struct {
			Grants []json.RawMessage `json:"grants"`
		} `json:"data"`
	}
	if removeGrant.Code != http.StatusOK || json.Unmarshal(removeGrant.Body.Bytes(), &grantsAfter) != nil || len(grantsAfter.Data.Grants) != 0 {
		t.Fatalf("remove grant status=%d body=%s", removeGrant.Code, removeGrant.Body)
	}
	tested := apiIntegrationRequest(t, r, http.MethodPost, "/api/v1/admin/databases/"+id+"/test", `{}`, &cookie, csrf)
	if tested.Code != http.StatusOK || !strings.Contains(tested.Body.String(), `"ran":true`) || !strings.Contains(tested.Body.String(), `"ok":true`) {
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
