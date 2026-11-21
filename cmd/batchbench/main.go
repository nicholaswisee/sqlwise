// Command batchbench measures a whole-table UPDATE against the same dataset
// run through the checkpointed batch runtime, on a disposable database it
// creates and drops. Never point it at a real user database.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"sqlwise/internal/database"
)

type report struct {
	Server          string  `json:"server_version"`
	Settings        string  `json:"server_settings"`
	ClientCPUs      int     `json:"client_cpus"`
	Rows            int64   `json:"rows"`
	TableBytes      string  `json:"table_size"`
	Indexes         string  `json:"indexes"`
	WholeSQL        string  `json:"whole_table_sql"`
	WholeSeconds    float64 `json:"whole_table_seconds"`
	ScaledFrom      []int64 `json:"scaled_attempts_rows"`
	BatchSize       int     `json:"batch_size"`
	BatchSeconds    float64 `json:"batched_seconds"`
	Batches         int64   `json:"batches"`
	BatchRows       int64   `json:"batched_rows"`
	PeakHeapMB      float64 `json:"peak_go_heap_mb"`
	FailAtBatch     int     `json:"fail_at_batch"`
	FailedCommitted int64   `json:"batches_committed_before_failure"`
	FailedRows      int64   `json:"rows_committed_before_failure"`
	ResumeSeconds   float64 `json:"resume_seconds"`
	ResumedBatches  []int   `json:"resume_ran_batches_first_last"`
	FinalBatches    int64   `json:"final_batches"`
	FinalRows       int64   `json:"final_rows"`
	Consistent      bool    `json:"all_rows_updated_once"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "batchbench:", err)
		os.Exit(1)
	}
}

func run() error {
	admin := flag.String("admin-url", os.Getenv("BENCH_ADMIN_URL"), "PostgreSQL URL with CREATEDB on a disposable server")
	rows := flag.Int64("rows", 1_000_000, "initial row count; doubled until the whole-table UPDATE exceeds -min")
	minimum := flag.Duration("min", time.Second, "the whole-table UPDATE must take strictly longer than this")
	maxRows := flag.Int64("max-rows", 64_000_000, "stop scaling at this row count")
	size := flag.Int("batch", 50_000, "batch size")
	failAt := flag.Int("fail-at", 5, "inject a failure before committing this batch (0 to skip)")
	keep := flag.Bool("keep", false, "keep the benchmark database afterwards")
	flag.Parse()
	if *admin == "" {
		return errors.New("-admin-url or BENCH_ADMIN_URL is required")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(*admin)
	if err != nil {
		return err
	}
	name := "sqlwise_bench"
	if err := recreate(ctx, cfg, name, true); err != nil {
		return err
	}
	if !*keep {
		defer recreate(context.Background(), cfg, name, false)
	}
	target := cfg.Copy()
	target.Database = name
	conn, err := pgx.ConnectConfig(ctx, target)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)

	r := report{ClientCPUs: runtime.NumCPU(), BatchSize: *size, FailAtBatch: *failAt}
	_ = conn.QueryRow(ctx, `SELECT version()`).Scan(&r.Server)
	_ = conn.QueryRow(ctx, `SELECT string_agg(name || '=' || setting || COALESCE(unit, ''), ', ' ORDER BY name) FROM pg_settings
		WHERE name IN ('shared_buffers', 'work_mem', 'max_wal_size', 'synchronous_commit', 'fsync')`).Scan(&r.Settings)
	r.WholeSQL = `UPDATE bench.accounts SET status = 'archived'`

	// Scale until the genuine whole-table UPDATE is strictly slower than the minimum.
	for n := *rows; ; n *= 2 {
		if err := seed(ctx, conn, n); err != nil {
			return err
		}
		r.Rows = n
		r.ScaledFrom = append(r.ScaledFrom, n)
		started := time.Now()
		if _, err := conn.Exec(ctx, r.WholeSQL); err != nil {
			return err
		}
		r.WholeSeconds = time.Since(started).Seconds()
		fmt.Fprintf(os.Stderr, "whole-table UPDATE of %d rows took %.3fs\n", n, r.WholeSeconds)
		if r.WholeSeconds > minimum.Seconds() {
			break
		}
		if n*2 > *maxRows {
			return fmt.Errorf("whole-table UPDATE stayed under %v at %d rows", *minimum, n)
		}
	}
	_ = conn.QueryRow(ctx, `SELECT pg_size_pretty(pg_total_relation_size('bench.accounts'))`).Scan(&r.TableBytes)
	_ = conn.QueryRow(ctx, `SELECT string_agg(indexdef, '; ') FROM pg_indexes WHERE schemaname='bench' AND tablename='accounts'`).Scan(&r.Indexes)

	// Same logical dataset again, then the batched run on a gate session.
	if err := seed(ctx, conn, r.Rows); err != nil {
		return err
	}
	spec := database.BatchUpdate{Schema: "bench", Table: "accounts", Key: "id", Column: "status", Value: "archived"}
	peak := sampleHeap()
	gate, err := database.AcquireGate(ctx, target.Copy(), "bench", 5*time.Second)
	if err != nil {
		return err
	}
	plan, err := gate.PrepareBatch(ctx, "bench-full", spec, *size)
	if err != nil {
		return err
	}
	started := time.Now()
	p, err := gate.RunBatches(ctx, plan, database.BatchHooks{})
	r.BatchSeconds = time.Since(started).Seconds()
	gate.Close()
	r.PeakHeapMB = peak()
	if err != nil {
		return err
	}
	r.Batches, r.BatchRows = p.Batches, p.Rows

	if *failAt > 0 {
		if err := failAndResume(ctx, conn, target, spec, *size, *failAt, &r); err != nil {
			return err
		}
	}
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	return out.Encode(r)
}

// failAndResume stops a run before committing batch failAt, then resumes it
// on a new gate as a restarted worker would.
func failAndResume(ctx context.Context, conn *pgx.Conn, target *pgx.ConnConfig, spec database.BatchUpdate, size, failAt int, r *report) error {
	if err := seed(ctx, conn, r.Rows); err != nil {
		return err
	}
	gate, err := database.AcquireGate(ctx, target.Copy(), "bench", 5*time.Second)
	if err != nil {
		return err
	}
	plan, err := gate.PrepareBatch(ctx, "bench-resume", spec, size)
	if err != nil {
		return err
	}
	injected := errors.New("injected failure")
	p, err := gate.RunBatches(ctx, plan, database.BatchHooks{BeforeCommit: func(batch int) error {
		if batch == failAt {
			return injected
		}
		return nil
	}})
	gate.Close()
	if !errors.Is(err, injected) {
		return fmt.Errorf("expected injected failure, got %v", err)
	}
	r.FailedCommitted, r.FailedRows = p.Batches, p.Rows
	gate, err = database.AcquireGate(ctx, target.Copy(), "bench", 5*time.Second)
	if err != nil {
		return err
	}
	defer gate.Close()
	if plan, err = gate.PrepareBatch(ctx, "bench-resume", spec, size); err != nil {
		return err
	}
	var ran []int
	started := time.Now()
	p, err = gate.RunBatches(ctx, plan, database.BatchHooks{BeforeCommit: func(batch int) error {
		ran = append(ran, batch)
		return nil
	}})
	r.ResumeSeconds = time.Since(started).Seconds()
	if err != nil {
		return err
	}
	if len(ran) > 0 {
		r.ResumedBatches = []int{ran[0], ran[len(ran)-1]}
	}
	r.FinalBatches, r.FinalRows = p.Batches, p.Rows
	var left, images int64
	if err := conn.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status <> 'archived') FROM bench.accounts`).Scan(&left); err != nil {
		return err
	}
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM sqlwise_internal.batch_rows WHERE execution_id='bench-resume'`).Scan(&images); err != nil {
		return err
	}
	r.Consistent = left == 0 && images == r.Rows && p.Rows == r.Rows
	return nil
}

// seed rebuilds the identical logical dataset: n accounts, all active.
func seed(ctx context.Context, conn *pgx.Conn, n int64) error {
	for _, sql := range []string{
		`DROP SCHEMA IF EXISTS bench CASCADE`,
		`DROP SCHEMA IF EXISTS sqlwise_internal CASCADE`,
		`CREATE SCHEMA bench`,
		`CREATE TABLE bench.accounts (id bigint PRIMARY KEY, status text NOT NULL, balance numeric(12,2) NOT NULL, email text NOT NULL)`,
		fmt.Sprintf(`INSERT INTO bench.accounts SELECT g, 'active', (g %% 10000) / 100.0, 'user' || g || '@example.test' FROM generate_series(1, %d) g`, n),
		`VACUUM ANALYZE bench.accounts`,
		`CHECKPOINT`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	return nil
}

func recreate(ctx context.Context, cfg *pgx.ConnConfig, name string, create bool) error {
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	quoted := pgx.Identifier{name}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)"); err != nil {
		return err
	}
	if create {
		_, err = conn.Exec(ctx, "CREATE DATABASE "+quoted)
	}
	return err
}

// sampleHeap tracks peak Go heap until the returned function is called.
func sampleHeap() func() float64 {
	var mu sync.Mutex
	var peak uint64
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			mu.Lock()
			peak = max(peak, m.HeapAlloc)
			mu.Unlock()
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	return func() float64 {
		close(done)
		mu.Lock()
		defer mu.Unlock()
		return float64(peak) / (1 << 20)
	}
}
