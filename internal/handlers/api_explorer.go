package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/queryplan"
	"sqlwise/internal/web/views"
)

type apiDatabase struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Permission string `json:"permission"`
	Engine     string `json:"engine,omitempty"`
}
type apiWorkspace struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	CanEdit        bool            `json:"can_edit"`
	Engine         string          `json:"engine,omitempty"`
	Dialect        string          `json:"dialect,omitempty"`
	Capabilities   apiCapabilities `json:"capabilities"`
	CurrentVersion int64           `json:"current_version"`
	VersionTrack   []apiTrackStop  `json:"version_track"`
	TrackEarlier   int             `json:"track_earlier"`
}
type apiCapabilities struct {
	ConnectionTest     bool `json:"connection_test"`
	SchemaInspection   bool `json:"schema_inspection"`
	DataPreview        bool `json:"data_preview"`
	MigrationExecution bool `json:"migration_execution"`
	AtomicMigrations   bool `json:"atomic_migrations"`
	SchemaDiff         bool `json:"schema_diff"`
	Explain            bool `json:"explain"`
	ExplainAnalyze     bool `json:"explain_analyze"`
}
type apiTrackStop struct {
	Label    string `json:"label"`
	Note     string `json:"note,omitempty"`
	NoteTone string `json:"note_tone,omitempty"`
	State    string `json:"state"`
	Href     string `json:"href"`
	Aria     string `json:"aria"`
}

func apiTrack(stops []views.TrackStop) []apiTrackStop {
	out := make([]apiTrackStop, 0, len(stops))
	for _, s := range stops {
		out = append(out, apiTrackStop{s.Label, s.Note, s.NoteTone, s.State, s.Href, s.Aria})
	}
	return out
}

