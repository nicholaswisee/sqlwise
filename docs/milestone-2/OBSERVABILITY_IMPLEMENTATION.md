# Observability implementation status

- **Owner:** Nicholas Wise Saragih Sumbayak
- **Branch:** `feat/observability-foundation`
- **Review:** [PR #10](https://github.com/nicholaswisee/sqlwise/pull/10)
- **Status:** API and platform telemetry foundation implemented; Milestone 2 W2 and W3 acceptance remains open.

## Delivered in this branch

1. `internal/telemetry/` configures JSON `slog` output, an OpenTelemetry trace provider, W3C `traceparent`/`tracestate` injection and extraction, request spans, and generated correlation IDs. With `OTEL_EXPORTER_OTLP_ENDPOINT`, traces go to the Collector; otherwise they go to stdout.
2. The API router records a route-pattern request event and exports request count and duration metrics at `/metrics`. Authentication middleware attaches the authenticated user ID to later events; successful login attaches the issued principal. The platform PostgreSQL pool records SQL start/result events and child spans, including duration, rows, SQLSTATE when available, a hash of the original statement, and masked statement text. Arguments and raw driver error details are not exported.
3. `deploy/observability/` provisions a local Collector, Loki, Tempo, Prometheus, Grafana data sources, and four API/platform-pool dashboard panels. Docker's Fluentd logging driver sends API stdout/stderr to the Collector; OTLP transports traces. The [runbook](../../deploy/observability/README.md) covers startup, queries, and the observed integration boundary.

## Verified evidence

- `go test ./internal/telemetry ./cmd/server ./internal/handlers`: 75 passed in the affected packages.
- `docker compose -f compose.yaml -f deploy/observability/compose.yaml config --quiet` and `git diff --check`: passed.
- An isolated live run sent one synthetic trace through `GET /api/v1/auth/login-challenge`. Loki returned nine API/SQL events with the same trace and correlation IDs. Tempo returned the HTTP span and four SQL child spans. Prometheus reported `app:8080` up, and Grafana loaded all four provisioned panels.
- The full Compose command could not bind the existing host port 5434 for the demo target. Starting the API and observability services explicitly worked. The isolated containers were stopped after verification.

## Still required for W2 and W3

- Jason: add the trace fields to the durable job DTO, call `telemetry.InjectTrace` at publication and `telemetry.ExtractTrace` before worker processing, initialize telemetry in the worker, forward worker stdout/stderr to the Collector, and expose actual queue, retry, lock, batch, and target-pool measurements from his lifecycle code.
- James: emit migration execution/attempt SQL events and spans from his executor, including success, failure, retry, commit/rollback, read-only, and relevant database/job/execution/attempt/batch identities. Agree on the leader-policy and telemetry contracts before changing shared files.
- Wise: complete the SQL/queue/pool inventory with both owners, add worker and job metric panels only after real measurements exist, run end-to-end success/failure/retry and secret-canary checks through the Collector, and replay the full stack in an environment with free host ports.
- The redactor masks all SQL literals, including values that may be safe to show. The assignment's exact-copy rule for non-sensitive SQL still needs an agreed interpretation; no full application-wide SQL coverage or W2/W3 sign-off is claimed here.
