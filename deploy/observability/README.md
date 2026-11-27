# Local observability

Start the API and observability services from the repository root:

```sh
docker compose -f compose.yaml -f deploy/observability/compose.yaml up -d --build --wait
```

The API is at `http://localhost:8080`, Grafana at `http://localhost:3000` (`admin` / `sqlwise_observability_local`), Prometheus at `http://localhost:9090`, and the API metrics endpoint at `http://localhost:8080/metrics`. The dashboard is provisioned under **SQLWise / SQLWise API**. Stop the stack with `docker compose -f compose.yaml -f deploy/observability/compose.yaml down`; omit `-v` to retain local data.

The Compose override sends API stdout and stderr through Docker's Fluentd driver to the Collector's `fluentforward` receiver on host port 24224. The API sends OTLP traces to the Collector on port 4318. The Collector exports logs to Loki and traces to Tempo. Prometheus scrapes actual API counters, duration histograms, and platform pool statistics. The override uses `fluentd-async` so an unavailable Collector does not prevent the API container from starting; logs buffered by Docker are bounded and may be lost during a prolonged outage.

For a local API run without the override, traces are written to stdout. Set `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` to send them to the Collector. SQL query arguments and driver error text are never exported. SQL literals and comments are masked before logs or spans are emitted. The SHA-256 hash identifies the original SQL text without printing it. Request logs use the registered route pattern, never the raw URL or query string.

The shared hook contract for the queued worker is `telemetry.InjectTrace(ctx)` when publishing and `telemetry.ExtractTrace(ctx, value)` before consuming. Persist only its `traceparent` and optional `tracestate` fields as observability metadata. Domain owners add queue, migration, batch, target pool, and worker SQL events where those operations occur. The dashboard currently shows API and platform pool measurements; job and target pool panels need the actual worker and migration measurements before they can display real data.

For a quick check, request `/health`, scrape `/metrics`, and search Loki for `{service_name="sqlwise"} | json | event="http.request"`. Copy a `trace_id` from an API log into Tempo's trace search. A database-backed request also produces `sql.start` and `sql.result` logs with the same `trace_id` and `correlation_id`.

## Local verification, 12 November 2026

An isolated Compose run served `GET /api/v1/auth/login-challenge` with a synthetic W3C trace ID `11111111111111111111111111111111`. Loki returned nine correlated API and SQL log events, all with correlation ID `6b1b88952d6291f616704024ee2f580b`; Tempo returned the HTTP span and four SQL child spans. Prometheus reported the `app:8080` target `up`, `/metrics` exposed request and platform pool values, and Grafana loaded the four provisioned API panels. The request's cookie and CSRF response values were omitted from saved evidence.

The local host already used port 5434, so the demo `target` container could not bind. The verification ran the API and observability services explicitly. Worker trace propagation, worker logs, job metrics, target pool metrics, and their dashboard panels await the corresponding domain implementation and hook points.
