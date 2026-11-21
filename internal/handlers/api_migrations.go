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
	ID                 string            `json:"id"`
	DatabaseID         string            `json:"database_id"`
	Title              string            `json:"title"`
	SQL                string            `json:"sql"`
	SQLDown            string            `json:"sql_down"`
	Irreversible       bool              `json:"irreversible"`
	IrreversibleReason string            `json:"irreversible_reason"`
	DefinitionHash     string            `json:"definition_hash"`
	Executed           bool              `json:"executed"`
	SQLHash            string            `json:"sql_hash"`
	Dialect            migration.Dialect `json:"dialect"`
	Revision           int64             `json:"revision"`
	AppliedVersion     int64             `json:"applied_version"`
	LastStatus         migration.Status  `json:"last_status"`
	CreatedBy          string            `json:"created_by"`
	CreatedByName      string            `json:"created_by_name"`
	CreatedAt          time.Time         `json:"created_at"`
	UpdatedBy          string            `json:"updated_by"`
	UpdatedByName      string            `json:"updated_by_name"`
	UpdatedAt          time.Time         `json:"updated_at"`
}
type apiAttempt struct {
	ID               string            `json:"id"`
	Seq              int64             `json:"sequence"`
	DatabaseID       string            `json:"database_id"`
	MigrationID      string            `json:"migration_id"`
	MigrationTitle   string            `json:"migration_title"`
	Operation        string            `json:"operation"`
	RequestID        string            `json:"request_id,omitempty"`
	CancelsExecution string            `json:"cancels_execution_id,omitempty"`
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
	return apiDraft{d.ID, d.DatabaseID, d.Title, d.SQL, d.SQLDown, d.Irreversible(), d.IrreversibleReason, d.DefinitionHash(), d.Executed(),
		d.SQLHash(), d.Dialect, d.Revision, d.AppliedVersion, d.LastStatus, d.CreatedBy, d.CreatedByName, d.CreatedAt, d.UpdatedBy, d.UpdatedByName, d.UpdatedAt}
}
func apiAttemptDTO(a migration.Attempt) apiAttempt {
	return apiAttempt{a.ID, a.Seq, a.DatabaseID, a.MigrationID, a.MigrationTitle, string(a.Operation), a.RequestID, a.CancelsExecutionID,
		a.DraftRevision, a.CandidateVersion, a.AppliedVersion, a.SQL, a.SQLHash, a.Dialect, a.StatementCount, a.ExecutorName, a.Status, a.Error,
		a.FailedStatement, a.StartedAt, a.FinishedAt, a.ReconciledAt}
}
func apiAttempts(list []migration.Attempt) []apiAttempt {
	out := make([]apiAttempt, 0, len(list))
	for _, a := range list {
		out = append(out, apiAttemptDTO(a))
	}
	return out
}

type apiHeadEntry struct {
	MigrationID string `json:"migration_id"`
	ExecutionID string `json:"execution_id"`
}
type apiHead struct {
	Active  []apiHeadEntry `json:"active"`
	Pending []apiHeadEntry `json:"pending"`
}

func apiHeadDTO(h migration.Head) apiHead {
	v := apiHead{Active: []apiHeadEntry{}, Pending: []apiHeadEntry{}}
	for _, e := range h.Active {
		v.Active = append(v.Active, apiHeadEntry{e.MigrationID, e.ExecutionID})
	}
	for _, e := range h.Pending {
		v.Pending = append(v.Pending, apiHeadEntry{e.MigrationID, e.ExecutionID})
	}
	return v
}

type apiApproval struct {
	GroupID      string     `json:"group_id"`
	GroupName    string     `json:"group_name"`
	Permission   string     `json:"permission"`
	LeaderName   string     `json:"leader_name"`
	LeaderActive bool       `json:"leader_active"`
	Decision     string     `json:"decision"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	CanDecide    bool       `json:"can_decide"`
}
type apiRequest struct {
	ID                 string        `json:"id"`
	DatabaseID         string        `json:"database_id"`
	MigrationID        string        `json:"migration_id"`
	MigrationTitle     string        `json:"migration_title"`
	Operation          string        `json:"operation"`
	DefinitionRevision int64         `json:"definition_revision"`
	DefinitionHash     string        `json:"definition_hash"`
	ExpectedVersion    int64         `json:"expected_version"`
	CancelsExecution   string        `json:"cancels_execution_id,omitempty"`
	RequestedByName    string        `json:"requested_by_name"`
	Status             string        `json:"status"`
	ReviewRound        int           `json:"review_round"`
	Stale              string        `json:"stale,omitempty"`
	ExecutionID        string        `json:"execution_id,omitempty"`
	Approvals          []apiApproval `json:"approvals"`
	Execution          *apiAttempt   `json:"execution"`
	CreatedAt          time.Time     `json:"created_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
}

