package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/web/views"
)

type ConnectionAdmin struct {
	*AccessAdmin
	store *database.Store
	pools *database.Pools
}

func NewConnectionAdmin(authHandler *Auth, policy *access.Service, store *database.Store, pools *database.Pools, assets views.Assets) *ConnectionAdmin {
	return &ConnectionAdmin{AccessAdmin: NewAccessAdmin(authHandler, policy, assets), store: store, pools: pools}
}

func (h *ConnectionAdmin) Register(router *gin.Engine) {
	admin := router.Group("", h.auth.RequireSession(), h.auth.RequirePlatformAdmin())
	admin.GET("/admin/databases", h.list)
	admin.POST("/admin/databases", h.create)
	admin.GET("/admin/databases/:id", h.edit)
	admin.POST("/admin/databases/:id", h.update)
	admin.POST("/admin/databases/:id/test", h.test)
	admin.POST("/admin/databases/:id/active", h.setActive)
}

func (h *ConnectionAdmin) baseData(c *gin.Context) views.ConnectionPageData {
	session := c.MustGet("auth_session").(auth.Session)
	data := views.ConnectionPageData{CSRFToken: session.CSRFToken, Username: session.Principal.Username}
	if c.Query("notice") == "saved" {
		data.Message = "Changes saved successfully."
	}
	return data
}

func (h *ConnectionAdmin) listData(c *gin.Context, form views.ConnectionForm) (views.ConnectionPageData, error) {
	data := h.baseData(c)
	connections, err := h.store.List(c.Request.Context())
	if err != nil {
		return data, err
	}
	for _, conn := range connections {
		data.Connections = append(data.Connections, views.ConnectionRow{ID: conn.DatabaseID, Name: conn.Name, DBMS: conn.DBMS.Label(), Address: conn.Host + ":" + strconv.Itoa(conn.Port) + "/" + conn.Database, Username: conn.Username, TLS: string(conn.TLS), Active: conn.Active})
	}
	data.Form = form
	return data, nil
}

func (h *ConnectionAdmin) detailData(c *gin.Context, id string) (views.ConnectionPageData, error) {
	data := h.baseData(c)
	conn, err := h.store.Get(c.Request.Context(), id)
	if err != nil {
		return data, err
	}
	data.Form = views.ConnectionForm{ID: id, Name: conn.Name, DBMS: string(conn.DBMS), Host: conn.Host, Port: strconv.Itoa(conn.Port), Database: conn.Database, Username: conn.Username, TLS: string(conn.TLS), Active: conn.Active, Existing: true}
	for _, capability := range conn.DBMS.Capabilities().List() {
		data.Capabilities = append(data.Capabilities, views.ConnectionCapability{Name: capability.Name, Supported: capability.Supported})
	}
	if conn.DBMS == database.PostgreSQL {
		data.ReceiptSQL = database.ReceiptSetupSQL(conn.Username)
	}
	return data, nil
}

func (h *ConnectionAdmin) list(c *gin.Context) {
	data, err := h.listData(c, views.ConnectionForm{Port: "5432", TLS: string(database.TLSVerifyFull)})
	if err != nil {
		h.fail(c, err)
		return
	}
	h.renderList(c, http.StatusOK, data)
}