type apiSchemaColumn struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Nullable   bool    `json:"nullable"`
	Default    *string `json:"default,omitempty"`
	PrimaryKey bool    `json:"primary_key"`
}
type apiSchemaDefinition struct {
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	Definition string `json:"definition"`
}
type apiSchemaTable struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Columns     []apiSchemaColumn     `json:"columns"`
	Constraints []apiSchemaDefinition `json:"constraints"`
	Indexes     []apiSchemaDefinition `json:"indexes"`
}
type apiCell struct {
	Text      string `json:"text"`
	Null      bool   `json:"null"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
}
type apiPlanNode struct {
	Type        string        `json:"type"`
	Operation   string        `json:"operation,omitempty"`
	Relation    string        `json:"relation,omitempty"`
	Index       string        `json:"index,omitempty"`
	Join        string        `json:"join,omitempty"`
	StartupCost float64       `json:"startup_cost"`
	TotalCost   float64       `json:"total_cost"`
	PlanRows    float64       `json:"plan_rows"`
	ActualTime  *float64      `json:"actual_time,omitempty"`
	ActualRows  *float64      `json:"actual_rows,omitempty"`
	Loops       *float64      `json:"loops,omitempty"`
	Children    []apiPlanNode `json:"children"`
}
type apiPlan struct {
	Dialect       string      `json:"dialect"`
	Mode          string      `json:"mode"`
	SQLHash       string      `json:"sql_hash"`
	Warnings      []string    `json:"warnings"`
	PlanningTime  *float64    `json:"planning_time,omitempty"`
	ExecutionTime *float64    `json:"execution_time,omitempty"`
	Root          apiPlanNode `json:"root"`
}

func (h *DatabaseViewer) RegisterAPI(router *gin.Engine) {
	router.GET("/api/v1/databases", h.auth.RequireAPISession(), h.apiList)
	db := router.Group("/api/v1/databases/:databaseID", h.auth.RequireAPISession(), h.auth.RequireAPIDatabasePermission(h.policy, "databaseID", access.View))
	db.GET("", h.apiExplorer)
	db.GET("/tables/:tableID", h.apiTable)
}

func (h *DatabaseViewer) apiList(c *gin.Context) {
	session := c.MustGet("auth_session").(auth.Session)
	dbs, err := h.policy.ListDatabases(c.Request.Context(), session.Principal.UserID)
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	rows := make([]apiDatabase, 0, len(dbs))
	for _, db := range dbs {
		row := apiDatabase{ID: db.ID, Name: db.Name, Permission: db.Permission.String()}
		if h.resolver != nil {
			if d, e := h.resolver.DBMS(c.Request.Context(), db.ID); e == nil {
				row.Engine = d.Label()
			}
		}
		rows = append(rows, row)
	}
	apiData(c, gin.H{"username": session.Principal.Username, "databases": rows})
}

func (h *DatabaseViewer) apiWorkspace(c *gin.Context) (apiWorkspace, error) {
	id := c.Param("databaseID")
	session := c.MustGet("auth_session").(auth.Session)
	dbs, err := h.policy.ListDatabases(c.Request.Context(), session.Principal.UserID)
	if err != nil {
		return apiWorkspace{}, err
	}
	i := slices.IndexFunc(dbs, func(d access.DatabasePermission) bool { return d.ID == id })
	if i < 0 {
		return apiWorkspace{}, access.ErrPermissionDenied
	}
	w := apiWorkspace{ID: id, Name: dbs[i].Name, CanEdit: dbs[i].Permission == access.Edit}
	if h.resolver != nil {
		if d, e := h.resolver.DBMS(c.Request.Context(), id); e == nil {
			w.Engine = d.Label()
			w.Dialect = string(d)
			cap := d.Capabilities()
			w.Capabilities = apiCapabilities{cap.ConnectionTest, cap.SchemaInspection, cap.DataPreview, cap.MigrationExecution, cap.AtomicMigrations, cap.SchemaDiff, cap.Explain, cap.ExplainAnalyze}
		}
	}
	if h.header != nil {
		var track []views.TrackStop
		w.Engine, track, w.TrackEarlier = h.header(c.Request.Context(), id)
		w.VersionTrack = apiTrack(track)
		for _, stop := range track {
			if stop.State == "current" {
				_, _ = fmt.Sscanf(stop.Label, "v%d", &w.CurrentVersion)
			}
		}
	}
	return w, nil
}

func (h *DatabaseViewer) apiExplorer(c *gin.Context) {
	w, err := h.apiWorkspace(c)
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	handle, err := h.open(c)
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	defer handle.Close()
	inspector, err := database.As[database.SchemaInspector](handle)
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	schemas, err := inspector.Schemas(c.Request.Context())
	if err != nil {
		apiExplorerFailure(c, err)
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
		apiExplorerFailure(c, &database.Error{Kind: database.ErrNotFound, Message: "Schema not found."})
		return
	}
	snapshot, err := inspector.InspectSchema(c.Request.Context(), schema)
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	tables := make([]apiSchemaTable, 0, len(snapshot.Tables))
	for _, t := range snapshot.Tables {
		v := apiSchemaTable{ID: t.Ref().ID(), Name: t.Name, Columns: []apiSchemaColumn{}, Constraints: []apiSchemaDefinition{}, Indexes: []apiSchemaDefinition{}}
		for _, col := range t.Columns {
			v.Columns = append(v.Columns, apiSchemaColumn{Name: col.Name, Type: col.Type, Nullable: col.Nullable, Default: col.Default, PrimaryKey: slices.Contains(t.PrimaryKey, col.Name)})
		}
		for _, x := range t.Constraints {
			v.Constraints = append(v.Constraints, apiSchemaDefinition{x.Name, string(x.Kind), x.Definition})
		}
		for _, x := range t.Indexes {
			k := "index"
			if x.Unique {
				k = "unique index"
			}
			v.Indexes = append(v.Indexes, apiSchemaDefinition{x.Name, k, x.Definition})
		}
		tables = append(tables, v)
	}
	apiData(c, gin.H{"workspace": w, "schemas": schemas, "schema": schema, "tables": tables})
}

func (h *DatabaseViewer) apiTable(c *gin.Context) {
	ref, err := database.ParseTableID(c.Param("tableID"))
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	offset, limit, err := database.ParsePage(c.Query("offset"), c.Query("limit"))
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	w, err := h.apiWorkspace(c)
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	handle, err := h.open(c)
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	defer handle.Close()
	reader, err := database.As[database.TableReader](handle)
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	page, err := reader.ReadTable(c.Request.Context(), database.TableRequest{Table: ref, Offset: offset, Limit: limit})
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	columns := make([]gin.H, 0, len(page.Columns))
	for _, col := range page.Columns {
		columns = append(columns, gin.H{"name": col.Name, "type": col.Type})
	}
	rows := make([][]apiCell, len(page.Rows))
	for i, row := range page.Rows {
		rows[i] = make([]apiCell, len(row))
		for j, cell := range row {
			rows[i][j] = apiCell{cell.Text, cell.Null, cell.Binary, cell.Truncated}
		}
	}
	apiData(c, gin.H{"workspace": w, "schema": ref.Schema, "table": ref.Name, "offset": offset, "limit": limit, "has_next": page.HasNext, "ordered": page.Ordered, "truncated": page.Truncated, "columns": columns, "rows": rows})
}

func apiExplorerFailure(c *gin.Context, err error) {
	var d *database.Error
	if errors.As(err, &d) {
		switch {
		case errors.Is(err, database.ErrNotFound):
			apiError(c, http.StatusNotFound, "not_found", d.Message)
		case errors.Is(err, database.ErrValidation), errors.Is(err, database.ErrUnsupportedStatement):
			apiError(c, http.StatusUnprocessableEntity, "validation", d.Message)
		case errors.Is(err, database.ErrConflict):
			apiError(c, http.StatusConflict, "conflict", d.Message)
		case errors.Is(err, database.ErrTimeout):
			apiError(c, http.StatusGatewayTimeout, "timeout", d.Message)
		default:
			apiError(c, http.StatusServiceUnavailable, "target_unavailable", d.Message)
		}
		return
	}
	apiFailure(c, err)
}

func (h *Explain) RegisterAPI(router *gin.Engine) {
	router.POST("/api/v1/databases/:databaseID/explain", h.auth.RequireAPISession(), h.auth.RequireAPIDatabasePermission(h.policy, "databaseID", access.View), h.apiExplain)
}
func (h *Explain) apiExplain(c *gin.Context) {
	var in struct {
		SQL       string         `json:"sql"`
		Mode      queryplan.Mode `json:"mode"`
		Confirmed bool           `json:"confirmed"`
	}
	if !apiDecode(c, &in) {
		return
	}
	r, err := h.plans.Explain(c.Request.Context(), c.Param("databaseID"), queryplan.Request{SQL: in.SQL, Mode: in.Mode, Confirmed: in.Confirmed})
	if err != nil {
		apiExplorerFailure(c, err)
		return
	}
	apiData(c, apiPlan{Dialect: r.Dialect, Mode: string(r.Mode), SQLHash: r.SQLHash, Warnings: r.Warnings, PlanningTime: r.PlanningTime, ExecutionTime: r.ExecutionTime, Root: apiNode(r.Root)})
}
func apiNode(n queryplan.Node) apiPlanNode {
	v := apiPlanNode{Type: n.Type, Operation: n.Operation, Relation: n.Relation, Index: n.Index, Join: n.Join, StartupCost: n.StartupCost, TotalCost: n.TotalCost, PlanRows: n.PlanRows, ActualTime: n.ActualTime, ActualRows: n.ActualRows, Loops: n.Loops, Children: []apiPlanNode{}}
	for _, c := range n.Children {
		v.Children = append(v.Children, apiNode(c))
	}
	return v
}
