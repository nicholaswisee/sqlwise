package handlers

import (
	"errors"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/web/views"
)

type DatabaseViewer struct {
	auth     *Auth
	policy   *access.Service
	resolver database.Resolver
	assets   views.Assets
}

func NewDatabaseViewer(authHandler *Auth, policy *access.Service, resolver database.Resolver, assets views.Assets) *DatabaseViewer {
	return &DatabaseViewer{auth: authHandler, policy: policy, resolver: resolver, assets: assets}
}

func (h *DatabaseViewer) Register(router *gin.Engine) {
	router.GET("/databases", h.auth.RequireSession(), h.list)
	view := router.Group("/databases/:databaseID", h.auth.RequireSession(), requireDatabasePermissionWithFailure(h.auth.service.ValidateContextFresh, h.policy, "databaseID", access.View, h.fail))
	view.GET("", h.explorer)
	view.GET("/tables/:tableID", h.table)
}

func (h *DatabaseViewer) list(c *gin.Context) {
	session := c.MustGet("auth_session").(auth.Session)
	databases, err := h.policy.ListDatabases(c.Request.Context(), session.Principal.UserID)
	if err != nil {
		h.fail(c, err)
		return
	}
	data := views.DatabaseListData{Username: session.Principal.Username}
	for _, db := range databases {
		data.Databases = append(data.Databases, views.DatabaseListRow{ID: db.ID, Name: db.Name, Permission: db.Permission.String()})
	}
	Render(c, http.StatusOK, views.DatabasesPage(h.assets, data))
}

func (h *DatabaseViewer) nav(c *gin.Context, active string) (views.DatabaseNav, error) {
	session := c.MustGet("auth_session").(auth.Session)
	id := c.Param("databaseID")
	databases, err := h.policy.ListDatabases(c.Request.Context(), session.Principal.UserID)
	if err != nil {
		return views.DatabaseNav{}, err
	}
	i := slices.IndexFunc(databases, func(db access.DatabasePermission) bool { return db.ID == id })
	if i < 0 {
		return views.DatabaseNav{}, access.ErrPermissionDenied
	}
	return views.DatabaseNav{ID: id, Name: databases[i].Name, CanEdit: databases[i].Permission == access.Edit, Username: session.Principal.Username, CSRFToken: session.CSRFToken, Active: active}, nil
}

func (h *DatabaseViewer) open(c *gin.Context) (database.Handle, error) {
	return h.resolver.Open(c.Request.Context(), c.Param("databaseID"))
}

func (h *DatabaseViewer) explorer(c *gin.Context) {
	nav, err := h.nav(c, "schema")
	if err != nil {
		h.fail(c, err)
		return
	}
	handle, err := h.open(c)
	if err != nil {
		h.fail(c, err)
		return
	}
	defer handle.Close()
	inspector, err := database.As[database.SchemaInspector](handle)
	if err != nil {
		h.fail(c, err)
		return
	}
	schemas, err := inspector.Schemas(c.Request.Context())
	if err != nil {
		h.fail(c, err)
		return
	}
	schema := c.Query("schema")
	if schema == "" && len(schemas) > 0 {
		schema = schemas[0]
		if slices.Contains(schemas, "public") {
			schema = "public"
		}
	}
	if !slices.Contains(schemas, schema) {
		h.fail(c, &database.Error{Kind: database.ErrNotFound, Message: "Schema not found."})
		return
	}
	snapshot, err := inspector.InspectSchema(c.Request.Context(), schema)
	if err != nil {
		h.fail(c, err)
		return
	}
	data := views.SchemaExplorerData{Nav: nav, Schemas: schemas, Schema: schema}
	for _, t := range snapshot.Tables {
		table := views.SchemaTable{ID: t.Ref().ID(), Name: t.Name}
		for _, col := range t.Columns {
			column := views.SchemaColumn{Name: col.Name, Type: col.Type, Nullable: col.Nullable, PrimaryKey: slices.Contains(t.PrimaryKey, col.Name)}
			if col.Default != nil {
				column.Default, column.HasDefault = *col.Default, true
			}
			table.Columns = append(table.Columns, column)
		}
		for _, constraint := range t.Constraints {
			table.Constraints = append(table.Constraints, views.SchemaDefinition{Name: constraint.Name, Kind: string(constraint.Kind), Definition: constraint.Definition})
		}
		for _, index := range t.Indexes {
			kind := "index"
			if index.Unique {
				kind = "unique index"
			}
			table.Indexes = append(table.Indexes, views.SchemaDefinition{Name: index.Name, Kind: kind, Definition: index.Definition})
		}
		data.Tables = append(data.Tables, table)
	}
	if c.GetHeader("HX-Request") == "true" {
		Render(c, http.StatusOK, views.SchemaExplorerFragment(data))
		return
	}
	Render(c, http.StatusOK, views.SchemaExplorer(h.assets, data))
}

