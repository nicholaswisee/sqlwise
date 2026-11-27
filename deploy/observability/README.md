# Local observability

## Start and endpoints

From the repository root, start the application and observability services:

```sh
docker compose -f compose.yaml -f deploy/observability/compose.yaml up -d --build --wait
```

The local endpoints are:

| Service        | Endpoint                                            | Purpose                                                                |
| -------------- | --------------------------------------------------- | ---------------------------------------------------------------------- |
| API            | `http://localhost:8080`                             | Application and `/health`                                              |
| API metrics    | `http://localhost:8080/metrics`                     | Prometheus metrics from the API process                                |
| Worker metrics | `http://worker:9091/metrics` on the Compose network | Prometheus metrics from the worker process (not published on the host) |
| Grafana        | `http://localhost:3000`                             | Dashboards (`admin` / `sqlwise_observability_local`)                   |
| Prometheus     | `http://localhost:9090`                             | Scrape targets and metric queries                                      |
| Loki           | `http://localhost:3100`                             | Log API                                                                |
| Tempo          | `http://localhost:3200`                             | Trace API                                                              |

The Compose override binds service ports to loopback. To stop the stack and retain its local data volumes, run:

```sh
docker compose -f compose.yaml -f deploy/observability/compose.yaml down
```

Find **SQLWise API** in Grafana's **SQLWise** folder.

## Signal paths and coverage

- **Logs:** Docker sends stdout/stderr from `app` and `worker` with the Fluentd driver (`sqlwise.api` and `sqlwise.worker` tags) to the Collector's `fluentforward` receiver on `127.0.0.1:24224`. The Collector adds `service.name=sqlwise` and exports logs to Loki. The driver is asynchronous; buffering is bounded, so logs may be lost if the Collector remains unavailable.
- **Traces:** API and worker initialize OpenTelemetry using `OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318`; the Collector receives OTLP/HTTP and exports traces to Tempo. Without that environment variable, traces are written to process stdout. API request spans use registered route patterns; SQL spans are named `db.query`. Job publishing stores W3C `traceparent` and optional `tracestate`; the worker extracts these fields and creates a `job.run` span. The worker emits structured events such as `worker.start`, `job.claimed`, and `job.reclaimed`, with fields including `job_id`, `delivery_id`, `execution_id`, `operation`, `worker`, `correlation_id`, `trace_id`, and `span_id` where available.
- **Metrics:** Prometheus scrapes `app:8080/metrics` as `sqlwise-api` and `worker:9091/metrics` as `sqlwise-worker`, every 10 seconds. API metrics include `sqlwise_http_requests_total` and `sqlwise_http_request_duration_seconds` (route/method/status labels) and platform pool metrics `sqlwise_db_pool_active_connections`, `sqlwise_db_pool_idle_connections`, and `sqlwise_db_pool_acquire_seconds_total`. The worker exports job metrics including `sqlwise_jobs`, `sqlwise_job_queue_wait_seconds`, `sqlwise_job_lock_wait_seconds`, `sqlwise_migration_duration_seconds`, `sqlwise_job_retries_total`, and target pool metrics including `sqlwise_target_pool_active_connections` and `sqlwise_target_pool_idle_connections`. The job status gauge reads durable counts at scrape time; several timing/counter series are process observations and reset on restart.

The provisioned dashboard currently has 12 panels: four API/platform-pool panels and panels for job status, queue and lock wait, migration execution, retries, committed batches, and target-pool connections/acquisition wait. These worker-backed panels require the worker to be running and emitting observations; no data before observations is not proof of a broken scrape. The dashboard is not an exhaustive view of every exported metric.

SQL literals and comments are masked before telemetry is emitted; query arguments and driver error text are not exported. Logs carry `correlation_id`, `trace_id`, and `span_id`; API request logs also include `method`, `route`, `status`, and `duration_ms`. `route` is the registered route pattern, not the raw URL or query string. SQL log events are `sql.start` and `sql.result`; these include the SQL fingerprint hash, rather than raw SQL text.

## Verify locally

These checks use only the local endpoints. After starting the stack:

```sh
curl -fsS http://localhost:8080/health
curl -fsS http://localhost:8080/metrics | grep '^sqlwise_http_requests_total'
curl -fsS http://localhost:9090/api/v1/targets
```

In Grafana, open **SQLWise API**. The Prometheus targets response should show both `sqlwise-api` and `sqlwise-worker` targets up. In Loki, search for `{service_name="sqlwise"} | json | event="http.request"`; follow the `trace_id` from that event in Tempo's trace search. A database-backed request should also produce `sql.start` and `sql.result` log events associated with its trace. The worker's `job.run` trace can be followed from the `trace_id` on its structured job logs.

## Historical verification (12 November 2026)

The isolated Compose test sent `GET /api/v1/auth/login-challenge` with synthetic W3C trace ID `11111111111111111111111111111111`. Loki returned nine API and SQL log events sharing correlation ID `6b1b88952d6291f616704024ee2f580b`. Tempo returned the HTTP span and four SQL child spans. Prometheus showed the `app:8080` target `up`, `/metrics` returned request and platform-pool values, and Grafana loaded the four panels available at the time. The saved evidence omits cookie and CSRF response values.

Port 5434 was already in use, so the test ran the API and observability services without the demo target. It did not verify worker traces, logs, metrics, or dashboard panels. The worker coverage described above comes from the source and Compose configuration. See [Migration jobs and recovery](../../docs/jobs.md) for worker configuration and job behavior.
