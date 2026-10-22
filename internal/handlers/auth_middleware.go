package handlers

import (
	"errors"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/auth"
)

// RequireSession binds the originally presented evidence, never its replacement.
func (h *Auth) RequireSession() gin.HandlerFunc { return h.requireSession(false) }

func (h *Auth) requireSession(readOnly bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h.securityHeaders(c)
		unsafe := c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions
		if unsafe {
			if err := http.NewCrossOriginProtection().Check(c.Request); err != nil {
				h.failure(c, auth.ErrCSRF)
				return
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
			contentType, _, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
			if contentType == "application/x-www-form-urlencoded" {
				if err := c.Request.ParseForm(); err != nil {
					h.failure(c, auth.ErrCSRF)
					return
				}
			}
		}
		token := cookieValue(c, sessionCookie)
		var session auth.Session
		var err error
		if unsafe || readOnly {
			session, err = h.service.ReadOnlySession(c.Request.Context(), token)
			if err != nil {
				h.sessionFailure(c, err)
				return
			}
			if unsafe && !auth.ValidCSRF(session.CSRFToken, csrfValue(c)) {
				h.failure(c, auth.ErrCSRF)
				return
			}
		}
		if !readOnly {
			session, err = h.service.Session(c.Request.Context(), token)
			if err != nil {
				h.sessionFailure(c, err)
				return
			}
		}
		ctx, err := h.service.BindSession(c.Request.Context(), session)
		if err != nil {
			h.sessionFailure(c, err)
			return
		}
		// Rotation is committed; preserve its winner even if fresh validation fails.
		h.setReplacement(c, session.Replacement)
		_, err = h.service.ValidateContextFresh(ctx)
		if err != nil && !errors.Is(err, auth.ErrAuthorizationEpoch) {
			h.sessionFailure(c, err)
			return
		}
		c.Request = c.Request.WithContext(ctx)
		c.Set("auth_session", session)
		c.Next()
	}
}

// RequirePlatformAdmin revalidates evidence bound by RequireSession without touching it.
func (h *Auth) RequirePlatformAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		h.securityHeaders(c)
		principal, err := h.service.ValidateContextFresh(c.Request.Context())
		if errors.Is(err, auth.ErrAuthorizationEpoch) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		if err != nil {
			h.sessionFailure(c, err)
			return
		}
		if !principal.IsPlatformAdmin {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		c.Next()
	}
}

func (h *Auth) sessionFailure(c *gin.Context, err error) {
	if errors.Is(err, auth.ErrSession) {
		h.rejectSession(c)
	} else {
		h.failure(c, err)
	}
}