func apiRequestDTO(r migration.Request) apiRequest {
	v := apiRequest{ID: r.ID, DatabaseID: r.DatabaseID, MigrationID: r.MigrationID, MigrationTitle: r.MigrationTitle, Operation: string(r.Operation),
		DefinitionRevision: r.DefinitionRevision, DefinitionHash: r.DefinitionHash, ExpectedVersion: r.ExpectedVersion, CancelsExecution: r.CancelsExecutionID,
		RequestedByName: r.RequestedByName, Status: string(r.Status), ReviewRound: r.ReviewRound, Stale: r.Stale, ExecutionID: r.ExecutionID,
		Approvals: []apiApproval{}, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	for _, a := range r.Approvals {
		v.Approvals = append(v.Approvals, apiApproval{a.GroupID, a.GroupName, a.Permission, a.LeaderName, a.LeaderActive, a.Decision, a.DecidedBy, a.DecidedAt, a.CanDecide})
	}
	if r.Execution != nil {
		e := apiAttemptDTO(*r.Execution)
		v.Execution = &e
	}
	return v
}

type apiJob struct {
	ID          string `json:"job_id"`
	RequestID   string `json:"request_id"`
	ExecutionID string `json:"execution_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
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
	edit.POST("/migrations/:migrationID/requests", h.apiCreateRequest)
	view.GET("/requests/:requestID", h.apiRequest)
	edit.POST("/requests/:requestID/submit", h.apiSubmitRequest)
	// Leaders with view access decide for their group; the service checks leadership.
	view.POST("/requests/:requestID/approve", h.apiDecide(true))
	view.POST("/requests/:requestID/reject", h.apiDecide(false))
	// GET /jobs/:jobID is served by the jobs API (handlers.Jobs).
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
	committed := v.Committed
	if c.Query("hide_rollback") == "true" {
		committed = migration.HideRollbacks(committed)
	}
	w := apiWorkspace{ID: v.Database.ID, Name: v.Database.Name, CanEdit: v.Database.CanEdit}
	if h.header != nil {
		var track []views.TrackStop
		w.Engine, track, w.TrackEarlier = h.header(c.Request.Context(), w.ID)
		w.VersionTrack = apiTrack(track)
		apiWorkspaceEngine(&w)
	}
	w.CurrentVersion = v.CurrentVersion
	apiData(c, gin.H{"workspace": w, "current_version": v.CurrentVersion, "drafts": drafts, "attempts": apiAttempts(v.Attempts),
		"committed": apiAttempts(committed), "head": apiHeadDTO(v.Head)})
}
func (h *Migrations) apiCreate(c *gin.Context) {
	var in apiDefinition
	if !apiDecode(c, &in) {
		return
	}
	d, e := h.migrations.Create(c.Request.Context(), c.Param("databaseID"), in.input())
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
	requests := make([]apiRequest, 0, len(d.Requests))
	for _, r := range d.Requests {
		requests = append(requests, apiRequestDTO(r))
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
	apiData(c, gin.H{"workspace": w, "current_version": d.CurrentVersion, "draft": apiDraftDTO(d.Draft), "attempts": apiAttempts(d.Attempts),
		"requests": requests, "eligible": append([]migration.Operation{}, d.Eligible...), "head": apiHeadDTO(d.Head), "blocked": blocked, "blocked_by": func() *apiAttempt {
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
		apiDefinition
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !apiDecode(c, &in) {
		return
	}
	d, e := h.migrations.Update(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"), migration.UpdateInput{ExpectedRevision: in.ExpectedRevision, DraftInput: in.input()})
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	apiData(c, apiDraftDTO(d))
}

type apiDefinition struct {
	Title              string `json:"title"`
	SQL                string `json:"sql"`
	SQLDown            string `json:"sql_down"`
	Irreversible       bool   `json:"irreversible"`
	IrreversibleReason string `json:"irreversible_reason"`
}

func (in apiDefinition) input() migration.DraftInput {
	return migration.DraftInput{Title: in.Title, SQL: in.SQL, SQLDown: in.SQLDown, Irreversible: in.Irreversible, IrreversibleReason: in.IrreversibleReason}
}

// apiExecute enqueues an approved request of this migration and returns its
// job at once; the SQL runs in the worker, never in the HTTP request.
func (h *Migrations) apiExecute(c *gin.Context) {
	var in struct {
		RequestID      string `json:"request_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !apiDecode(c, &in) {
		return
	}
	if in.IdempotencyKey == "" || len(in.IdempotencyKey) > 128 {
		apiError(c, http.StatusUnprocessableEntity, "validation", "An idempotency key of at most 128 characters is required.")
		return
	}
	databaseID := c.Param("databaseID")
	r, e := h.migrations.Request(c.Request.Context(), databaseID, in.RequestID)
	if e == nil && r.MigrationID != c.Param("migrationID") {
		e = &migration.Error{Kind: migration.ErrNotFound, Message: "Request not found for this migration."}
	}
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	if h.jobs == nil {
		apiError(c, http.StatusServiceUnavailable, "target_unavailable", "The execution queue is not configured on this server.")
		return
	}
	job, e := h.jobs.Enqueue(c.Request.Context(), databaseID, r.ID, in.IdempotencyKey)
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	c.Header("Location", "/api/v1/databases/"+databaseID+"/jobs/"+job.ID)
	c.JSON(http.StatusAccepted, gin.H{"data": apiJob{job.ID, job.RequestID, job.ExecutionID, job.Status, job.Error}})
}

