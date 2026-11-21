// Command worker executes queued jobs outside HTTP requests. Run one or more
// next to the API; the per-target gate keeps them from overlapping.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/jobs"
	"sqlwise/internal/jobs/migrationrun"
	"sqlwise/internal/migration"
	"sqlwise/internal/telemetry"
)

func main() {
	shutdown, err := telemetry.Start(context.Background(), "sqlwise-worker", os.Stdout)
	if err != nil {
		log.Fatal("initialize telemetry: ", err)
	}
	defer shutdown(context.Background())
	if err := run(); err != nil {
		slog.Error("worker failed", "event", "worker.error", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, metricsAddr, err := config(os.Getenv)
	if err != nil {
		return err
	}
	pool, err := connect(os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	telemetry.BindPlatformPool(pool)
	store := jobs.NewStore(pool)
	if err := store.CheckSchema(context.Background()); err != nil {
		return err
	}
	jobs.BindMetrics(store)
	cipher, err := database.NewCipher(os.Getenv(database.KeyEnv))
	if err != nil {
		return err
	}
	pools := database.NewPools(database.NewStore(pool, cipher)).WithTracer(telemetry.PGXTracer{})
	defer pools.Close()
	jobs.BindTargetPools(pools.Stat)
	sessions, err := auth.NewService(auth.NewStore(pool), time.Now)
	if err != nil {
		return errors.New("could not initialize authorization")
	}
	policy := access.NewService(access.NewStore(pool), sessions)
	// The worker resolves targets only through its gate; Open is never used here.
	migrations := migration.NewService(migration.NewStore(pool), policy, nil, time.Now).
		WithTimeouts(migration.LockWait, cfg.JobTimeout, migration.CleanupTimeout)
	worker, err := jobs.NewWorker(store, pools, migrationrun.Runner{Service: migrations}, cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if metricsAddr != "" {
		server := &http.Server{Addr: metricsAddr, Handler: metricsMux(), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("worker metrics server failed", "event", "worker.metrics.error", "error", err.Error())
			}
		}()
		defer server.Shutdown(context.Background())
	}
	return worker.Run(ctx)
}

func metricsMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", telemetry.MetricsHandler())
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"service":"sqlwise-worker","status":"ok"}`))
	})
	return mux
}

func connect(dsn string) (*pgxpool.Pool, error) {
	if dsn == "" {
		return nil, errors.New("DATABASE_URL is required; point it at the platform database")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid platform database configuration")
	}
	config.MaxConns = 8
	config.ConnConfig.ConnectTimeout = 5 * time.Second
	config.ConnConfig.Tracer = telemetry.PGXTracer{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("could not initialize platform database pool")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("platform database unreachable")
	}
	return pool, nil
}

// config reads worker settings; durations use Go syntax such as 30s or 15m.
func config(env func(string) string) (jobs.Config, string, error) {
	cfg := jobs.DefaultConfig()
	cfg.ID = env("WORKER_ID")
	if host, err := os.Hostname(); err == nil && cfg.ID == "" {
		// Unique per process; set WORKER_ID to a stable value to reclaim at once after restarts.
		cfg.ID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	durations := map[string]*time.Duration{
		"WORKER_POLL": &cfg.Poll, "WORKER_LEASE": &cfg.Lease, "WORKER_LOCK_WAIT": &cfg.LockWait,
		"WORKER_JOB_TIMEOUT": &cfg.JobTimeout, "WORKER_SHUTDOWN_GRACE": &cfg.ShutdownGrace, "WORKER_RECHECK": &cfg.Recheck,
	}
	for name, target := range durations {
		if raw := env(name); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil || d <= 0 {
				return cfg, "", fmt.Errorf("%s must be a positive duration such as 30s", name)
			}
			*target = d
		}
	}
	if raw := env("WORKER_CONCURRENCY"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 64 {
			return cfg, "", errors.New("WORKER_CONCURRENCY must be between 1 and 64")
		}
		cfg.Concurrency = n
	}
	// "off" disables the endpoint; containers set 0.0.0.0:9091 for scraping.
	addr := env("WORKER_METRICS_ADDR")
	switch addr {
	case "":
		addr = "127.0.0.1:9091"
	case "off":
		addr = ""
	}
	return cfg, addr, nil
}
