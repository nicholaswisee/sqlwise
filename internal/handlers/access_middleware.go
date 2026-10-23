package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
)

// DatabasePermissionChecker is the target access contract for protected routes.
type DatabasePermissionChecker interface {
	Require(ctx context.Context, userID, databaseID string, required access.Permission) error
}

// RequireDatabasePermission checks the session-bound principal and its live
// group-derived permission before the target handler can run.
func (h *Auth) RequireDatabasePermission(checker DatabasePermissionChecker, databaseParam string, required access.Permission) gin.HandlerFunc {
	next := requireDatabasePermissionWithFailure(h.service.ValidateContextFresh, checker, databaseParam, required, func(c *gin.Context, err error) {
		if errors.Is(err, auth.ErrAuthorizationEpoch) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if errors.Is(err, access.ErrPermissionDenied) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		h.sessionFailure(c, err)
	})
	return func(c *gin.Context) { h.securityHeaders(c); next(c) }
}

type principalValidator func(context.Context) (auth.Principal, error)

func requireDatabasePermission(validate principalValidator, checker DatabasePermissionChecker, databaseParam string, required access.Permission) gin.HandlerFunc {
	return requireDatabasePermissionWithFailure(validate, checker, databaseParam, required, func(c *gin.Context, err error) {
		status := http.StatusServiceUnavailable
		if errors.Is(err, auth.ErrAuthorizationEpoch) || errors.Is(err, access.ErrPermissionDenied) {
			status = http.StatusForbidden
		} else if errors.Is(err, auth.ErrSession) {
			status = http.StatusUnauthorized
		}
		c.AbortWithStatus(status)
	})
}

func requireDatabasePermissionWithFailure(validate principalValidator, checker DatabasePermissionChecker, databaseParam string, required access.Permission, fail func(*gin.Context, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, err := validate(c.Request.Context())
		if err != nil {
			fail(c, err)
			return
		}
		databaseID := c.Param(databaseParam)
		if databaseID == "" {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if err := checker.Require(c.Request.Context(), principal.UserID, databaseID, required); err != nil {
			fail(c, err)
			return
		}
		c.Next()
	}
}
