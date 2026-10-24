package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/migration"
	"sqlwise/internal/schemadiff"
	"sqlwise/internal/web/views"
)

// Migrations serves the /databases/:databaseID/migrations and schema-diff
// route families. Routes are guarded by RequireSession and
// RequireDatabasePermission; the services re-evaluate permission on each call,
// so execution rechecks edit after waiting for the lock.
type Migrations struct {
	auth       *Auth
	policy     DatabasePermissionChecker
	migrations *migration.Service
	diff       *schemadiff.Service
	assets     views.Assets
}

func NewMigrations(auth *Auth, policy DatabasePermissionChecker, migrations *migration.Service, diff *schemadiff.Service, assets views.Assets) *Migrations {
	return &Migrations{auth: auth, policy: policy, migrations: migrations, diff: diff, assets: assets}
}

func (h *Migrations) Register(router *gin.Engine) {
	db := router.Group("/databases/:databaseID", h.auth.RequireSession())
	view := db.Group("", h.require(access.View))
	edit := db.Group("", h.require(access.Edit))
	view.GET("/migrations", h.history)
	edit.POST("/migrations", h.create)
	view.GET("/migrations/:migrationID", h.detail)
	edit.PATCH("/migrations/:migrationID", h.update)
	// Plain HTML forms cannot send PATCH; POST to the resource is the fallback.
	edit.POST("/migrations/:migrationID", h.update)
	edit.POST("/migrations/:migrationID/execute", h.execute)
	view.GET("/migrations/:migrationID/attempts/:attemptID", h.attempt)
	edit.POST("/migrations/:migrationID/attempts/:attemptID/reconcile", h.markReconciled)
	view.GET("/schema-diff", h.schemaDiff)
	view.POST("/schema-diff/preview", h.preview)
	edit.POST("/schema-diff/draft", h.useAsDraft)
}

// require is RequireDatabasePermission with h.fail, so denials render the
// page or HTMX fragment message instead of an empty 403 body.
func (h *Migrations) require(required access.Permission) gin.HandlerFunc {
	return requireDatabasePermissionWithFailure(h.auth.service.ValidateContextFresh, h.policy, "databaseID", required, h.fail)
}

func (h *Migrations) nav(c *gin.Context, db migration.Database, active string) views.DatabaseNav {
	session := c.MustGet("auth_session").(auth.Session)
	return views.DatabaseNav{ID: db.ID, Name: db.Name, CanEdit: db.CanEdit, Username: session.Principal.Username, CSRFToken: session.CSRFToken, Active: active}
}

func htmx(c *gin.Context) bool { return c.GetHeader("HX-Request") == "true" }

// see redirects with PRG, or tells HTMX to navigate.
func see(c *gin.Context, location string) {
	if htmx(c) {
		c.Header("HX-Redirect", location)
		c.Status(http.StatusNoContent)
		return
	}
	c.Redirect(http.StatusSeeOther, location)
}

// status maps domain and access errors to HTTP status and a safe message.
func status(err error) (int, string) {
	switch {
	case errors.Is(err, auth.ErrAuthorizationEpoch):
		return http.StatusForbidden, "Your access changed. Reload the page to continue."
	case errors.Is(err, access.ErrPermissionDenied):
		return http.StatusForbidden, "You do not have permission for this action on this database."
	}
	message := migration.Message(err)
	switch {
	case errors.Is(err, migration.ErrNotFound):
		return http.StatusNotFound, message
	case errors.Is(err, migration.ErrValidation), errors.Is(err, migration.ErrUnsupportedDialect), errors.Is(err, migration.ErrExecutionFailed):
		return http.StatusUnprocessableEntity, message
	case errors.Is(err, migration.ErrConflict), errors.Is(err, migration.ErrBusy), errors.Is(err, migration.ErrRecoveryRequired):
		return http.StatusConflict, message
	case errors.Is(err, migration.ErrTargetUnavailable), errors.Is(err, migration.ErrOutcomeUnknown):
		return http.StatusServiceUnavailable, message
	case errors.Is(err, migration.ErrTimeout):
		return http.StatusGatewayTimeout, message
	}
	log.Print("migration request failed")
	return http.StatusInternalServerError, "Something went wrong. Please try again."
}

