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
)

type apiConnectionRow struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	DBMS     string `json:"dbms"`
	Address  string `json:"address"`
	Username string `json:"username"`
	TLS      string `json:"tls"`
	Active   bool   `json:"active"`
}
type apiConnectionForm struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	DBMS     string `json:"dbms"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	TLS      string `json:"tls"`
	Active   bool   `json:"active"`
	Existing bool   `json:"existing"`
	Sandbox  bool   `json:"sandbox"`
}
type apiConnectionCapability struct {
	Name      string `json:"name"`
	Supported bool   `json:"supported"`
}
type apiConnectionTest struct {
	Ran     bool   `json:"ran"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}
type apiConnectionList struct {
	Connections []apiConnectionRow `json:"connections"`
}
type apiConnectionDetail struct {
	Form         apiConnectionForm         `json:"form"`
	Capabilities []apiConnectionCapability `json:"capabilities"`
	ReceiptSQL   string                    `json:"receipt_sql,omitempty"`
	Test         *apiConnectionTest        `json:"test,omitempty"`
}

func (h *ConnectionAdmin) RegisterAPI(router *gin.Engine) {
	a := router.Group("/api/v1/admin", h.auth.RequireAPISession(), h.auth.RequireAPIPlatformAdmin())
	a.GET("/databases", h.apiList)
	a.POST("/databases", h.apiCreate)
	a.GET("/databases/:id", h.apiDetail)
	a.POST("/databases/:id", h.apiUpdate)
	a.POST("/databases/:id/test", h.apiTest)
	a.POST("/databases/:id/active", h.apiSetActive)
}

func (h *ConnectionAdmin) apiList(c *gin.Context) {
	conns, err := h.store.List(c.Request.Context())
	if err != nil {
		apiConnectionFailure(c, err)
		return
	}
	out := apiConnectionList{Connections: []apiConnectionRow{}}
	for _, x := range conns {
		out.Connections = append(out.Connections, apiConnectionRow{x.DatabaseID, x.Name, x.DBMS.Label(), x.Host + ":" + strconv.Itoa(x.Port) + "/" + x.Database, x.Username, string(x.TLS), x.Active})
	}
	apiData(c, out)
}
func (h *ConnectionAdmin) apiDetail(c *gin.Context) {
	d, err := h.apiDetailData(c, c.Param("id"))
	if err != nil {
		apiConnectionFailure(c, err)
		return
	}
	apiData(c, d)
}

func apiConnectionFailure(c *gin.Context, err error) {
	status, _ := connectionError(err)
	code, message := "unavailable", "The connection request could not be completed."
	switch status {
	case http.StatusUnprocessableEntity:
		code, message = "validation", "Check the submitted connection values and try again."
	case http.StatusConflict:
		code, message = "conflict", "A connection with that name already exists."
	case http.StatusNotFound:
		code, message = "not_found", "Connection not found."
	case http.StatusGatewayTimeout:
		code, message = "timeout", "The connection timed out."
	case http.StatusServiceUnavailable:
		code, message = "unavailable", "The connection is temporarily unavailable."
	}
	if errors.Is(err, access.ErrPermissionDenied) {
		status, code, message = http.StatusForbidden, "permission_denied", "You do not have permission to perform this action."
	}
	if errors.Is(err, auth.ErrAuthorizationEpoch) {
		status, code, message = http.StatusConflict, "authorization_stale", "Your permissions changed. Refresh and try again."
	}
	apiError(c, status, code, message)
}
func (h *ConnectionAdmin) apiDetailData(c *gin.Context, id string) (apiConnectionDetail, error) {
	x, err := h.store.Get(c.Request.Context(), id)
	if err != nil {
		return apiConnectionDetail{}, err
	}
	d := apiConnectionDetail{Form: apiConnectionForm{ID: id, Name: x.Name, DBMS: string(x.DBMS), Host: x.Host, Port: strconv.Itoa(x.Port), Database: x.Database, Username: x.Username, TLS: string(x.TLS), Active: x.Active, Existing: true, Sandbox: x.Sandbox}, Capabilities: []apiConnectionCapability{}}
	caps := x.DBMS.Capabilities()
	caps.ExplainAnalyze = caps.Explain && x.Sandbox
	for _, v := range caps.List() {
		d.Capabilities = append(d.Capabilities, apiConnectionCapability{v.Name, v.Supported})
	}
	if x.DBMS == database.PostgreSQL {
		d.ReceiptSQL = database.ReceiptSetupSQL(x.Username)
	}
	return d, nil
}

