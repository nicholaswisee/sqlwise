package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/web/views"
)

func TestAdminFormErrorMappingAndActiveParser(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
		ok    bool
	}{
		{"true", true, true}, {"false", false, true}, {"", false, false}, {"yes", false, false},
	} {
		got, err := parseActive(tc.input)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("parseActive(%q)=(%v,%v), want (%v, ok=%v)", tc.input, got, err, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct {
		err  error
		want int
	}{
		{access.ErrInvalidInput, http.StatusUnprocessableEntity}, {access.ErrConflict, http.StatusConflict}, {access.ErrNotFound, http.StatusNotFound},
	} {
		got, _ := adminError(tc.err)
		if got != tc.want {
			t.Errorf("adminError(%v) status=%d want %d", tc.err, got, tc.want)
		}
	}
}

func TestAccessPageDataFiltersSelectedGroup(t *testing.T) {
	snapshot := access.AdminSnapshot{
		Users:       []auth.AdminUser{{ID: "u1", Username: "one", Active: true}},
		Groups:      []access.Group{{ID: "g1", Name: "one", Active: true}, {ID: "g2", Name: "two", Active: true}},
		Memberships: []access.Membership{{UserID: "u1", GroupID: "g1"}, {UserID: "u1", GroupID: "g2"}},
		Grants:      []access.GroupGrant{{GroupID: "g1", DatabaseID: "d1", Permission: access.View}, {GroupID: "g1", DatabaseID: "d3", Permission: access.Edit}, {GroupID: "g2", DatabaseID: "d2", Permission: access.Edit}},
		Databases:   []access.DatabaseRegistration{{ID: "d1", Name: "db one", Active: true}, {ID: "d2", Name: "db two", Active: true}, {ID: "d3", Name: "db inactive", Active: false}},
	}
	session := auth.Session{Principal: auth.Principal{Username: "admin"}, CSRFToken: "csrf"}

	data, err := accessPageData(snapshot, session, "g1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if data.SelectedGroup.ID != "g1" {
		t.Fatalf("selected group = %q", data.SelectedGroup.ID)
	}
	if len(data.Memberships) != 1 || data.Memberships[0].GroupID != "g1" {
		t.Fatalf("memberships leaked across groups: %+v", data.Memberships)
	}
	if len(data.Grants) != 2 || data.Grants[0].GroupID != "g1" || data.Grants[1].GroupID != "g1" {
		t.Fatalf("grants leaked across groups: %+v", data.Grants)
	}
	if data.Grants[1].DatabaseName != "db inactive" {
		t.Fatalf("inactive database name = %q", data.Grants[1].DatabaseName)
	}
	if len(data.Databases) != 3 || data.Databases[2].Active {
		t.Fatalf("database active state not preserved: %+v", data.Databases)
	}

	all, err := accessPageData(snapshot, session, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Memberships) != 2 || len(all.Grants) != 3 {
		t.Fatalf("overview should retain all groups: memberships=%d grants=%d", len(all.Memberships), len(all.Grants))
	}
}

func TestAccessPageDataRejectsUnknownGroup(t *testing.T) {
	_, err := accessPageData(access.AdminSnapshot{}, auth.Session{}, "missing", "", "")
	if err != access.ErrNotFound {
		t.Fatalf("error = %v, want %v", err, access.ErrNotFound)
	}
}

func TestUserUpdateRequiresExactlyOneField(t *testing.T) {
	for _, tc := range []struct {
		name, username, password string
		wantUser, wantPassword   string
		wantErr                  bool
	}{
		{name: "username only", username: "new-name", wantUser: "new-name"},
		{name: "password only", password: "a-long-enough-password", wantPassword: "a-long-enough-password"},
		{name: "neither", wantErr: true},
		{name: "both", username: "new-name", password: "a-long-enough-password", wantErr: true},
		{name: "whitespace username", username: "  ", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			username, password, err := userUpdateInput(tc.username, tc.password)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v, wantErr=%v", err, tc.wantErr)
			}
			if username != tc.wantUser || password != tc.wantPassword {
				t.Fatalf("got (%q,%q), want (%q,%q)", username, password, tc.wantUser, tc.wantPassword)
			}
		})
	}
}

func TestAdminFailureFeedbackClearsSuccessMessage(t *testing.T) {
	status, message, problem := adminFeedback("User created.", auth.ErrInvalidInput)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", status)
	}
	if message != "" {
		t.Fatalf("failure retained success message %q", message)
	}
	if problem == "" {
		t.Fatal("failure feedback has no error message")
	}
}

func TestAdminBoundaryFailurePreservesSessionAndEpochResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AccessAdmin{auth: &Auth{assets: views.Assets{Dev: true}}, assets: views.Assets{Dev: true}}
	for _, scenario := range []struct {
		name, method string
		err          error
		status       int
		redirect     string
	}{
		{"revoked native", http.MethodPost, auth.ErrSession, http.StatusUnauthorized, ""},
		{"revoked htmx", http.MethodPost, auth.ErrSession, http.StatusUnauthorized, "/login"},
		{"changed native", http.MethodPost, auth.ErrAuthorizationEpoch, http.StatusForbidden, ""},
		{"changed htmx", http.MethodPost, auth.ErrAuthorizationEpoch, http.StatusForbidden, "/account"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			router := gin.New()
			router.POST("/admin/test", func(c *gin.Context) {
				if !h.boundaryFailure(c, scenario.err) {
					t.Fatal("boundary error was not handled")
				}
			})
			request := httptest.NewRequest(scenario.method, "/admin/test", nil)
			if scenario.redirect != "" {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != scenario.status || response.Header().Get("HX-Redirect") != scenario.redirect {
				t.Fatalf("status=%d redirect=%q, want %d %q", response.Code, response.Header().Get("HX-Redirect"), scenario.status, scenario.redirect)
			}
		})
	}
}
