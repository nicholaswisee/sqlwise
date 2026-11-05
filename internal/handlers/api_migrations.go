package handlers

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/migration"
	"sqlwise/internal/schemadiff"
	"sqlwise/internal/web/views"
)

type apiDraft struct {
	ID             string            `json:"id"`
	DatabaseID     string            `json:"database_id"`
	Title          string            `json:"title"`
	SQL            string            `json:"sql"`
	SQLHash        string            `json:"sql_hash"`
	Dialect        migration.Dialect `json:"dialect"`
	Revision       int64             `json:"revision"`
	AppliedVersion int64             `json:"applied_version"`
	LastStatus     migration.Status  `json:"last_status"`
	CreatedBy      string            `json:"created_by"`
	CreatedByName  string            `json:"created_by_name"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedBy      string            `json:"updated_by"`
	UpdatedByName  string            `json:"updated_by_name"`
	UpdatedAt      time.Time         `json:"updated_at"`
}
type apiAttempt struct {
	ID               string            `json:"id"`
	Seq              int64             `json:"sequence"`
	DatabaseID       string            `json:"database_id"`
	MigrationID      string            `json:"migration_id"`
	MigrationTitle   string            `json:"migration_title"`
	DraftRevision    int64             `json:"draft_revision"`
	CandidateVersion int64             `json:"candidate_version"`
	AppliedVersion   int64             `json:"applied_version"`
	SQL              string            `json:"sql"`
	SQLHash          string            `json:"sql_hash"`
	Dialect          migration.Dialect `json:"dialect"`
	StatementCount   int               `json:"statement_count"`
	ExecutorName     string            `json:"executor_name"`
	Status           migration.Status  `json:"status"`
	Error            string            `json:"error,omitempty"`
	FailedStatement  int               `json:"failed_statement,omitempty"`
	StartedAt        time.Time         `json:"started_at"`
	FinishedAt       *time.Time        `json:"finished_at,omitempty"`
	ReconciledAt     *time.Time        `json:"reconciled_at,omitempty"`
}

func apiDraftDTO(d migration.Draft) apiDraft {
	return apiDraft{d.ID, d.DatabaseID, d.Title, d.SQL, d.SQLHash(), d.Dialect, d.Revision, d.AppliedVersion, d.LastStatus, d.CreatedBy, d.CreatedByName, d.CreatedAt, d.UpdatedBy, d.UpdatedByName, d.UpdatedAt}
}
func apiAttemptDTO(a migration.Attempt) apiAttempt {
	return apiAttempt{a.ID, a.Seq, a.DatabaseID, a.MigrationID, a.MigrationTitle, a.DraftRevision, a.CandidateVersion, a.AppliedVersion, a.SQL, a.SQLHash, a.Dialect, a.StatementCount, a.ExecutorName, a.Status, a.Error, a.FailedStatement, a.StartedAt, a.FinishedAt, a.ReconciledAt}
}

func (h *Migrations) RegisterAPI(router *gin.Engine) {
	db := router.Group("/api/v1/databases/:databaseID", h.auth.RequireAPISession())
	view := db.Group("", h.auth.RequireAPIDatabasePermission(h.policy, "databaseID", access.View))
	edit := db.Group("", h.auth.RequireAPIDatabasePermission(h.policy, "databaseID", access.Edit))
	view.GET("/migrations", h.apiHistory)
	edit.POST("/migrations", h.apiCreate)
	view.GET("/migrations/:migrationID", h.apiDetail)
	edit.PATCH("/migrations/:migrationID", h.apiUpdate)
	edit.POST("/migrations/:migrationID/execute", h.apiExecute)
	view.GET("/migrations/:migrationID/attempts/:attemptID", h.apiAttempt)
	edit.POST("/migrations/:migrationID/attempts/:attemptID/reconcile", h.apiReconcile)
	view.POST("/schema-diff/preview", h.apiPreview)
	edit.POST("/schema-diff/draft", h.apiUseAsDraft)
}
func (h *Migrations) apiHistory(c *gin.Context) {
	v, e := h.migrations.History(c.Request.Context(), c.Param("databaseID"))
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	drafts := make([]apiDraft, 0, len(v.Drafts))
	for _, d := range v.Drafts {
		drafts = append(drafts, apiDraftDTO(d))
	}
	attempts := make([]apiAttempt, 0, len(v.Attempts))
	for _, a := range v.Attempts {
		attempts = append(attempts, apiAttemptDTO(a))
	}
	w := apiWorkspace{ID: v.Database.ID, Name: v.Database.Name, CanEdit: v.Database.CanEdit}
	if h.header != nil {
		var track []views.TrackStop
		w.Engine, track, w.TrackEarlier = h.header(c.Request.Context(), w.ID)
		w.VersionTrack = apiTrack(track)
		apiWorkspaceEngine(&w)
	}
	w.CurrentVersion = v.CurrentVersion
	apiData(c, gin.H{"workspace": w, "current_version": v.CurrentVersion, "drafts": drafts, "attempts": attempts})
}
func (h *Migrations) apiCreate(c *gin.Context) {
	var in struct {
		Title string `json:"title"`
		SQL   string `json:"sql"`
	}
	if !apiDecode(c, &in) {
		return
	}
	d, e := h.migrations.Create(c.Request.Context(), c.Param("databaseID"), migration.DraftInput{Title: in.Title, SQL: in.SQL})
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	c.Header("Location", "/api/v1/databases/"+d.DatabaseID+"/migrations/"+d.ID)
	c.JSON(http.StatusCreated, gin.H{"data": apiDraftDTO(d)})
}
func (h *Migrations) apiDetail(c *gin.Context) {
	d, e := h.migrations.Detail(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"))
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	attempts := make([]apiAttempt, 0, len(d.Attempts))
	for _, a := range d.Attempts {
		attempts = append(attempts, apiAttemptDTO(a))
	}
	blocked := ""
	if d.Blocked != "" {
		blocked = d.Blocked
	}
	w := apiWorkspace{ID: d.Database.ID, Name: d.Database.Name, CanEdit: d.Database.CanEdit}
	if h.header != nil {
		var track []views.TrackStop
		w.Engine, track, w.TrackEarlier = h.header(c.Request.Context(), w.ID)
		w.VersionTrack = apiTrack(track)
		apiWorkspaceEngine(&w)
	}
	w.CurrentVersion = d.CurrentVersion
	apiData(c, gin.H{"workspace": w, "current_version": d.CurrentVersion, "draft": apiDraftDTO(d.Draft), "attempts": attempts, "blocked": blocked, "blocked_by": func() *apiAttempt {
		if d.BlockedBy == nil {
			return nil
		}
		v := apiAttemptDTO(*d.BlockedBy)
		return &v
	}()})
}

func apiWorkspaceEngine(w *apiWorkspace) {
	switch w.Engine {
	case database.PostgreSQL.Label():
		w.Dialect = string(database.PostgreSQL)
		c := database.PostgreSQL.Capabilities()
		c.ExplainAnalyze = false // The workspace header has no target sandbox handle.
		w.Capabilities = apiCapabilities{c.ConnectionTest, c.SchemaInspection, c.DataPreview, c.MigrationExecution, c.AtomicMigrations, c.SchemaDiff, c.Explain, c.ExplainAnalyze}
	case database.MySQL.Label():
		w.Dialect = string(database.MySQL)
		c := database.MySQL.Capabilities()
		c.ExplainAnalyze = false // The workspace header has no target sandbox handle.
		w.Capabilities = apiCapabilities{c.ConnectionTest, c.SchemaInspection, c.DataPreview, c.MigrationExecution, c.AtomicMigrations, c.SchemaDiff, c.Explain, c.ExplainAnalyze}
	}
}
func (h *Migrations) apiUpdate(c *gin.Context) {
	var in struct {
		Title            string `json:"title"`
		SQL              string `json:"sql"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if !apiDecode(c, &in) {
		return
	}
	d, e := h.migrations.Update(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"), migration.UpdateInput{ExpectedRevision: in.ExpectedRevision, DraftInput: migration.DraftInput{Title: in.Title, SQL: in.SQL}})
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	apiData(c, apiDraftDTO(d))
}
func (h *Migrations) apiExecute(c *gin.Context) {
	var in struct {
		ExpectedRevision int64  `json:"expected_revision"`
		ExpectedVersion  int64  `json:"expected_version"`
		ExpectedSQLHash  string `json:"expected_sql_hash"`
	}
	if !apiDecode(c, &in) {
		return
	}
	a, e := h.migrations.Execute(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"), migration.ExecuteInput{ExpectedRevision: in.ExpectedRevision, ExpectedVersion: in.ExpectedVersion, ExpectedSQLHash: in.ExpectedSQLHash})
	if e != nil {
		if a.ID != "" {
			status, code, message := apiMigrationError(e)
			c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": code, "message": message, "fields": gin.H{}, "attempt": apiAttemptDTO(a)}})
			return
		}
		apiMigrationFailure(c, e)
		return
	}
	apiData(c, apiAttemptDTO(a))
}
func (h *Migrations) apiAttempt(c *gin.Context) {
	_, a, e := h.migrations.Attempt(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"), c.Param("attemptID"))
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	apiData(c, apiAttemptDTO(a))
}
func (h *Migrations) apiReconcile(c *gin.Context) {
	a, e := h.migrations.MarkReconciled(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"), c.Param("attemptID"))
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	apiData(c, apiAttemptDTO(a))
}
func (h *Migrations) apiPreview(c *gin.Context) {
	var in struct {
		Desired string `json:"desired"`
	}
	if !apiDecode(c, &in) {
		return
	}
	p, e := h.diff.Preview(c.Request.Context(), c.Param("databaseID"), in.Desired)
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	apiData(c, apiProposal(p))
}
func (h *Migrations) apiUseAsDraft(c *gin.Context) {
	var in struct {
		Desired           string `json:"desired"`
		SourceFingerprint string `json:"source_fingerprint"`
		Title             string `json:"title"`
	}
	if !apiDecode(c, &in) {
		return
	}
	d, e := h.diff.UseAsDraft(c.Request.Context(), c.Param("databaseID"), in.Desired, in.SourceFingerprint, in.Title)
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	c.Header("Location", "/api/v1/databases/"+d.DatabaseID+"/migrations/"+d.ID)
	c.JSON(http.StatusCreated, gin.H{"data": apiDraftDTO(d)})
}

type apiDiffChange struct {
	Text        string `json:"text"`
	Destructive bool   `json:"destructive"`
}
type apiDiffProposal struct {
	Schema            string          `json:"schema"`
	SourceFingerprint string          `json:"source_fingerprint"`
	Warnings          []string        `json:"warnings"`
	SQL               string          `json:"sql"`
	RequiresReview    bool            `json:"requires_review"`
	Changes           []apiDiffChange `json:"changes"`
}

func apiProposal(p schemadiff.Proposal) apiDiffProposal {
	v := apiDiffProposal{Schema: p.Schema, SourceFingerprint: p.SourceFingerprint, Warnings: p.Warnings, SQL: p.SQL, RequiresReview: p.RequiresReview, Changes: []apiDiffChange{}}
	for _, c := range p.Changes {
		v.Changes = append(v.Changes, apiDiffChange{c.String(), c.Destructive})
	}
	return v
}

func apiMigrationFailure(c *gin.Context, err error) {
	status, code, message := apiMigrationError(err)
	apiError(c, status, code, message)
}

func apiMigrationError(err error) (int, string, string) {
	if errors.Is(err, auth.ErrSession) {
		return http.StatusUnauthorized, "session_expired", "Your session has expired."
	}
	if errors.Is(err, auth.ErrCSRF) {
		return http.StatusForbidden, "csrf_invalid", "Request verification failed."
	}
	if errors.Is(err, migration.ErrNotFound) {
		return http.StatusNotFound, "not_found", migration.Message(err)
	}
	if errors.Is(err, migration.ErrValidation) || errors.Is(err, migration.ErrUnsupportedDialect) || errors.Is(err, migration.ErrExecutionFailed) {
		return http.StatusUnprocessableEntity, "validation", migration.Message(err)
	}
	if errors.Is(err, migration.ErrConflict) || errors.Is(err, migration.ErrBusy) || errors.Is(err, migration.ErrRecoveryRequired) {
		return http.StatusConflict, "conflict", migration.Message(err)
	}
	if errors.Is(err, migration.ErrTimeout) {
		return http.StatusGatewayTimeout, "timeout", migration.Message(err)
	}
	if errors.Is(err, migration.ErrTargetUnavailable) || errors.Is(err, migration.ErrOutcomeUnknown) {
		return http.StatusServiceUnavailable, "target_unavailable", migration.Message(err)
	}
	switch {
	case errors.Is(err, auth.ErrAuthorizationEpoch):
		return http.StatusConflict, "authorization_stale", "Your permissions changed. Refresh and try again."
	case errors.Is(err, access.ErrPermissionDenied):
		return http.StatusForbidden, "permission_denied", "You do not have permission to perform this action."
	case errors.Is(err, migration.ErrBusy):
		return http.StatusConflict, "conflict", migration.Message(err)
	}
	return http.StatusServiceUnavailable, "unavailable", "The service is temporarily unavailable."
}
