package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
)

type permissionSpy struct {
	userID, databaseID string
	required           access.Permission
	err                error
	calls              int
}

func (s *permissionSpy) Require(_ context.Context, userID, databaseID string, required access.Permission) error {
	s.calls++
	s.userID = userID
	s.databaseID = databaseID
	s.required = required
	return s.err
}

func TestRequireDatabasePermissionUsesBoundPrincipalBeforeNext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	spy := &permissionSpy{}
	called := false
	r := gin.New()
	r.GET("/db/:database", func(c *gin.Context) { c.Next() }, requireDatabasePermission(func(context.Context) (auth.Principal, error) { return auth.Principal{UserID: "bound-user"}, nil }, spy, "database", access.Edit), func(c *gin.Context) { called = true; c.Status(http.StatusNoContent) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/db/target-a", nil))
	if w.Code != http.StatusNoContent || !called {
		t.Fatalf("status=%d called=%v", w.Code, called)
	}
	if spy.calls != 1 || spy.userID != "bound-user" || spy.databaseID != "target-a" || spy.required != access.Edit {
		t.Fatalf("unexpected authorization call: %+v", spy)
	}
}

func TestRequireDatabasePermissionDenialsAbortBeforeTargetWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name                      string
		validationErr, requireErr error
		wantStatus                int
		wantCalls                 int
	}{
		{"expired proof", auth.ErrSession, nil, http.StatusUnauthorized, 0},
		{"stale privilege", auth.ErrAuthorizationEpoch, nil, http.StatusForbidden, 0},
		{"no access", nil, access.ErrPermissionDenied, http.StatusForbidden, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spy := &permissionSpy{err: tc.requireErr}
			called := false
			r := gin.New()
			r.GET("/db/:database", requireDatabasePermission(func(context.Context) (auth.Principal, error) {
				return auth.Principal{UserID: "bound-user"}, tc.validationErr
			}, spy, "database", access.View), func(c *gin.Context) { called = true; c.Status(http.StatusNoContent) })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/db/target-a", nil))
			if w.Code != tc.wantStatus || called || spy.calls != tc.wantCalls {
				t.Fatalf("status=%d called=%v checker calls=%d", w.Code, called, spy.calls)
			}
		})
	}
}

func TestRequireDatabasePermissionPropagatesCheckerFailureSafely(t *testing.T) {
	gin.SetMode(gin.TestMode)
	spy := &permissionSpy{err: errors.New("database unavailable")}
	called := false
	r := gin.New()
	r.GET("/db/:database", requireDatabasePermission(func(context.Context) (auth.Principal, error) { return auth.Principal{UserID: "bound-user"}, nil }, spy, "database", access.View), func(c *gin.Context) { called = true })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/db/target-a", nil))
	if w.Code != http.StatusServiceUnavailable || called {
		t.Fatalf("status=%d called=%v", w.Code, called)
	}
}
