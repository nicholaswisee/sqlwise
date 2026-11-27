package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"sqlwise/internal/database"
	"sqlwise/internal/telemetry"
)

// Gates opens the execution gate of a target; *database.Pools implements it.
type Gates interface {
	Gate(ctx context.Context, databaseID string, wait time.Duration) (*database.Gate, error)
}

// Progress receives durable batch progress read from the target ledger.
type Progress func(direction string, p database.BatchProgress)

// Runner executes one delivery while the worker holds the target gate. It
// must run every target statement on gate and must not take another lock.
// A redelivered job must reconcile its recorded execution, never repeat it.
type Runner interface {
	Run(ctx context.Context, gate *database.Gate, job Job, progress Progress) Outcome
}

// FailureRecorder is an optional Runner extension. The worker calls it once a
// job fails for good, including failures before the runner ever ran (gate
// refused, target unreachable after every retry), so the domain can keep a
// history entry for executions that never reached the target.
type FailureRecorder interface {
	RecordFailure(ctx context.Context, job Job, message string)
}

type Config struct {
	// ID names this worker in claims; a stable ID lets a restart reclaim at once.
	ID          string
	Concurrency int
	Poll        time.Duration
	Lease       time.Duration
	LockWait    time.Duration
	// JobTimeout bounds one delivery; it is independent of any HTTP deadline.
	JobTimeout    time.Duration
	ShutdownGrace time.Duration
	// Backoff[n] is the wait before attempt n+2; the freeze sets 1s then 2s.
	Backoff []time.Duration
	Defer   time.Duration
	Recheck time.Duration
}

func DefaultConfig() Config {
	return Config{Concurrency: 4, Poll: 500 * time.Millisecond, Lease: 30 * time.Second, LockWait: 5 * time.Second,
		JobTimeout: 30 * time.Minute, ShutdownGrace: 30 * time.Second, Backoff: []time.Duration{time.Second, 2 * time.Second},
		Defer: 2 * time.Second, Recheck: 15 * time.Second}
}

type Worker struct {
	store  *Store
	gates  Gates
	runner Runner
	cfg    Config
	tracer trace.Tracer
}

func NewWorker(store *Store, gates Gates, runner Runner, cfg Config) (*Worker, error) {
	if cfg.ID == "" {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		cfg.ID = "worker-" + id[:12]
	}
	if cfg.Concurrency < 1 || cfg.Lease < time.Second || cfg.JobTimeout <= 0 || cfg.Poll <= 0 {
		return nil, errors.New("worker concurrency, poll, lease and job timeout must be positive; lease at least 1s")
	}
	if len(cfg.Backoff) < MaxAttempts-1 {
		return nil, fmt.Errorf("worker backoff needs %d waits", MaxAttempts-1)
	}
	return &Worker{store: store, gates: gates, runner: runner, cfg: cfg, tracer: otel.Tracer("sqlwise/jobs")}, nil
}

func (w *Worker) ID() string { return w.cfg.ID }

// Run delivers jobs until ctx ends, then stops claiming and waits up to the
// shutdown grace for in-flight deliveries before cancelling them. Deliveries
// use their own context, so stopping never cuts a batch mid-commit early.
func (w *Worker) Run(ctx context.Context) error {
	if n, err := w.store.expireOwn(context.WithoutCancel(ctx), w.cfg.ID); err != nil {
		return err
	} else if n > 0 {
		slog.Warn("worker reclaiming jobs of its previous run", "event", "worker.recover", "worker", w.cfg.ID, "jobs", n)
	}
	slog.Info("worker started", "event", "worker.start", "worker", w.cfg.ID, "concurrency", w.cfg.Concurrency)
	jobs, cancelJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelJobs()
	slots := make(chan struct{}, w.cfg.Concurrency)
	var wg sync.WaitGroup
claiming:
	for {
		select {
		case <-ctx.Done():
			break claiming
		case slots <- struct{}{}:
		}
		claimCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		c, ok, err := w.store.claim(claimCtx, w.cfg.ID, w.cfg.Lease)
		cancel()
		if err != nil || !ok {
			<-slots
			if err != nil {
				slog.Error("job claim failed", "event", "job.claim.error", "worker", w.cfg.ID, "error", err.Error())
			}
			select {
			case <-ctx.Done():
				break claiming
			case <-time.After(w.cfg.Poll):
			}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			w.deliver(jobs, c)
		}()
	}
	slog.Info("worker stopping", "event", "worker.stop", "worker", w.cfg.ID)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(w.cfg.ShutdownGrace):
		slog.Warn("worker grace elapsed; cancelling deliveries", "event", "worker.stop.cancel", "worker", w.cfg.ID)
		cancelJobs()
		<-done
	}
	return nil
}

