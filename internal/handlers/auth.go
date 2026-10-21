package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/auth"
	"sqlwise/internal/web/views"
)

const (
	sessionCookie = "sqlwise_session"
	loginCookie   = "sqlwise_login"
)

type Auth struct {
	service *auth.Service
	assets  views.Assets
	secure  bool
}

func NewAuth(service *auth.Service, assets views.Assets, secure bool) *Auth {
	return &Auth{service: service, assets: assets, secure: secure}
}

func (h *Auth) Register(router *gin.Engine) {
	group := router.Group("", h.security)
	group.GET("/login", h.loginPage)
	group.POST("/login", h.login)
	group.GET("/account", h.account)
	group.POST("/logout", h.logout)
}

func (h *Auth) loginPage(c *gin.Context) {
	if session, err := h.service.Session(c.Request.Context(), cookieValue(c, sessionCookie)); err == nil {
		h.setReplacement(c, session.Replacement)
		c.Redirect(http.StatusSeeOther, "/account")
		return
	} else if !errors.Is(err, auth.ErrSession) {
		h.failure(c, err)
		return
	}
	challenge, err := h.service.LoginChallenge(c.Request.Context(), cookieValue(c, loginCookie))
	if err != nil {
		h.failure(c, err)
		return
	}
	h.setCookie(c, &http.Cookie{Name: loginCookie, Value: challenge.Token, Expires: challenge.ExpiresAt, MaxAge: int(auth.LoginLifetime.Seconds())})
	Render(c, http.StatusOK, views.Login(h.assets, "", challenge.CSRFToken, ""))
}

func (h *Auth) login(c *gin.Context) {
	csrf := csrfValue(c)
	challenge := cookieValue(c, loginCookie)
	if err := h.service.ValidateLoginCSRF(c.Request.Context(), challenge, csrf); err != nil {
		h.failure(c, err)
		return
	}
	username := c.Request.PostForm.Get("username")
	credentials, err := auth.ParseCredentials(username, c.Request.PostForm.Get("password"))
	if err != nil {
		Render(c, http.StatusUnprocessableEntity, views.Login(h.assets, username, csrf, "Enter a valid username and password."))
		return
	}
	issued, err := h.service.Login(c.Request.Context(), auth.LoginRequest{Credentials: credentials,
		ChallengeToken: challenge, CSRFToken: csrf, PreviousSessionToken: cookieValue(c, sessionCookie)})
	if errors.Is(err, auth.ErrCredentials) {
		Render(c, http.StatusUnauthorized, views.Login(h.assets, username, csrf, "Unable to sign in with those credentials."))
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	h.setCookie(c, &http.Cookie{Name: sessionCookie, Value: issued.Token, Expires: issued.ExpiresAt, MaxAge: int(auth.SessionLifetime.Seconds())})
	h.setCookie(c, &http.Cookie{Name: loginCookie, MaxAge: -1, Expires: time.Unix(1, 0)})
	c.Redirect(http.StatusSeeOther, "/account")
}

func (h *Auth) account(c *gin.Context) {
	session, err := h.service.Session(c.Request.Context(), cookieValue(c, sessionCookie))
	if errors.Is(err, auth.ErrSession) {
		c.Redirect(http.StatusSeeOther, "/login")
		return
	}
	if err != nil {
		h.failure(c, err)
		return
	}
	h.setReplacement(c, session.Replacement)
	Render(c, http.StatusOK, views.Account(h.assets, session.Principal.Username, session.CSRFToken))
}

func (h *Auth) setReplacement(c *gin.Context, replacement *auth.IssuedSession) {
	if replacement == nil {
		return
	}
	h.setCookie(c, &http.Cookie{Name: sessionCookie, Value: replacement.Token, Expires: replacement.ExpiresAt})
}

func (h *Auth) logout(c *gin.Context) {
	if err := h.service.Logout(c.Request.Context(), cookieValue(c, sessionCookie), csrfValue(c)); err != nil {
		h.failure(c, err)
		return
	}
	h.setCookie(c, &http.Cookie{Name: sessionCookie, MaxAge: -1, Expires: time.Unix(1, 0)})
	c.Redirect(http.StatusSeeOther, "/login")
}