func (h *Migrations) apiCreateRequest(c *gin.Context) {
	var in struct {
		Operation          string `json:"operation"`
		ExpectedRevision   int64  `json:"expected_revision"`
		ExpectedDefinition string `json:"expected_definition_hash"`
		ExpectedVersion    int64  `json:"expected_version"`
	}
	if !apiDecode(c, &in) {
		return
	}
	r, e := h.migrations.CreateRequest(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"), migration.RequestInput{
		Operation: migration.Operation(in.Operation), ExpectedRevision: in.ExpectedRevision, ExpectedDefinition: in.ExpectedDefinition, ExpectedVersion: in.ExpectedVersion})
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	c.Header("Location", "/api/v1/databases/"+r.DatabaseID+"/requests/"+r.ID)
	c.JSON(http.StatusCreated, gin.H{"data": apiRequestDTO(r)})
}

func (h *Migrations) apiRequest(c *gin.Context) {
	r, e := h.migrations.Request(c.Request.Context(), c.Param("databaseID"), c.Param("requestID"))
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	apiData(c, apiRequestDTO(r))
}

func (h *Migrations) apiSubmitRequest(c *gin.Context) {
	r, e := h.migrations.SubmitRequest(c.Request.Context(), c.Param("databaseID"), c.Param("requestID"))
	if e != nil {
		apiMigrationFailure(c, e)
		return
	}
	apiData(c, apiRequestDTO(r))
}

func (h *Migrations) apiDecide(approve bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in struct {
			GroupID string `json:"group_id"`
		}
		if !apiDecode(c, &in) {
			return
		}
		r, e := h.migrations.Decide(c.Request.Context(), c.Param("databaseID"), c.Param("requestID"), in.GroupID, approve)
		if e != nil {
			apiMigrationFailure(c, e)
			return
		}
		apiData(c, apiRequestDTO(r))
	}
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
	v := apiDiffProposal{Schema: p.Schema, SourceFingerprint: p.SourceFingerprint, Warnings: append([]string{}, p.Warnings...), SQL: p.SQL, RequiresReview: p.RequiresReview, Changes: []apiDiffChange{}}
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
	if errors.Is(err, access.ErrNotFound) {
		return http.StatusNotFound, "not_found", "Database not found."
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
