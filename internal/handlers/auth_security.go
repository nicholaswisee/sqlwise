package handlers

import (
	"errors"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/auth"
	"sqlwise/internal/web/views"
)

func (h *Auth) security(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
	if c.Request.URL.RawQuery != "" {
		Render(c, http.StatusBadRequest, views.AuthError(h.assets, "Submit credentials using the sign-in form."))
		c.Abort()
		return
	}
	if c.Request.Method == http.MethodPost {
		if err := http.NewCrossOriginProtection().Check(c.Request); err != nil {
			h.failure(c, auth.ErrCSRF)
			c.Abort()
			return
		}
		contentType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if err != nil || contentType != "application/x-www-form-urlencoded" {
			Render(c, http.StatusUnsupportedMediaType, views.AuthError(h.assets, "Use the sign-in form to submit this request."))
			c.Abort()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		if err := c.Request.ParseForm(); err != nil {
			Render(c, http.StatusUnprocessableEntity, views.AuthError(h.assets, "The submitted form is invalid or too large."))
			c.Abort()
			return
		}
	}
	c.Next()
}

func (h *Auth) failure(c *gin.Context, err error) {
	status, message := http.StatusServiceUnavailable, "Sign-in is temporarily unavailable. Please try again."
	switch {
	case errors.Is(err, auth.ErrCSRF):
		status, message = http.StatusForbidden, "This form has expired or could not be verified. Open the sign-in page and try again."
	case errors.Is(err, auth.ErrSession):
		status, message = http.StatusUnauthorized, "Your session has expired. Please sign in again."
	case errors.Is(err, auth.ErrBusy):
		c.Header("Retry-After", "1")
	}
	Render(c, status, views.AuthError(h.assets, message))
}

func (h *Auth) setCookie(c *gin.Context, cookie *http.Cookie) {
	cookie.Path = "/"
	cookie.HttpOnly = true
	cookie.SameSite = http.SameSiteLaxMode
	cookie.Secure = h.secure || c.Request.TLS != nil
	http.SetCookie(c.Writer, cookie)
}

func cookieValue(c *gin.Context, name string) string {
	cookie, err := c.Request.Cookie(name)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func csrfValue(c *gin.Context) string {
	if token := c.GetHeader("X-CSRF-Token"); token != "" {
		return token
	}
	return c.Request.PostForm.Get("csrf_token")
}