// deliver runs one claimed job and records its outcome before the gate is
// released, so the next holder sees any block this job left.
func (w *Worker) deliver(base context.Context, c claimed) {
	ctx := telemetry.ExtractTrace(base, c.Trace)
	ctx = telemetry.WithCorrelation(telemetry.WithActor(ctx, c.ActorID), c.ID)
	ctx = telemetry.WithLogFields(ctx, "database", c.DatabaseID, "job_id", c.ID, "migration_id", c.MigrationID,
		"execution_id", c.ExecutionID, "operation", c.Operation, "attempt", c.Attempts+1)
	ctx, span := w.tracer.Start(ctx, "job.run", trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(
		attribute.String("sqlwise.job.id", c.ID), attribute.String("sqlwise.execution.id", c.ExecutionID),
		attribute.String("sqlwise.job.operation", c.Operation), attribute.String("sqlwise.database.id", c.DatabaseID),
		attribute.String("sqlwise.delivery.id", c.delivery)))
	defer span.End()
	log := slog.With("job_id", c.ID, "delivery_id", c.delivery, "request_id", c.RequestID, "execution_id", c.ExecutionID, "migration_id", c.MigrationID, "database", c.DatabaseID,
		"operation", c.Operation, "actor", c.ActorID, "worker", w.cfg.ID, "correlation_id", c.ID)
	if sc := span.SpanContext(); sc.IsValid() {
		log = log.With("trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
	}
	switch c.previous {
	case Running:
		log.WarnContext(ctx, "job reclaimed after its lease expired", "event", "job.reclaimed", "attempt", c.Attempts)
	case Queued:
		if c.first && c.StartedAt != nil {
			queueWait.WithLabelValues(operationLabel(c.Operation)).Observe(c.StartedAt.Sub(c.CreatedAt).Seconds())
		}
	}
	log.InfoContext(ctx, "job claimed", "event", "job.claimed", "attempt", c.Attempts+1, "previous_status", string(c.previous))

	beat, stop := context.WithCancel(ctx)
	defer stop()
	go w.heartbeat(beat, c.ID, log)

	gate, ok := w.acquire(ctx, c, log)
	if !ok {
		return
	}
	defer gate.Close()
	recovery := c.RecoversExecutionID != ""
	if !recovery && c.previous != Blocked {
		// The block may have been written by the previous gate holder after this claim.
		if blocker, err := w.store.blocker(ctx, c.DatabaseID, c.ID); err != nil || blocker != "" {
			reason := "Waiting for an approved recovery of blocked job " + blocker + "."
			if err != nil {
				reason = ""
			}
			w.requeue(ctx, c, c.Attempts, w.cfg.Defer, reason, "blocked", log)
			return
		}
	}
	attempts := c.Attempts
	if c.previous != Blocked {
		attempts++
	}
	run, cancel := context.WithTimeout(ctx, w.cfg.JobTimeout)
	defer cancel()
	started := time.Now()
	outcome := w.run(run, gate, c, log)
	executionDuration.WithLabelValues(operationLabel(c.Operation), string(outcome.Status)).Observe(time.Since(started).Seconds())
	if outcome.Status != Succeeded {
		span.SetStatus(codes.Error, string(outcome.Status))
	}
	span.SetAttributes(attribute.String("sqlwise.job.status", string(outcome.Status)), attribute.String("sqlwise.job.effect", string(outcome.Effect)))
	w.settle(ctx, c, outcome, attempts, log)
}

// acquire waits for the target gate; busy targets defer without spending an attempt.
func (w *Worker) acquire(ctx context.Context, c claimed, log *slog.Logger) (*database.Gate, bool) {
	ctx, span := w.tracer.Start(ctx, "job.lock")
	defer span.End()
	started := time.Now()
	gate, err := w.gates.Gate(ctx, c.DatabaseID, w.cfg.LockWait)
	waited := time.Since(started)
	switch {
	case err == nil:
		lockWait.WithLabelValues("acquired").Observe(waited.Seconds())
		log.InfoContext(ctx, "job gate acquired", "event", "job.lock.acquired", "lock_wait_ms", waited.Milliseconds(), "target_pid", gate.PID())
		return gate, true
	case errors.Is(err, database.ErrBusy):
		lockWait.WithLabelValues("busy").Observe(waited.Seconds())
		w.requeue(ctx, c, c.Attempts, w.cfg.Defer, "", "busy", log)
	case errors.Is(err, database.ErrUnsupportedDialect), errors.Is(err, database.ErrNotFound):
		lockWait.WithLabelValues("refused").Observe(waited.Seconds())
		if c.previous == Blocked || c.previous == Running {
			w.settle(ctx, c, Unknown("The target cannot be reached to reconcile the previous delivery."), c.Attempts, log)
			break
		}
		w.settle(ctx, c, Failure(messageOr(err, "This database cannot run queued jobs.")+" Nothing was executed."), c.Attempts+1, log)
	default:
		lockWait.WithLabelValues("unavailable").Observe(waited.Seconds())
		message := messageOr(err, "The target database is unavailable.") + " Nothing was executed."
		if c.previous == Blocked {
			w.requeue(ctx, c, c.Attempts, w.cfg.Recheck, "", "unavailable", log)
			break
		}
		if c.previous == Running {
			w.settle(ctx, c, Unknown("The target is unavailable; the previous delivery has not been reconciled."), c.Attempts, log)
			break
		}
		w.settle(ctx, c, Transient(message), c.Attempts+1, log)
	}
	span.SetStatus(codes.Error, "gate not acquired")
	return nil, false
}

// run calls the runner and treats a panic as an unknown effect, never a success.
func (w *Worker) run(ctx context.Context, gate *database.Gate, c claimed, log *slog.Logger) (out Outcome) {
	defer func() {
		if r := recover(); r != nil {
			log.ErrorContext(ctx, "job runner panicked", "event", "job.panic")
			out = Unknown("The worker failed while executing; the outcome is checked before anything else runs on this database.")
		}
	}()
	var last database.BatchProgress
	progress := func(direction string, p database.BatchProgress) {
		batches, rows, total := p.Batches, p.Rows, p.TotalRows
		if direction == database.Compensate {
			// A compensation job reports batches it reverted out of those committed.
			RecordBatch(direction, p.Compensated-last.Compensated, p.CompensatedRows-last.CompensatedRows)
			batches, rows, total = p.Compensated, p.CompensatedRows, p.Rows
		} else {
			RecordBatch(direction, p.Batches-last.Batches, p.Rows-last.Rows)
		}
		last = p
		if err := w.store.progress(context.WithoutCancel(ctx), c.ID, w.cfg.ID, batches, rows, &total); err != nil {
			log.WarnContext(ctx, "job progress not recorded", "event", "job.progress.error")
		}
		log.InfoContext(ctx, "job batch committed", "event", "job.batch.committed", "direction", direction,
			"batches", p.Batches, "rows", p.Rows, "total_rows", p.TotalRows)
	}
	log.InfoContext(ctx, "job started", "event", "job.start")
	return w.runner.Run(ctx, gate, c.Job, progress)
}

// settle applies the retry policy and records the outcome.
func (w *Worker) settle(ctx context.Context, c claimed, o Outcome, attempts int, log *slog.Logger) {
	ctx = context.WithoutCancel(ctx)
	if o.Deferred {
		jobDeferrals.WithLabelValues("batch_pending").Inc()
		log.InfoContext(ctx, "job deferred", "event", "job.deferred", "reason", "batch_pending", "delay_ms", w.cfg.Defer.Milliseconds(), "error", o.Message)
		if ok, err := w.store.release(ctx, c.ID, w.cfg.ID, c.Attempts, w.cfg.Defer, o.Message); err != nil || !ok {
			log.WarnContext(ctx, "job claim lost before requeue", "event", "job.claim_lost")
		}
		return
	}
	if o.Retryable && c.previous == Blocked {
		w.requeue(ctx, c, attempts, w.cfg.Recheck, "", "unavailable", log)
		return
	}
	if o.Retryable && o.Effect == EffectNone && attempts < c.MaxAttempts {
		delay := w.cfg.Backoff[attempts-1]
		jobRetries.WithLabelValues(operationLabel(c.Operation)).Inc()
		log.WarnContext(ctx, "job retry scheduled", "event", "job.retry", "attempt", attempts, "delay_ms", delay.Milliseconds(), "error", o.Message)
		w.requeue(ctx, c, attempts, delay, o.Message, "", log)
		return
	}
	if o.Retryable {
		o = Failure(fmt.Sprintf("%s Gave up after %d attempts.", o.Message, attempts))
	}
	if recorder, ok := w.runner.(FailureRecorder); ok && o.Status == Failed {
		recorder.RecordFailure(ctx, c.Job, o.Message)
	}
	ok, err := w.store.finish(ctx, c.ID, w.cfg.ID, o, attempts, w.cfg.Recheck)
	if err != nil || !ok {
		// Another worker owns the claim now; it reconciles the same execution.
		log.WarnContext(ctx, "job claim lost before recording", "event", "job.claim_lost", "status", string(o.Status))
		return
	}
	if c.previous != Blocked || o.Status != Blocked {
		jobsFinished.WithLabelValues(operationLabel(c.Operation), string(o.Status), string(o.Effect)).Inc()
	}
	level := slog.LevelInfo
	if o.Status != Succeeded {
		level = slog.LevelWarn
	}
	log.Log(ctx, level, "job finished", "event", "job.finished", "status", string(o.Status), "effect", string(o.Effect),
		"attempt", attempts, "error", o.Message)
}

func (w *Worker) requeue(ctx context.Context, c claimed, attempts int, delay time.Duration, message, reason string, log *slog.Logger) {
	ctx = context.WithoutCancel(ctx)
	if reason != "" {
		jobDeferrals.WithLabelValues(reason).Inc()
		log.InfoContext(ctx, "job deferred", "event", "job.deferred", "reason", reason, "delay_ms", delay.Milliseconds())
	}
	if ok, err := w.store.requeue(ctx, c.ID, w.cfg.ID, c.previous, attempts, delay, message); err != nil || !ok {
		log.WarnContext(ctx, "job claim lost before requeue", "event", "job.claim_lost")
	}
}

func (w *Worker) heartbeat(ctx context.Context, jobID string, log *slog.Logger) {
	tick := time.NewTicker(w.cfg.Lease / 3)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			beat, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.cfg.Lease/3)
			ok, err := w.store.heartbeat(beat, jobID, w.cfg.ID, w.cfg.Lease)
			cancel()
			if err == nil && !ok {
				// The gate still fences the target; the new claimant waits for it.
				log.Warn("job lease lost", "event", "job.lease_lost")
				return
			}
		}
	}
}

func messageOr(err error, fallback string) string {
	if m := database.Message(err); m != "" {
		return m
	}
	return fallback
}