func (h *DatabaseViewer) table(c *gin.Context) {
	ref, err := database.ParseTableID(c.Param("tableID"))
	if err != nil {
		h.fail(c, err)
		return
	}
	offset, limit, err := database.ParsePage(c.Query("offset"), c.Query("limit"))
	if err != nil {
		h.fail(c, err)
		return
	}
	nav, err := h.nav(c, "schema")
	if err != nil {
		h.fail(c, err)
		return
	}
	handle, err := h.open(c)
	if err != nil {
		h.fail(c, err)
		return
	}
	defer handle.Close()
	reader, err := database.As[database.TableReader](handle)
	if err != nil {
		h.fail(c, err)
		return
	}
	page, err := reader.ReadTable(c.Request.Context(), database.TableRequest{Table: ref, Offset: offset, Limit: limit})
	if err != nil {
		h.fail(c, err)
		return
	}
	data := views.TablePageData{Nav: nav, Schema: ref.Schema, Table: ref.Name, From: offset + 1, To: offset + len(page.Rows), Ordered: page.Ordered, Truncated: page.Truncated}
	for _, col := range page.Columns {
		data.Columns = append(data.Columns, views.TableColumn{Name: col.Name, Type: col.Type})
	}
	for _, row := range page.Rows {
		cells := make([]views.TableCell, len(row))
		for i, cell := range row {
			cells[i] = views.TableCell{Text: cell.Text, Null: cell.Null, Binary: cell.Binary, Truncated: cell.Truncated}
		}
		data.Rows = append(data.Rows, cells)
	}
	id := c.Param("databaseID")
	if offset > 0 {
		data.PrevURL = views.TableURL(id, c.Param("tableID"), max(0, offset-limit), limit)
	}
	if page.HasNext {
		data.NextURL = views.TableURL(id, c.Param("tableID"), offset+len(page.Rows), limit)
	}
	if c.GetHeader("HX-Request") == "true" {
		Render(c, http.StatusOK, views.TablePageFragment(data))
		return
	}
	Render(c, http.StatusOK, views.TableData(h.assets, data))
}

func (h *DatabaseViewer) fail(c *gin.Context, err error) {
	c.Abort()
	switch {
	case errors.Is(err, auth.ErrSession):
		h.auth.rejectSession(c)
		return
	case errors.Is(err, auth.ErrAuthorizationEpoch):
		Render(c, http.StatusForbidden, views.DatabaseError(h.assets, "Your access changed. Reload the page to continue."))
		return
	case errors.Is(err, access.ErrPermissionDenied):
		Render(c, http.StatusForbidden, views.DatabaseError(h.assets, "You do not have permission to view this database."))
		return
	}
	status, message := http.StatusInternalServerError, "Something went wrong. Please try again."
	var domain *database.Error
	if errors.As(err, &domain) {
		status, message = connectionError(err)
	}
	if c.Request.Context().Err() != nil {
		c.Status(status)
		return
	}
	Render(c, status, views.DatabaseError(h.assets, message))
}
