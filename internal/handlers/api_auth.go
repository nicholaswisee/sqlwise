package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
)

func (h *Auth) RegisterAPI(router *gin.Engine) {
	api := router.Group("/api/v1/auth")
	api.GET("/login-challenge", h.apiLoginChallenge)
	api.POST("/login", h.apiLoginSecurity, h.apiLogin)
	api.GET("/session", h.RequireAPISession(), h.apiSession)
	api.POST("/logout", h.requireAPISession(true), h.apiLogout)
}

func (h *Auth) apiLoginChallenge(c *gin.Context) {
	h.securityHeaders(c)
	challenge, err := h.service.LoginChallenge(c.Request.Context(), cookieValue(c, loginCookie))
	if err != nil {
		apiFailure(c, err)
		return
	}
	h.setCookie(c, &http.Cookie{Name: loginCookie, Value: challenge.Token, Expires: challenge.ExpiresAt, MaxAge: int(auth.LoginLifetime.Seconds())})
	apiData(c, gin.H{"csrf_token": challenge.CSRFToken, "expires_at": challenge.ExpiresAt})
}

func (h *Auth) apiLoginSecurity(c *gin.Context) {
	h.securityHeaders(c)
	if err := http.NewCrossOriginProtection().Check(c.Request); err != nil {
		apiFailure(c, auth.ErrCSRF)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var input struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !apiDecode(c, &input) {
		return
	}
	c.Set("api_login_input", input)
	c.Next()
}

func (h *Auth) apiLogin(c *gin.Context) {
	input := c.MustGet("api_login_input").(struct {
		Username string `json:"username"`
		Password string `json:"password"`
	})
	csrf, challenge := csrfValue(c), cookieValue(c, loginCookie)
	if err := h.service.ValidateLoginCSRF(c.Request.Context(), challenge, csrf); err != nil {
		apiFailure(c, err)
		return
	}
	credentials, err := auth.ParseCredentials(input.Username, input.Password)
	if err != nil {
		apiError(c, http.StatusUnprocessableEntity, "validation", "Enter a valid username and password.")
		return
	}
	issued, err := h.service.Login(c.Request.Context(), auth.LoginRequest{Credentials: credentials, ChallengeToken: challenge, CSRFToken: csrf, PreviousSessionToken: cookieValue(c, sessionCookie)})
	if errors.Is(err, auth.ErrCredentials) {
		apiError(c, http.StatusUnauthorized, "credentials_invalid", "Unable to sign in with those credentials.")
		return
	}
	if err != nil {
		apiFailure(c, err)
		return
	}
	h.setCookie(c, &http.Cookie{Name: sessionCookie, Value: issued.Token, Expires: issued.ExpiresAt, MaxAge: int(auth.SessionLifetime.Seconds())})
	h.setCookie(c, &http.Cookie{Name: loginCookie, MaxAge: -1, Expires: time.Unix(1, 0)})
	apiData(c, gin.H{"user": apiPrincipal(issued.Principal), "csrf_token": issued.CSRFToken, "expires_at": issued.ExpiresAt})
}

func (h *Auth) RequireAPISession() gin.HandlerFunc {
	return h.requireAPISession(false)
}

func (h *Auth) requireAPISession(readOnly bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h.securityHeaders(c)
		unsafe := c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && c.Request.Method != http.MethodOptions
		if unsafe {
			if err := http.NewCrossOriginProtection().Check(c.Request); err != nil {
				apiFailure(c, auth.ErrCSRF)
				return
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		}
		token := cookieValue(c, sessionCookie)
		if token == "" || h.service == nil {
			apiFailure(c, auth.ErrSession)
			return
		}
		var session auth.Session
		var err error
		if unsafe || readOnly {
			session, err = h.service.ReadOnlySession(c.Request.Context(), token)
			if err != nil {
				apiFailure(c, err)
				return
			}
			if unsafe && !auth.ValidCSRF(session.CSRFToken, csrfValue(c)) {
				apiFailure(c, auth.ErrCSRF)
				return
			}
		}
		if !readOnly {
			session, err = h.service.Session(c.Request.Context(), token)
			if err != nil {
				apiFailure(c, err)
				return
			}
		}
		ctx, err := h.service.BindSession(c.Request.Context(), session)
		if err != nil {
			apiFailure(c, err)
			return
		}
		h.setReplacement(c, session.Replacement)
		_, err = h.service.ValidateContextFresh(ctx)
		if err != nil && !errors.Is(err, auth.ErrAuthorizationEpoch) {
			apiFailure(c, err)
			return
		}
		c.Set("api_needs_refresh", errors.Is(err, auth.ErrAuthorizationEpoch))
		c.Request = c.Request.WithContext(ctx)
		c.Set("auth_session", session)
		c.Next()
	}
}

func (h *Auth) RequireAPIPlatformAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		h.securityHeaders(c)
		principal, err := h.service.ValidateContextFresh(c.Request.Context())
		if errors.Is(err, auth.ErrAuthorizationEpoch) {
			apiFailure(c, err)
			return
		}
		if err != nil {
			apiFailure(c, err)
			return
		}
		if !principal.IsPlatformAdmin {
			apiError(c, http.StatusForbidden, "permission_denied", "You do not have permission to perform this action.")
			return
		}
		c.Next()
	}
}

func (h *Auth) RequireAPIDatabasePermission(checker DatabasePermissionChecker, param string, required access.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		h.securityHeaders(c)
		if c.Param(param) == "" {
			apiError(c, http.StatusNotFound, "not_found", "The requested resource was not found.")
			return
		}
		requireDatabasePermissionWithFailure(h.service.ValidateContextFresh, checker, param, required, func(c *gin.Context, err error) { apiFailure(c, err) })(c)
	}
}

func (h *Auth) apiSession(c *gin.Context) {
	session := c.MustGet("auth_session").(auth.Session)
	data := gin.H{"user": apiPrincipal(session.Principal), "csrf_token": session.CSRFToken, "expires_at": session.ExpiresAt, "databases": []any{}, "needs_refresh": c.GetBool("api_needs_refresh")}
	if h.access == nil {
		apiData(c, data)
		return
	}
	databases, err := h.access.ListDatabases(c.Request.Context(), session.Principal.UserID)
	if errors.Is(err, auth.ErrAuthorizationEpoch) {
		data["needs_refresh"] = true
		apiData(c, data)
		return
	}
	if err != nil {
		apiFailure(c, err)
		return
	}
	items := make([]gin.H, 0, len(databases))
	for _, db := range databases {
		items = append(items, gin.H{"id": db.ID, "name": db.Name, "permission": db.Permission.String()})
	}
	data["databases"] = items
	data["needs_refresh"] = false
	apiData(c, data)
}

func (h *Auth) apiLogout(c *gin.Context) {
	if err := h.service.Logout(c.Request.Context(), cookieValue(c, sessionCookie), csrfValue(c)); err != nil {
		apiFailure(c, err)
		return
	}
	h.setCookie(c, &http.Cookie{Name: sessionCookie, MaxAge: -1, Expires: time.Unix(1, 0)})
	c.Status(http.StatusNoContent)
}

func apiPrincipal(p auth.Principal) gin.H {
	return gin.H{"id": p.UserID, "username": p.Username, "is_platform_admin": p.IsPlatformAdmin}
}