func (h *ConnectionAdmin) edit(c *gin.Context) {
	data, err := h.detailData(c, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	h.renderDetail(c, http.StatusOK, data)
}

func (h *ConnectionAdmin) renderList(c *gin.Context, status int, data views.ConnectionPageData) {
	if c.GetHeader("HX-Request") == "true" {
		Render(c, status, views.ConnectionsFragment(data))
		return
	}
	Render(c, status, views.ConnectionsPage(h.assets, data))
}

func (h *ConnectionAdmin) renderDetail(c *gin.Context, status int, data views.ConnectionPageData) {
	if c.GetHeader("HX-Request") == "true" {
		Render(c, status, views.ConnectionDetailFragment(data))
		return
	}
	Render(c, status, views.ConnectionEditPage(h.assets, data))
}

func connectionInput(c *gin.Context) (string, database.Connection, views.ConnectionForm, error) {
	form := views.ConnectionForm{
		Name: c.PostForm("name"), DBMS: c.PostForm("dbms"), Host: strings.TrimSpace(c.PostForm("host")), Port: strings.TrimSpace(c.PostForm("port")),
		Database: c.PostForm("database"), Username: c.PostForm("username"), TLS: c.PostForm("tls"),
	}
	conn := database.Connection{DBMS: database.DBMS(form.DBMS), Host: form.Host, Database: form.Database, Username: form.Username, Password: c.PostForm("password"), TLS: database.TLSMode(form.TLS)}
	port, err := strconv.Atoi(form.Port)
	if err != nil {
		return form.Name, conn, form, errInvalidPort
	}
	conn.Port = port
	return form.Name, conn, form, conn.Validate()
}

var errInvalidPort = &database.Error{Kind: database.ErrValidation, Message: "Port must be from 1 to 65535."}

func (h *ConnectionAdmin) create(c *gin.Context) {
	name, conn, form, err := connectionInput(c)
	var registration access.DatabaseRegistration
	if err == nil {
		registration, err = h.policy.RegisterDatabase(c.Request.Context(), name, func(tx pgx.Tx, databaseID string) error {
			return h.store.Insert(c.Request.Context(), tx, databaseID, conn)
		})
	}
	if err != nil {
		if h.boundaryFailure(c, err) {
			return
		}
		status, problem := connectionError(err)
		data, loadErr := h.listData(c, form)
		if loadErr != nil {
			h.fail(c, loadErr)
			return
		}
		data.Error = problem
		h.renderList(c, status, data)
		return
	}
	if h.afterMutation(c) {
		return
	}
	redirectAfterAdminMutation(c, connectionURL(registration.ID)+"?notice=saved")
}

func (h *ConnectionAdmin) update(c *gin.Context) {
	id := c.Param("id")
	name, conn, form, err := connectionInput(c)
	if err == nil {
		err = h.policy.UpdateDatabase(c.Request.Context(), id, name, func(tx pgx.Tx) error {
			return h.store.Update(c.Request.Context(), tx, id, conn)
		})
	}
	if err == nil {
		h.pools.Invalidate(id)
	}
	h.detailResult(c, id, &form, err)
}

func (h *ConnectionAdmin) setActive(c *gin.Context) {
	id := c.Param("id")
	active, err := parseActive(c.PostForm("active"))
	if err != nil {
		err = &database.Error{Kind: database.ErrValidation, Message: "Choose enable or disable."}
	} else {
		err = h.policy.SetDatabaseActive(c.Request.Context(), id, active)
	}
	if err == nil {
		h.pools.Invalidate(id)
	}
	h.detailResult(c, id, nil, err)
}

func (h *ConnectionAdmin) detailResult(c *gin.Context, id string, form *views.ConnectionForm, err error) {
	if h.boundaryFailure(c, err) {
		return
	}
	if err == nil {
		if h.afterMutation(c) {
			return
		}
		if c.GetHeader("HX-Request") != "true" {
			c.Redirect(http.StatusSeeOther, connectionURL(id)+"?notice=saved")
			return
		}
	}
	data, loadErr := h.detailData(c, id)
	if loadErr != nil {
		h.fail(c, loadErr)
		return
	}
	status := http.StatusOK
	if err != nil {
		status, data.Error = connectionError(err)
		if form != nil {
			form.ID, form.Active, form.Existing = id, data.Form.Active, true
			data.Form = *form
		}
	} else {
		data.Message = "Changes saved successfully."
	}
	h.renderDetail(c, status, data)
}

func (h *ConnectionAdmin) afterMutation(c *gin.Context) bool {
	_, err := h.auth.service.ValidateContextFresh(c.Request.Context())
	switch {
	case errors.Is(err, auth.ErrAuthorizationEpoch):
		redirectAfterAdminMutation(c, "/account")
	case errors.Is(err, auth.ErrSession):
		redirectAfterAdminMutation(c, "/login")
	default:
		return false
	}
	return true
}

func (h *ConnectionAdmin) test(c *gin.Context) {
	id := c.Param("id")
	result := views.ConnectionTest{Ran: true, OK: true, Message: "Connection succeeded."}
	handle, err := h.pools.Open(c.Request.Context(), id)
	if err == nil {
		err = handle.Ping(c.Request.Context())
		handle.Close()
	}
	if err != nil {
		result.OK = false
		if result.Message = database.Message(err); result.Message == "" {
			result.Message = "The connection could not be tested."
		}
	}
	if c.GetHeader("HX-Request") == "true" {
		Render(c, http.StatusOK, views.ConnectionTestResult(result))
		return
	}
	data, loadErr := h.detailData(c, id)
	if loadErr != nil {
		h.fail(c, loadErr)
		return
	}
	data.Test = result
	h.renderDetail(c, http.StatusOK, data)
}

func (h *ConnectionAdmin) fail(c *gin.Context, err error) {
	if h.boundaryFailure(c, err) {
		return
	}
	status, message := connectionError(err)
	Render(c, status, views.AuthError(h.assets, message))
}

func connectionError(err error) (int, string) {
	var domain *database.Error
	if errors.As(err, &domain) {
		switch {
		case errors.Is(err, database.ErrValidation), errors.Is(err, database.ErrUnsupportedDialect):
			return http.StatusUnprocessableEntity, domain.Message
		case errors.Is(err, database.ErrConflict):
			return http.StatusConflict, domain.Message
		case errors.Is(err, database.ErrNotFound):
			return http.StatusNotFound, domain.Message
		case errors.Is(err, database.ErrTimeout):
			return http.StatusGatewayTimeout, domain.Message
		case errors.Is(err, database.ErrConnectionUnavailable):
			return http.StatusServiceUnavailable, domain.Message
		}
	}
	switch {
	case errors.Is(err, access.ErrInvalidInput):
		return http.StatusUnprocessableEntity, "Display name must be 1 to 128 characters."
	case errors.Is(err, access.ErrNotFound):
		return http.StatusNotFound, "Connection not found."
	}
	return adminError(err)
}

func connectionURL(id string) string { return "/admin/databases/" + id }
