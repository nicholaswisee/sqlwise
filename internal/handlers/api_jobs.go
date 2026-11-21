package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/jobs"
)

// JobReader is the read side of the durable queue.
type JobReader interface {
	Job(ctx context.Context, databaseID, jobID string) (jobs.Job, error)
	List(ctx context.Context, databaseID string, limit int) ([]jobs.Job, error)
}

// Jobs serves job polling; enqueueing stays on the reviewed request routes.
type Jobs struct {
	auth   *Auth
	policy DatabasePermissionChecker
	jobs   JobReader
}

func NewJobs(auth *Auth, policy DatabasePermissionChecker, reader JobReader) *Jobs {
	return &Jobs{auth: auth, policy: policy, jobs: reader}
}

// apiJobStatus keeps job_id/status/error for existing pollers. A 202 job is
// only accepted; status and effect say what actually happened.
type apiJobStatus struct {
	JobID               string     `json:"job_id"`
	DatabaseID          string     `json:"database_id"`
	MigrationID         string     `json:"migration_id"`
	RequestID           string     `json:"request_id"`
	ExecutionID         string     `json:"execution_id"`
	RecoversExecutionID string     `json:"recovers_execution_id,omitempty"`
	Operation           string     `json:"operation"`
	Status              string     `json:"status"`
	Effect              string     `json:"effect"`
	Terminal            bool       `json:"terminal"`
	Attempts            int        `json:"attempts"`
	MaxAttempts         int        `json:"max_attempts"`
	CommittedBatches    int64      `json:"committed_batches"`
	CommittedRows       int64      `json:"committed_rows"`
	TotalRows           *int64     `json:"total_rows"`
	Error               string     `json:"error,omitempty"`
	BlockedBy           string     `json:"blocked_by,omitempty"`
	ResolvedBy          string     `json:"resolved_by,omitempty"`
	Recovery            []string   `json:"recovery"`
	NextRunAt           *time.Time `json:"next_run_at,omitempty"`
	QueuedAt            time.Time  `json:"queued_at"`
	StartedAt           *time.Time `json:"started_at,omitempty"`
	FinishedAt          *time.Time `json:"finished_at,omitempty"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

func apiJobStatusDTO(j jobs.Job) apiJobStatus {
	v := apiJobStatus{JobID: j.ID, DatabaseID: j.DatabaseID, MigrationID: j.MigrationID, RequestID: j.RequestID, ExecutionID: j.ExecutionID,
		RecoversExecutionID: j.RecoversExecutionID, Operation: j.Operation, Status: string(j.Status), Effect: string(j.Effect),
		Terminal: j.Terminal(), Attempts: j.Attempts, MaxAttempts: j.MaxAttempts, CommittedBatches: j.CommittedBatches,
		CommittedRows: j.CommittedRows, TotalRows: j.TotalRows, Error: j.Error, BlockedBy: j.BlockedBy, ResolvedBy: j.ResolvedBy,
		Recovery: j.Recovery(), QueuedAt: j.CreatedAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt, UpdatedAt: j.UpdatedAt}
	if j.Status == jobs.Queued || (j.Status == jobs.Blocked && j.Effect == jobs.EffectUnknown) {
		next := j.NextRunAt
		v.NextRunAt = &next
	}
	return v
}

func (h *Jobs) RegisterAPI(router *gin.Engine) {
	view := router.Group("/api/v1/databases/:databaseID", h.auth.RequireAPISession(),
		h.auth.RequireAPIDatabasePermission(h.policy, "databaseID", access.View))
	view.GET("/jobs", h.apiList)
	view.GET("/jobs/:jobID", h.apiDetail)
}

func (h *Jobs) apiDetail(c *gin.Context) {
	j, err := h.jobs.Job(c.Request.Context(), c.Param("databaseID"), c.Param("jobID"))
	if err != nil {
		apiJobFailure(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	apiData(c, apiJobStatusDTO(j))
}

func (h *Jobs) apiList(c *gin.Context) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 100 {
		apiError(c, http.StatusUnprocessableEntity, "validation", "Limit must be between 1 and 100.")
		return
	}
	list, err := h.jobs.List(c.Request.Context(), c.Param("databaseID"), limit)
	if err != nil {
		apiJobFailure(c, err)
		return
	}
	out := make([]apiJobStatus, 0, len(list))
	for _, j := range list {
		out = append(out, apiJobStatusDTO(j))
	}
	c.Header("Cache-Control", "no-store")
	apiData(c, gin.H{"jobs": out})
}

func apiJobFailure(c *gin.Context, err error) {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		apiError(c, http.StatusNotFound, "not_found", jobs.Message(err))
	case errors.Is(err, jobs.ErrValidation):
		apiError(c, http.StatusUnprocessableEntity, "validation", jobs.Message(err))
	default:
		apiFailure(c, err)
	}
}
