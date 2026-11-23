package jobs

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sqlwise/internal/database"
	"sqlwise/internal/telemetry"
)

// Labels are bounded: operation, status, effect, direction and outcome only.
var (
	jobsPublished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sqlwise_jobs_published_total",
		Help: "Jobs durably published by operation.",
	}, []string{"operation"})
	jobsFinished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sqlwise_jobs_finished_total",
		Help: "Jobs that reached succeeded, failed or blocked, once per job transition.",
	}, []string{"operation", "status", "effect"})
	jobRetries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sqlwise_job_retries_total",
		Help: "Automatic retries scheduled after confirmed transient failures.",
	}, []string{"operation"})
	jobDeferrals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sqlwise_job_deferrals_total",
		Help: "Deliveries put back without an attempt, by reason.",
	}, []string{"reason"})
	queueWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "sqlwise_job_queue_wait_seconds",
		Help:    "Time from publication to first worker start.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"operation"})
	lockWait = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "sqlwise_job_lock_wait_seconds",
		Help:    "Time from gate request to acquisition or give-up.",
		Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
	}, []string{"outcome"})
	executionDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "sqlwise_migration_duration_seconds",
		Help:    "Logical execution time per delivery holding the gate, not HTTP enqueue time.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 300, 900},
	}, []string{"operation", "status"})
	batchesCommitted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sqlwise_batches_committed_total",
		Help: "Target batches committed, by direction.",
	}, []string{"direction"})
	rowsCommitted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sqlwise_batch_rows_committed_total",
		Help: "Rows changed by committed target batches, by direction.",
	}, []string{"direction"})
)

var (
	boundStore atomic.Pointer[Store]
	boundPools atomic.Pointer[func() database.PoolStat]
)

func init() {
	telemetry.Register(jobsPublished, jobsFinished, jobRetries, jobDeferrals, queueWait, lockWait, executionDuration,
		batchesCommitted, rowsCommitted, statusCollector{})
	pool := func(name, help string, read func(database.PoolStat) float64) prometheus.Collector {
		return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: name, Help: help, ConstLabels: prometheus.Labels{"role": "target"}}, func() float64 {
			if stat := boundPools.Load(); stat != nil {
				return read((*stat)())
			}
			return 0
		})
	}
	telemetry.Register(
		pool("sqlwise_target_pool_active_connections", "Acquired connections across target pools.", func(s database.PoolStat) float64 { return float64(s.Active) }),
		pool("sqlwise_target_pool_idle_connections", "Idle connections across target pools.", func(s database.PoolStat) float64 { return float64(s.Idle) }),
		pool("sqlwise_target_pools_open", "Target pools currently open.", func(s database.PoolStat) float64 { return float64(s.Pools) }),
		pool("sqlwise_target_pool_acquire_seconds_total", "Cumulative target pool acquisition wait.", func(s database.PoolStat) float64 { return s.AcquireWait }),
		pool("sqlwise_target_gate_sessions", "Dedicated gate sessions holding a target lock.", func(s database.PoolStat) float64 { return float64(s.Gates) }),
	)
}

// BindMetrics derives job status gauges from durable state in this store.
func BindMetrics(store *Store) { boundStore.Store(store) }

// BindTargetPools exposes target pool usage of this process.
func BindTargetPools(stat func() database.PoolStat) { boundPools.Store(&stat) }

var statusDesc = prometheus.NewDesc("sqlwise_jobs", "Durable jobs by current status.", []string{"status"}, nil)

// statusCollector reads current job counts at scrape time, so restarts never double count.
type statusCollector struct{}

func (statusCollector) Describe(ch chan<- *prometheus.Desc) { ch <- statusDesc }

func (statusCollector) Collect(ch chan<- prometheus.Metric) {
	store := boundStore.Load()
	if store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	counts, err := store.Counts(ctx)
	if err != nil {
		return
	}
	for _, s := range []Status{Queued, Running, Succeeded, Failed, Blocked} {
		ch <- prometheus.MustNewConstMetric(statusDesc, prometheus.GaugeValue, float64(counts[s]), string(s))
	}
}

func operationLabel(op string) string {
	switch op {
	case "up", "rollback", "forward", OpResume, OpCompensate:
		return op
	}
	return "other"
}

// RecordBatch counts committed batch progress deltas in one direction.
func RecordBatch(direction string, batches, rows int64) {
	if direction != database.Compensate {
		direction = database.Forward
	}
	if batches > 0 {
		batchesCommitted.WithLabelValues(direction).Add(float64(batches))
	}
	if rows > 0 {
		rowsCommitted.WithLabelValues(direction).Add(float64(rows))
	}
}