// fail renders a whole-page error, delegating session failures to Auth.
func (h *Migrations) fail(c *gin.Context, err error) {
	if errors.Is(err, auth.ErrSession) {
		h.auth.rejectSession(c)
		return
	}
	code, message := status(err)
	if c.Request.Context().Err() != nil {
		c.AbortWithStatus(code)
		return
	}
	if htmx(c) {
		h.render(c, code, views.EditResult(message))
		return
	}
	h.render(c, code, views.DatabaseError(h.assets, message))
}

func (h *Migrations) render(c *gin.Context, code int, component templ.Component) {
	Render(c, code, component)
	c.Abort()
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func attemptRow(a migration.Attempt) views.MigrationAttemptRow {
	row := views.MigrationAttemptRow{ID: a.ID, MigrationID: a.MigrationID, MigrationTitle: a.MigrationTitle, DraftRevision: a.DraftRevision,
		CandidateVersion: a.CandidateVersion, AppliedVersion: a.AppliedVersion, Status: string(a.Status), Executor: a.ExecutorName,
		StartedAt: formatTime(a.StartedAt), Reconciled: a.ReconciledAt != nil, Error: a.Error, FailedStatement: a.FailedStatement,
		StatementCount: a.StatementCount, SQL: a.SQL, SQLHash: a.SQLHash}
	if a.FinishedAt != nil {
		row.FinishedAt = formatTime(*a.FinishedAt)
	} else {
		row.FinishedAt = "not recorded"
	}
	return row
}

func attemptRows(attempts []migration.Attempt) []views.MigrationAttemptRow {
	rows := make([]views.MigrationAttemptRow, len(attempts))
	for i, a := range attempts {
		rows[i] = attemptRow(a)
	}
	return rows
}

func (h *Migrations) historyData(c *gin.Context, form views.MigrationForm, message string) (views.MigrationHistoryData, error) {
	history, err := h.migrations.History(c.Request.Context(), c.Param("databaseID"))
	if err != nil {
		return views.MigrationHistoryData{}, err
	}
	data := views.MigrationHistoryData{Nav: h.nav(c, history.Database, "migrations"), CurrentVersion: history.CurrentVersion,
		Attempts: attemptRows(history.Attempts), Form: form, Error: message}
	for _, d := range history.Drafts {
		data.Drafts = append(data.Drafts, views.MigrationDraftRow{ID: d.ID, Title: d.Title, Revision: d.Revision, AppliedVersion: d.AppliedVersion,
			LastStatus: string(d.LastStatus), UpdatedBy: d.UpdatedByName, UpdatedAt: formatTime(d.UpdatedAt)})
	}
	return data, nil
}

func (h *Migrations) history(c *gin.Context) {
	data, err := h.historyData(c, views.MigrationForm{}, "")
	if err != nil {
		h.fail(c, err)
		return
	}
	Render(c, http.StatusOK, views.MigrationHistory(h.assets, data))
}

func (h *Migrations) create(c *gin.Context) {
	form := views.MigrationForm{Title: c.Request.PostForm.Get("title"), SQL: c.Request.PostForm.Get("sql")}
	draft, err := h.migrations.Create(c.Request.Context(), c.Param("databaseID"), migration.DraftInput{Title: form.Title, SQL: form.SQL})
	if err == nil {
		see(c, "/databases/"+draft.DatabaseID+"/migrations/"+draft.ID)
		return
	}
	code, message := status(err)
	if code != http.StatusUnprocessableEntity || htmx(c) {
		h.fail(c, err)
		return
	}
	// Keep the user's input when validation fails.
	data, loadErr := h.historyData(c, form, message)
	if loadErr != nil {
		h.fail(c, loadErr)
		return
	}
	Render(c, code, views.MigrationHistory(h.assets, data))
}

func (h *Migrations) detailData(c *gin.Context) (views.MigrationDetailData, error) {
	detail, err := h.migrations.Detail(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"))
	if err != nil {
		return views.MigrationDetailData{}, err
	}
	d := detail.Draft
	return views.MigrationDetailData{Nav: h.nav(c, detail.Database, "migrations"), CurrentVersion: detail.CurrentVersion,
		Draft: views.MigrationDraftView{ID: d.ID, Title: d.Title, SQL: d.SQL, SQLHash: d.SQLHash(), Dialect: string(d.Dialect), Revision: d.Revision,
			AppliedVersion: d.AppliedVersion, CreatedBy: d.CreatedByName, CreatedAt: formatTime(d.CreatedAt), UpdatedBy: d.UpdatedByName, UpdatedAt: formatTime(d.UpdatedAt)},
		Form: views.MigrationForm{Title: d.Title, SQL: d.SQL}, Attempts: attemptRows(detail.Attempts), Blocked: detail.Blocked, ReconcileURL: reconcileURL(detail)}, nil
}

func reconcileURL(detail migration.Detail) string {
	if detail.BlockedBy == nil || !detail.Database.CanEdit {
		return ""
	}
	a := detail.BlockedBy
	return "/databases/" + a.DatabaseID + "/migrations/" + a.MigrationID + "/attempts/" + a.ID + "/reconcile"
}

func (h *Migrations) markReconciled(c *gin.Context) {
	a, err := h.migrations.MarkReconciled(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"), c.Param("attemptID"))
	if err != nil {
		h.fail(c, err)
		return
	}
	see(c, "/databases/"+a.DatabaseID+"/migrations/"+a.MigrationID)
}

func (h *Migrations) detail(c *gin.Context) {
	data, err := h.detailData(c)
	if err != nil {
		h.fail(c, err)
		return
	}
	if c.Query("saved") == "1" {
		data.Notice = "Draft saved as revision " + strconv.FormatInt(data.Draft.Revision, 10) + "."
	}
	if attemptID := c.Query("attempt"); attemptID != "" {
		for i := range data.Attempts {
			if data.Attempts[i].ID == attemptID {
				data.Result = &views.ExecutionResultView{DatabaseID: data.Nav.ID, MigrationID: data.Draft.ID, Attempt: &data.Attempts[i], Message: data.Attempts[i].Error}
			}
		}
	}
	Render(c, http.StatusOK, views.MigrationDetail(h.assets, data))
}

func formInt(c *gin.Context, name string) (int64, bool) {
	v, err := strconv.ParseInt(c.Request.PostForm.Get(name), 10, 64)
	return v, err == nil && v >= 0
}

func (h *Migrations) update(c *gin.Context) {
	revision, ok := formInt(c, "expected_revision")
	if !ok {
		h.fail(c, &migration.Error{Kind: migration.ErrValidation, Message: "The form is missing the revision being edited. Reload and try again."})
		return
	}
	input := migration.UpdateInput{ExpectedRevision: revision,
		DraftInput: migration.DraftInput{Title: c.Request.PostForm.Get("title"), SQL: c.Request.PostForm.Get("sql")}}
	draft, err := h.migrations.Update(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"), input)
	if err == nil {
		see(c, "/databases/"+draft.DatabaseID+"/migrations/"+draft.ID+"?saved=1")
		return
	}
	code, message := status(err)
	if htmx(c) || (code != http.StatusUnprocessableEntity && code != http.StatusConflict) {
		h.fail(c, err)
		return
	}
	data, loadErr := h.detailData(c)
	if loadErr != nil {
		h.fail(c, loadErr)
		return
	}
	data.Form, data.EditError = views.MigrationForm{Title: input.Title, SQL: input.SQL}, message
	Render(c, code, views.MigrationDetail(h.assets, data))
}

func (h *Migrations) execute(c *gin.Context) {
	revision, okRevision := formInt(c, "expected_revision")
	version, okVersion := formInt(c, "expected_version")
	hash := c.Request.PostForm.Get("expected_sql_hash")
	if !okRevision || !okVersion || len(hash) != 64 {
		h.fail(c, &migration.Error{Kind: migration.ErrValidation, Message: "The confirmation form is incomplete. Reload the migration and confirm again."})
		return
	}
	databaseID, migrationID := c.Param("databaseID"), c.Param("migrationID")
	attempt, err := h.migrations.Execute(c.Request.Context(), databaseID, migrationID,
		migration.ExecuteInput{ExpectedRevision: revision, ExpectedVersion: version, ExpectedSQLHash: hash})
	if err == nil {
		see(c, "/databases/"+databaseID+"/migrations/"+migrationID+"?attempt="+attempt.ID)
		return
	}
	if errors.Is(err, auth.ErrSession) {
		h.auth.rejectSession(c)
		return
	}
	code, message := status(err)
	result := views.ExecutionResultView{DatabaseID: databaseID, MigrationID: migrationID, Message: message}
	if attempt.ID != "" {
		row := attemptRow(attempt)
		result.Attempt = &row
	}
	if htmx(c) {
		h.render(c, code, views.ExecutionResult(result))
		return
	}
	if code == http.StatusForbidden || code == http.StatusNotFound {
		h.fail(c, err)
		return
	}
	data, loadErr := h.detailData(c)
	if loadErr != nil {
		h.fail(c, loadErr)
		return
	}
	data.Result = &result
	Render(c, code, views.MigrationDetail(h.assets, data))
}

func (h *Migrations) attempt(c *gin.Context) {
	db, attempt, err := h.migrations.Attempt(c.Request.Context(), c.Param("databaseID"), c.Param("migrationID"), c.Param("attemptID"))
	if err != nil {
		h.fail(c, err)
		return
	}
	Render(c, http.StatusOK, views.AttemptDetail(h.assets, views.AttemptDetailData{Nav: h.nav(c, db, "migrations"), Attempt: attemptRow(attempt)}))
}

func (h *Migrations) schemaDiffData(c *gin.Context, desired string) (views.SchemaDiffData, error) {
	db, err := h.migrations.Database(c.Request.Context(), c.Param("databaseID"))
	if err != nil {
		return views.SchemaDiffData{}, err
	}
	return views.SchemaDiffData{Nav: h.nav(c, db, "schema-diff"), Desired: desired}, nil
}

func (h *Migrations) schemaDiff(c *gin.Context) {
	data, err := h.schemaDiffData(c, views.DesiredSnapshotExample)
	if err != nil {
		h.fail(c, err)
		return
	}
	Render(c, http.StatusOK, views.SchemaDiff(h.assets, data))
}

func proposalView(p schemadiff.Proposal) *views.SchemaDiffProposal {
	view := &views.SchemaDiffProposal{Schema: p.Schema, SourceFingerprint: p.SourceFingerprint, Warnings: p.Warnings, SQL: p.SQL, RequiresReview: p.RequiresReview}
	for _, change := range p.Changes {
		view.Changes = append(view.Changes, views.SchemaDiffChange{Text: change.String(), Destructive: change.Destructive})
	}
	return view
}

func (h *Migrations) preview(c *gin.Context) {
	desired := c.Request.PostForm.Get("desired")
	data, err := h.schemaDiffData(c, desired)
	if err != nil {
		h.fail(c, err)
		return
	}
	code := http.StatusOK
	proposal, err := h.diff.Preview(c.Request.Context(), data.Nav.ID, desired)
	if errors.Is(err, auth.ErrSession) {
		h.auth.rejectSession(c)
		return
	}
	if err != nil {
		code, data.Error = status(err)
	} else {
		data.Proposal = proposalView(proposal)
	}
	if htmx(c) {
		Render(c, code, views.SchemaDiffResult(data))
		return
	}
	Render(c, code, views.SchemaDiff(h.assets, data))
}

func (h *Migrations) useAsDraft(c *gin.Context) {
	desired, title := c.Request.PostForm.Get("desired"), c.Request.PostForm.Get("title")
	draft, err := h.diff.UseAsDraft(c.Request.Context(), c.Param("databaseID"), desired, c.Request.PostForm.Get("source_fingerprint"), title)
	if err == nil {
		see(c, "/databases/"+draft.DatabaseID+"/migrations/"+draft.ID)
		return
	}
	code, message := status(err)
	if code == http.StatusForbidden || code == http.StatusNotFound || errors.Is(err, auth.ErrSession) {
		h.fail(c, err)
		return
	}
	data, loadErr := h.schemaDiffData(c, desired)
	if loadErr != nil {
		h.fail(c, loadErr)
		return
	}
	data.Title, data.Error = title, message
	Render(c, code, views.SchemaDiff(h.assets, data))
}