type apiConnectionInput struct {
	Name     string `json:"name"`
	DBMS     string `json:"dbms"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
	TLS      string `json:"tls"`
	Sandbox  bool   `json:"sandbox"`
}

func (in apiConnectionInput) connection() (database.Connection, error) {
	port, err := strconv.Atoi(in.Port)
	if err != nil {
		return database.Connection{}, errInvalidPort
	}
	x := database.Connection{DBMS: database.DBMS(in.DBMS), Host: strings.TrimSpace(in.Host), Port: port, Database: in.Database, Username: in.Username, Password: in.Password, TLS: database.TLSMode(in.TLS), Sandbox: in.Sandbox}
	return x, x.Validate()
}
func (h *ConnectionAdmin) apiCreate(c *gin.Context) {
	var in apiConnectionInput
	if !apiDecode(c, &in) {
		return
	}
	x, err := in.connection()
	var reg access.DatabaseRegistration
	if err == nil {
		reg, err = h.policy.RegisterDatabase(c.Request.Context(), in.Name, func(tx pgx.Tx, id string) error { return h.store.Insert(c.Request.Context(), tx, id, x) })
	}
	if err != nil {
		apiConnectionFailure(c, err)
		return
	}
	if !h.apiFresh(c) {
		return
	}
	d, err := h.apiDetailData(c, reg.ID)
	if err != nil {
		apiConnectionFailure(c, err)
		return
	}
	c.Header("Location", "/api/v1/admin/databases/"+reg.ID)
	c.JSON(http.StatusCreated, gin.H{"data": d})
}
func (h *ConnectionAdmin) apiUpdate(c *gin.Context) {
	id := c.Param("id")
	var in apiConnectionInput
	if !apiDecode(c, &in) {
		return
	}
	x, err := in.connection()
	if err == nil {
		err = h.policy.UpdateDatabase(c.Request.Context(), id, in.Name, func(tx pgx.Tx) error { return h.store.Update(c.Request.Context(), tx, id, x) })
	}
	if err != nil {
		apiConnectionFailure(c, err)
		return
	}
	h.pools.Invalidate(id)
	if !h.apiFresh(c) {
		return
	}
	d, err := h.apiDetailData(c, id)
	if err != nil {
		apiConnectionFailure(c, err)
		return
	}
	apiData(c, d)
}
func (h *ConnectionAdmin) apiSetActive(c *gin.Context) {
	var in struct {
		Active *bool `json:"active"`
	}
	if !apiDecode(c, &in) {
		return
	}
	if in.Active == nil {
		apiError(c, 422, "invalid_input", "Active must be true or false.")
		return
	}
	id := c.Param("id")
	err := h.policy.SetDatabaseActive(c.Request.Context(), id, *in.Active)
	if err != nil {
		apiConnectionFailure(c, err)
		return
	}
	h.pools.Invalidate(id)
	if !h.apiFresh(c) {
		return
	}
	d, err := h.apiDetailData(c, id)
	if err != nil {
		apiConnectionFailure(c, err)
		return
	}
	apiData(c, d)
}

func (h *ConnectionAdmin) apiFresh(c *gin.Context) bool {
	if _, err := h.auth.service.ValidateContextFresh(c.Request.Context()); err != nil {
		apiFailure(c, err)
		return false
	}
	return true
}
func (h *ConnectionAdmin) apiTest(c *gin.Context) {
	id := c.Param("id")
	result := apiConnectionTest{Ran: true, OK: true, Message: "Connection succeeded."}
	handle, err := h.pools.Open(c.Request.Context(), id)
	if err == nil {
		err = handle.Ping(c.Request.Context())
		handle.Close()
	}
	if err != nil {
		result.OK = false
		result.Message = database.Message(err)
		if result.Message == "" {
			result.Message = "The connection could not be tested."
		}
	}
	d, loadErr := h.apiDetailData(c, id)
	if loadErr != nil {
		apiConnectionFailure(c, loadErr)
		return
	}
	d.Test = &result
	apiData(c, d)
}
