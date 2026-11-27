# Logging, tracing and audit

The API (`cmd/server`) and the worker (`cmd/worker`) write structured JSON logs with Go's `log/slog` to stdout. Traces use OpenTelemetry and go to the same stream through the stdout exporter by default, or to an OTLP endpoint when `OTEL_EXPORTER_OTLP_ENDPOINT` is set (see [deploy/observability](../../deploy/observability/) for the Collector, Tempo, Loki, Prometheus and Grafana setup). Nothing here is stored in the platform database; the platform database keeps the durable records (requests, approvals, attempts, jobs and deliveries).

View them with `docker compose logs -f app worker`.

## What is logged

| Event | Where | When |
|---|---|---|
| `http.request` | API | Every request: method, route template (never the raw path or query), status, duration. |
| `audit` | API | Every state-changing `/api/v1` call: `action` (for example `databases.requests.approve`, `admin.groups.databases.grant`, `auth.login`), `outcome` (`success`, `denied` for 401/403, `rejected` for other 4xx, `error` for 5xx), actor, route IDs (`database_id`, `request_id`, `target_id`, …) and result IDs (`created_id`, `job_id`, `execution_id`, `group_id`). Request bodies are never logged. |
| `sql.start`, `sql.result` | API, worker | Every statement on the platform database and on PostgreSQL and MySQL targets, including read-only explorer queries, `BEGIN`, `COMMIT` and `ROLLBACK`: masked query, `sql_hash`, parameter types, duration, rows, SQLSTATE and, on failure, the masked server message at WARN. Context fields add `database`, and in the worker `job_id`, `migration_id`, `execution_id`, `operation`, `attempt`, `batch` and `batch_direction`. |
| `job.enqueued`, `job.claimed`, `job.lock.acquired`, `job.start`, `job.batch.committed`, `job.retry`, `job.deferred`, `job.reclaimed`, `job.finished` | API, worker | The queue lifecycle of every delivery, with job, delivery, request, execution and migration IDs and the attempt number. |
| `migration.execution.start`, `migration.execution.result` | worker | Every execution attempt: migration, execution and request IDs, operation, revision, candidate and applied version, executor, status, masked copy of the SQL, `sql_sha256` (the exact text is kept in migration history), duration, failed statement and masked error. |
| `migration.execution.precheck_failed`, `migration.execution.refused` | worker | Revalidation before any SQL (stale head, changed approvals, revoked access, unreachable target). A final refusal is also written to migration history with status `refused`. |
| `migration.recovery.start`, `migration.recovery.result`, `migration.recovery.refused` | worker | Batch resume and compensation. |

Every line written with a request or job context carries `trace_id`, `span_id` and `correlation_id`. The correlation ID is the HTTP request ID in the API and the job ID in the worker; the trace ID survives the queue hop.

## Masking

- SQL literals, numbers and quoted identifiers in logged queries become `[REDACTED]`; a statement containing `://` is masked entirely.
- Parameter values are never logged, only their Go types (`[REDACTED string]`).
- Quoted text in database and domain error messages is masked (`invalid input syntax for type integer: [REDACTED]`); `Detail` and `Where` of PostgreSQL errors, which can echo row values, are not logged at all.
- Passwords, session tokens, CSRF tokens and connection credentials never reach a log line; routes are logged as templates, so IDs in the URL appear only as named audit targets.

## Traces

`POST …/execute` (API, server span) → `job.run` (worker, consumer span, parent restored from the traceparent stored on the job) → `job.lock` (execution gate) and, per batch, `db.batch` → `db.query`. Platform and target statements are `db.query` client spans with `db.system.name`, `db.query.text` (masked), `db.namespace` (connection ID), returned rows, and on failure an `exception` event and `db.response.status_code` (SQLSTATE).

## Sample output

Captured from the Docker Compose stack on 27 November 2026. IDs are throwaway local values.

### (a) HTTP request and its audit line

Successful login: the request line and the audit line share `trace_id`/`correlation_id`.

```json
{
  "time": "2026-11-27T04:18:49.619775908Z",
  "level": "INFO",
  "msg": "http request",
  "service": "sqlwise-api",
  "event": "http.request",
  "method": "POST",
  "route": "/api/v1/auth/login",
  "status": "200",
  "duration_ms": 254,
  "actor": "c2810b208d2af6cf2229ef2e3181cbce",
  "correlation_id": "202dda1e29c934768e8454a1d50c8b0c",
  "trace_id": "5aee0513bb156c439cb003a82f9dc205",
  "span_id": "8300496e1e9fb475"
}
```

```json
{
  "time": "2026-11-27T04:18:49.619855161Z",
  "level": "INFO",
  "msg": "audit",
  "service": "sqlwise-api",
  "event": "audit",
  "action": "auth.login",
  "outcome": "success",
  "status": "200",
  "actor": "c2810b208d2af6cf2229ef2e3181cbce",
  "correlation_id": "202dda1e29c934768e8454a1d50c8b0c",
  "trace_id": "5aee0513bb156c439cb003a82f9dc205",
  "span_id": "8300496e1e9fb475"
}
```

Wrong-password login (401 → outcome `denied`, level WARN).

```json
{
  "time": "2026-11-27T04:18:49.899896043Z",
  "level": "INFO",
  "msg": "http request",
  "service": "sqlwise-api",
  "event": "http.request",
  "method": "POST",
  "route": "/api/v1/auth/login",
  "status": "401",
  "duration_ms": 241,
  "actor": "system",
  "correlation_id": "5efcc76df018b64692a577cda0a46691",
  "trace_id": "10491649554b74ffe0315189d439f941",
  "span_id": "57fdb16170e0646e"
}
```

```json
{
  "time": "2026-11-27T04:18:49.901195438Z",
  "level": "WARN",
  "msg": "audit",
  "service": "sqlwise-api",
  "event": "audit",
  "action": "auth.login",
  "outcome": "denied",
  "status": "401",
  "actor": "system",
  "correlation_id": "5efcc76df018b64692a577cda0a46691",
  "trace_id": "10491649554b74ffe0315189d439f941",
  "span_id": "57fdb16170e0646e"
}
```

Non-admin calling an admin endpoint (403 → `denied`).

```json
{
  "time": "2026-11-27T04:18:51.629768064Z",
  "level": "WARN",
  "msg": "audit",
  "service": "sqlwise-api",
  "event": "audit",
  "action": "admin.users.create",
  "outcome": "denied",
  "status": "403",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "correlation_id": "c5eedee55fb96e74952408c6b16c2a59",
  "trace_id": "05dd6ece0d33c22b41bf53a7da2610e2",
  "span_id": "70a068aa2f6c86ee"
}
```

### (b) Migration execution chain (one trace_id)

All lines below carry `trace_id = 81f6b28ea6f5be2eaabd983c8651b77d`. Batch size 10 over 25 rows → batches 1-3 plus an empty terminating batch 4.

**API http.request (POST …/execute, 202)**

```json
{
  "time": "2026-11-27T04:19:02.225788148Z",
  "level": "INFO",
  "msg": "http request",
  "service": "sqlwise-api",
  "event": "http.request",
  "method": "POST",
  "route": "/api/v1/databases/:databaseID/migrations/:migrationID/execute",
  "status": "202",
  "duration_ms": 47,
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "correlation_id": "8cee3d9f4d15c1f7ff9665550ad3de3a",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "d6e3c596e6b40b1d"
}
```

**API audit**

```json
{
  "time": "2026-11-27T04:19:02.225815516Z",
  "level": "INFO",
  "msg": "audit",
  "service": "sqlwise-api",
  "event": "audit",
  "action": "databases.migrations.execute",
  "outcome": "success",
  "status": "202",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "correlation_id": "8cee3d9f4d15c1f7ff9665550ad3de3a",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "d6e3c596e6b40b1d",
  "database_id": "e8fa20aed249ac53b616146d898b2b04",
  "migration_id": "42ddcef74323e5c47b4b90445e4a683d"
}
```

**API job.enqueued**

```json
{
  "time": "2026-11-27T04:19:02.225651178Z",
  "level": "INFO",
  "msg": "job enqueued",
  "service": "sqlwise-api",
  "event": "job.enqueued",
  "job_id": "98744e901b549618b3ecce2be1e79698",
  "request_id": "cb1a9c799c6e35735f7129aae57613e6",
  "execution_id": "c9e7cff046e93f0d5647e8f411b972c2",
  "database": "e8fa20aed249ac53b616146d898b2b04",
  "operation": "up",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "correlation_id": "8cee3d9f4d15c1f7ff9665550ad3de3a",
  "status": "queued",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "d6e3c596e6b40b1d"
}
```

**worker job.claimed**

```json
{
  "time": "2026-11-27T04:19:02.670582002Z",
  "level": "INFO",
  "msg": "job claimed",
  "service": "sqlwise-worker",
  "job_id": "98744e901b549618b3ecce2be1e79698",
  "delivery_id": "f3c5b31e049a41649ca7d90574ae234e",
  "request_id": "cb1a9c799c6e35735f7129aae57613e6",
  "execution_id": "c9e7cff046e93f0d5647e8f411b972c2",
  "migration_id": "42ddcef74323e5c47b4b90445e4a683d",
  "database": "e8fa20aed249ac53b616146d898b2b04",
  "operation": "up",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "worker": "worker-1",
  "correlation_id": "98744e901b549618b3ecce2be1e79698",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "bc6e477b4f8dc325",
  "event": "job.claimed",
  "attempt": 1,
  "previous_status": "queued"
}
```

**worker job.lock.acquired**

```json
{
  "time": "2026-11-27T04:19:02.681852371Z",
  "level": "INFO",
  "msg": "job gate acquired",
  "service": "sqlwise-worker",
  "job_id": "98744e901b549618b3ecce2be1e79698",
  "delivery_id": "f3c5b31e049a41649ca7d90574ae234e",
  "request_id": "cb1a9c799c6e35735f7129aae57613e6",
  "execution_id": "c9e7cff046e93f0d5647e8f411b972c2",
  "migration_id": "42ddcef74323e5c47b4b90445e4a683d",
  "database": "e8fa20aed249ac53b616146d898b2b04",
  "operation": "up",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "worker": "worker-1",
  "correlation_id": "98744e901b549618b3ecce2be1e79698",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "bc6e477b4f8dc325",
  "event": "job.lock.acquired",
  "lock_wait_ms": 11,
  "target_pid": 9248
}
```

**worker job.start**

```json
{
  "time": "2026-11-27T04:19:02.683943504Z",
  "level": "INFO",
  "msg": "job started",
  "service": "sqlwise-worker",
  "job_id": "98744e901b549618b3ecce2be1e79698",
  "delivery_id": "f3c5b31e049a41649ca7d90574ae234e",
  "request_id": "cb1a9c799c6e35735f7129aae57613e6",
  "execution_id": "c9e7cff046e93f0d5647e8f411b972c2",
  "migration_id": "42ddcef74323e5c47b4b90445e4a683d",
  "database": "e8fa20aed249ac53b616146d898b2b04",
  "operation": "up",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "worker": "worker-1",
  "correlation_id": "98744e901b549618b3ecce2be1e79698",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "bc6e477b4f8dc325",
  "event": "job.start"
}
```

**worker migration.execution.start**

```json
{
  "time": "2026-11-27T04:19:02.717060368Z",
  "level": "INFO",
  "msg": "migration execution",
  "service": "sqlwise-worker",
  "event": "migration.execution.start",
  "database": "e8fa20aed249ac53b616146d898b2b04",
  "migration_id": "42ddcef74323e5c47b4b90445e4a683d",
  "migration_title": "rd batch d",
  "execution_id": "c9e7cff046e93f0d5647e8f411b972c2",
  "request_id": "cb1a9c799c6e35735f7129aae57613e6",
  "operation": "up",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "draft_revision": 1,
  "candidate_version": 2,
  "mode": "batch",
  "status": "running",
  "statements": 1,
  "sql": "UPDATE [REDACTED].[REDACTED] SET [REDACTED] = [REDACTED];",
  "sql_sha256": "0855f27c955d1738cd08de390d06447bee0e85720c9ad97969074bd9cd0bb782",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "bc6e477b4f8dc325",
  "correlation_id": "98744e901b549618b3ecce2be1e79698"
}
```

**worker sql.start (batch 1 forward UPDATE on the target)**

```json
{
  "time": "2026-11-27T04:19:02.846649145Z",
  "level": "INFO",
  "msg": "sql statement",
  "service": "sqlwise-worker",
  "event": "sql.start",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "correlation_id": "98744e901b549618b3ecce2be1e79698",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "b03cae4dc99ae2b8",
  "query": "WITH batch AS (\n\t\t\tSELECT [REDACTED]::bigint AS k, [REDACTED]::text AS before FROM [REDACTED].[REDACTED]\n\t\t\tWHERE ($1::bigint IS NULL OR [REDACTED] > $1) AND [REDACTED] <= $2 ORDER BY [REDACTED] LIMIT $3 FOR UPDATE\n\t\t), changed AS (\n\t\t\tUPDATE [REDACTED].[REDACTED] t SET [REDACTED] = $4::text::text FROM batch\n\t\t\tWHERE t.[REDACTED] = batch.k RETURNING batch.k, batch.before, t.[REDACTED]::text AS after\n\t\t), saved AS (\n\t\t\tINSERT INTO sqlwise_internal.batch_rows (execution_id, batch_no, row_key, before_value, after_value)\n\t\t\tSELECT $5, $6, k, before, after FROM changed RETURNING row_key\n\t\t)\n\t\tSELECT count(*), COALESCE(min(row_key), [REDACTED]), COALESCE(max(row_key), [REDACTED]) FROM saved",
  "sql_hash": "cba18f3090648df00c0c8de3a3bbe32e196077f874b45d4fdd8dc508aaf39d5d",
  "params": [
    "[REDACTED *int64]",
    "[REDACTED int64]",
    "[REDACTED int]",
    "[REDACTED string]",
    "[REDACTED string]",
    "[REDACTED int]"
  ],
  "status": "started",
  "duration_ms": 0,
  "rows": 0,
  "sqlstate": "",
  "database": "e8fa20aed249ac53b616146d898b2b04",
  "job_id": "98744e901b549618b3ecce2be1e79698",
  "migration_id": "42ddcef74323e5c47b4b90445e4a683d",
  "execution_id": "c9e7cff046e93f0d5647e8f411b972c2",
  "operation": "up",
  "attempt": 1,
  "batch": 1,
  "batch_direction": "forward"
}
```

**worker sql.result (same statement)**

```json
{
  "time": "2026-11-27T04:19:02.850913923Z",
  "level": "INFO",
  "msg": "sql statement",
  "service": "sqlwise-worker",
  "event": "sql.result",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "correlation_id": "98744e901b549618b3ecce2be1e79698",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "b03cae4dc99ae2b8",
  "query": "WITH batch AS (\n\t\t\tSELECT [REDACTED]::bigint AS k, [REDACTED]::text AS before FROM [REDACTED].[REDACTED]\n\t\t\tWHERE ($1::bigint IS NULL OR [REDACTED] > $1) AND [REDACTED] <= $2 ORDER BY [REDACTED] LIMIT $3 FOR UPDATE\n\t\t), changed AS (\n\t\t\tUPDATE [REDACTED].[REDACTED] t SET [REDACTED] = $4::text::text FROM batch\n\t\t\tWHERE t.[REDACTED] = batch.k RETURNING batch.k, batch.before, t.[REDACTED]::text AS after\n\t\t), saved AS (\n\t\t\tINSERT INTO sqlwise_internal.batch_rows (execution_id, batch_no, row_key, before_value, after_value)\n\t\t\tSELECT $5, $6, k, before, after FROM changed RETURNING row_key\n\t\t)\n\t\tSELECT count(*), COALESCE(min(row_key), [REDACTED]), COALESCE(max(row_key), [REDACTED]) FROM saved",
  "sql_hash": "cba18f3090648df00c0c8de3a3bbe32e196077f874b45d4fdd8dc508aaf39d5d",
  "params": [
    "[REDACTED *int64]",
    "[REDACTED int64]",
    "[REDACTED int]",
    "[REDACTED string]",
    "[REDACTED string]",
    "[REDACTED int]"
  ],
  "status": "success",
  "duration_ms": 4,
  "rows": 1,
  "sqlstate": "",
  "database": "e8fa20aed249ac53b616146d898b2b04",
  "job_id": "98744e901b549618b3ecce2be1e79698",
  "migration_id": "42ddcef74323e5c47b4b90445e4a683d",
  "execution_id": "c9e7cff046e93f0d5647e8f411b972c2",
  "operation": "up",
  "attempt": 1,
  "batch": 1,
  "batch_direction": "forward"
}
```

**worker job.batch.committed**

```json
{
  "time": "2026-11-27T04:19:02.872340687Z",
  "level": "INFO",
  "msg": "job batch committed",
  "service": "sqlwise-worker",
  "job_id": "98744e901b549618b3ecce2be1e79698",
  "delivery_id": "f3c5b31e049a41649ca7d90574ae234e",
  "request_id": "cb1a9c799c6e35735f7129aae57613e6",
  "execution_id": "c9e7cff046e93f0d5647e8f411b972c2",
  "migration_id": "42ddcef74323e5c47b4b90445e4a683d",
  "database": "e8fa20aed249ac53b616146d898b2b04",
  "operation": "up",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "worker": "worker-1",
  "correlation_id": "98744e901b549618b3ecce2be1e79698",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "bc6e477b4f8dc325",
  "event": "job.batch.committed",
  "direction": "forward",
  "batches": 1,
  "rows": 10,
  "total_rows": 25
}
```

**worker migration.execution.result**

```json
{
  "time": "2026-11-27T04:19:02.898141696Z",
  "level": "INFO",
  "msg": "migration execution",
  "service": "sqlwise-worker",
  "event": "migration.execution.result",
  "database": "e8fa20aed249ac53b616146d898b2b04",
  "migration_id": "42ddcef74323e5c47b4b90445e4a683d",
  "migration_title": "rd batch d",
  "execution_id": "c9e7cff046e93f0d5647e8f411b972c2",
  "request_id": "cb1a9c799c6e35735f7129aae57613e6",
  "operation": "up",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "draft_revision": 1,
  "candidate_version": 2,
  "mode": "batch",
  "status": "succeeded",
  "statements": 1,
  "sql": "UPDATE [REDACTED].[REDACTED] SET [REDACTED] = [REDACTED];",
  "sql_sha256": "0855f27c955d1738cd08de390d06447bee0e85720c9ad97969074bd9cd0bb782",
  "applied_version": 2,
  "duration_ms": 191,
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "bc6e477b4f8dc325",
  "correlation_id": "98744e901b549618b3ecce2be1e79698"
}
```

**worker job.finished**

```json
{
  "time": "2026-11-27T04:19:02.901813118Z",
  "level": "INFO",
  "msg": "job finished",
  "service": "sqlwise-worker",
  "job_id": "98744e901b549618b3ecce2be1e79698",
  "delivery_id": "f3c5b31e049a41649ca7d90574ae234e",
  "request_id": "cb1a9c799c6e35735f7129aae57613e6",
  "execution_id": "c9e7cff046e93f0d5647e8f411b972c2",
  "migration_id": "42ddcef74323e5c47b4b90445e4a683d",
  "database": "e8fa20aed249ac53b616146d898b2b04",
  "operation": "up",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "worker": "worker-1",
  "correlation_id": "98744e901b549618b3ecce2be1e79698",
  "trace_id": "81f6b28ea6f5be2eaabd983c8651b77d",
  "span_id": "bc6e477b4f8dc325",
  "event": "job.finished",
  "status": "succeeded",
  "effect": "committed",
  "attempt": 1,
  "error": ""
}
```

### (c) A failed statement

The migration `INSERT INTO items VALUES ('<literal>');` targets an integer column. The literal never reaches the log: the SQL and the server error are both masked, while the statement position and SQLSTATE stay readable. The failure is WARN on the SQL line, the execution result and the job outcome, which share one `trace_id`.

```json
{
  "time": "2026-11-27T05:04:09.158751959Z",
  "level": "WARN",
  "msg": "sql statement",
  "service": "sqlwise-worker",
  "event": "sql.result",
  "actor": "c2810b208d2af6cf2229ef2e3181cbce",
  "correlation_id": "64d04b085c329e99ca493f4428bd7280",
  "trace_id": "151c0a721b6ad453a3d2c1015aa221b0",
  "span_id": "e3691b8bf3b62320",
  "query": "INSERT INTO items VALUES ([REDACTED])",
  "sql_hash": "46265bf3ee69d8dd0c53fbde0d75cf7e5f871503dfe0a8588adef111ebfe2f0c",
  "params": [],
  "status": "failure",
  "duration_ms": 2,
  "rows": 0,
  "sqlstate": "22P02",
  "error": "invalid input syntax for type integer: [REDACTED]",
  "database": "f1f9bacb2fe3b3d0f762c6fda474aafe",
  "job_id": "64d04b085c329e99ca493f4428bd7280",
  "migration_id": "6377fee9393ba692921904381c5a6849",
  "execution_id": "5cc879a40c3e40fb7b27cdb5df156995",
  "operation": "up",
  "attempt": 1
}
```

```json
{
  "time": "2026-11-27T05:04:09.161252159Z",
  "level": "WARN",
  "msg": "migration execution",
  "service": "sqlwise-worker",
  "event": "migration.execution.result",
  "database": "f1f9bacb2fe3b3d0f762c6fda474aafe",
  "migration_id": "6377fee9393ba692921904381c5a6849",
  "migration_title": "fx bad",
  "execution_id": "5cc879a40c3e40fb7b27cdb5df156995",
  "request_id": "72d2aad32eb86ddb6eb63ed986a7b7fa",
  "operation": "up",
  "actor": "c2810b208d2af6cf2229ef2e3181cbce",
  "draft_revision": 1,
  "candidate_version": 2,
  "mode": "transaction",
  "status": "failed",
  "statements": 1,
  "sql": "INSERT INTO items VALUES ([REDACTED]);",
  "sql_sha256": "757122c67aad34fb6584ac6943d5d695f49d271eff78f0c631427a3886d941d1",
  "duration_ms": 9,
  "failed_statement": 1,
  "error": "Statement 1 failed: invalid input syntax for type integer: [REDACTED] (SQLSTATE 22P02). All changes were rolled back.",
  "trace_id": "151c0a721b6ad453a3d2c1015aa221b0",
  "span_id": "43704240e7e49304",
  "correlation_id": "64d04b085c329e99ca493f4428bd7280"
}
```

```json
{
  "time": "2026-11-27T05:04:09.167319019Z",
  "level": "WARN",
  "msg": "job finished",
  "service": "sqlwise-worker",
  "job_id": "64d04b085c329e99ca493f4428bd7280",
  "delivery_id": "196e2a02d4a29f5a2dea1d1866f19cdb",
  "request_id": "72d2aad32eb86ddb6eb63ed986a7b7fa",
  "execution_id": "5cc879a40c3e40fb7b27cdb5df156995",
  "migration_id": "6377fee9393ba692921904381c5a6849",
  "database": "f1f9bacb2fe3b3d0f762c6fda474aafe",
  "operation": "up",
  "actor": "c2810b208d2af6cf2229ef2e3181cbce",
  "worker": "worker-1",
  "correlation_id": "64d04b085c329e99ca493f4428bd7280",
  "trace_id": "151c0a721b6ad453a3d2c1015aa221b0",
  "span_id": "43704240e7e49304",
  "event": "job.finished",
  "status": "failed",
  "effect": "none",
  "attempt": 1,
  "error": "Statement 1 failed: invalid input syntax for type integer: [REDACTED] (SQLSTATE 22P02). All changes were rolled back."
}
```

### (d) Spans from the stdout exporter (same trace)

Hierarchy: `POST …/execute` (API, root) → `job.run` (worker, CONSUMER) → { `job.lock`, `db.batch` ×4 → `db.query` }. `job.lock` is a sibling of the `db.batch` spans (it covers the gate acquisition only), not their parent. Resource/scope boilerplate trimmed; long query text truncated.

**HTTP server span (API)**

```json
{
  "Name": "POST /api/v1/databases/:databaseID/migrations/:migrationID/execute",
  "TraceID": "81f6b28ea6f5be2eaabd983c8651b77d",
  "SpanID": "d6e3c596e6b40b1d",
  "ParentSpanID": "0000000000000000",
  "SpanKind": 2,
  "StartTime": "2026-11-27T04:19:02.178601722Z",
  "EndTime": "2026-11-27T04:19:02.225828099Z",
  "Attributes": {
    "http.request.method": "POST",
    "http.response.status_code": 202,
    "http.route": "/api/v1/databases/:databaseID/migrations/:migrationID/execute"
  },
  "Status": {
    "Code": "Unset",
    "Description": ""
  },
  "service.name": "sqlwise-api"
}
```

**job.run (worker)**

```json
{
  "Name": "job.run",
  "TraceID": "81f6b28ea6f5be2eaabd983c8651b77d",
  "SpanID": "bc6e477b4f8dc325",
  "ParentSpanID": "d6e3c596e6b40b1d",
  "SpanKind": 5,
  "StartTime": "2026-11-27T04:19:02.670552465Z",
  "EndTime": "2026-11-27T04:19:02.903171197Z",
  "Attributes": {
    "sqlwise.job.id": "98744e901b549618b3ecce2be1e79698",
    "sqlwise.execution.id": "c9e7cff046e93f0d5647e8f411b972c2",
    "sqlwise.job.operation": "up",
    "sqlwise.database.id": "e8fa20aed249ac53b616146d898b2b04",
    "sqlwise.delivery.id": "f3c5b31e049a41649ca7d90574ae234e",
    "sqlwise.job.status": "succeeded",
    "sqlwise.job.effect": "committed"
  },
  "Status": {
    "Code": "Unset",
    "Description": ""
  },
  "service.name": "sqlwise-worker"
}
```

**job.lock**

```json
{
  "Name": "job.lock",
  "TraceID": "81f6b28ea6f5be2eaabd983c8651b77d",
  "SpanID": "c78e3a75c277d4e0",
  "ParentSpanID": "bc6e477b4f8dc325",
  "SpanKind": 1,
  "StartTime": "2026-11-27T04:19:02.670602942Z",
  "EndTime": "2026-11-27T04:19:02.68186376Z",
  "Attributes": {},
  "Status": {
    "Code": "Unset",
    "Description": ""
  },
  "service.name": "sqlwise-worker"
}
```

**db.batch, batch 1**

```json
{
  "Name": "db.batch",
  "TraceID": "81f6b28ea6f5be2eaabd983c8651b77d",
  "SpanID": "04181a840f3a3fb0",
  "ParentSpanID": "bc6e477b4f8dc325",
  "SpanKind": 1,
  "StartTime": "2026-11-27T04:19:02.843972675Z",
  "EndTime": "2026-11-27T04:19:02.866724698Z",
  "Attributes": {
    "sqlwise.execution.id": "c9e7cff046e93f0d5647e8f411b972c2",
    "sqlwise.batch.direction": "forward",
    "sqlwise.batch.number": 1,
    "sqlwise.batch.rows": 10
  },
  "Status": {
    "Code": "Unset",
    "Description": ""
  },
  "service.name": "sqlwise-worker"
}
```

**db.query inside batch 1**

```json
{
  "Name": "db.query",
  "TraceID": "81f6b28ea6f5be2eaabd983c8651b77d",
  "SpanID": "b03cae4dc99ae2b8",
  "ParentSpanID": "04181a840f3a3fb0",
  "SpanKind": 3,
  "StartTime": "2026-11-27T04:19:02.846634994Z",
  "EndTime": "2026-11-27T04:19:02.850984919Z",
  "Attributes": {
    "db.system.name": "postgresql",
    "db.query.text": "WITH batch AS (    SELECT [REDACTED]::bigint AS k, [REDACTED]::text AS before FROM [REDACTED].[REDACTED]    WHERE ($1::bigint IS NULL OR [REDACTED] > $1) AND [R \u2026",
    "db.namespace": "e8fa20aed249ac53b616146d898b2b04",
    "db.response.returned_rows": 1
  },
  "Status": {
    "Code": "Unset",
    "Description": ""
  },
  "service.name": "sqlwise-worker"
}
```

Failing spans: `db.query` with an `exception` event and `db.response.status_code`.

```json
{
  "Name": "db.query",
  "TraceID": "e62ab10389783c109cbd44d8da3f6b42",
  "SpanID": "b6f7109f7513fdaf",
  "ParentSpanID": "8275ba63f8d94c85",
  "SpanKind": 3,
  "StartTime": "2026-11-27T04:19:01.602173937Z",
  "EndTime": "2026-11-27T04:19:01.603122371Z",
  "Attributes": {
    "db.system.name": "postgresql",
    "db.query.text": "INSERT INTO rd_ok_d (id) VALUES ([REDACTED])",
    "db.namespace": "e8fa20aed249ac53b616146d898b2b04",
    "db.response.status_code": "22P02",
    "db.response.returned_rows": 0
  },
  "Status": {
    "Code": "Error",
    "Description": "query failed"
  },
  "service.name": "sqlwise-worker",
  "Events": [
    {
      "Name": "exception",
      "Attributes": {
        "exception.type": "*errors.errorString",
        "exception.message": "invalid input syntax for type integer: [REDACTED]"
      }
    }
  ]
}
```

```json
{
  "Name": "db.query",
  "TraceID": "3bc3f9c0f3f79dab1f001caf6d1b2ed4",
  "SpanID": "b497cd4d4b241037",
  "ParentSpanID": "4ac29e516bb488e8",
  "SpanKind": 3,
  "StartTime": "2026-11-27T04:19:04.092321946Z",
  "EndTime": "2026-11-27T04:19:04.095217982Z",
  "Attributes": {
    "db.system.name": "postgresql",
    "db.query.text": "WITH batch AS (    SELECT [REDACTED]::bigint AS k, [REDACTED]::text AS before FROM [REDACTED].[REDACTED]    WHERE ($1::bigint IS NULL OR [REDACTED] > $1) AND [R \u2026",
    "db.namespace": "e8fa20aed249ac53b616146d898b2b04",
    "db.response.status_code": "23514",
    "db.response.returned_rows": 0
  },
  "Status": {
    "Code": "Error",
    "Description": "query failed"
  },
  "service.name": "sqlwise-worker",
  "Events": [
    {
      "Name": "exception",
      "Attributes": {
        "exception.type": "*errors.errorString",
        "exception.message": "new row for relation [REDACTED] violates check constraint [REDACTED]"
      }
    }
  ]
}
```

### (e) MySQL statement (explorer schema read on `rd_obs_mysql_d`)

```json
{
  "time": "2026-11-27T04:19:05.563716887Z",
  "level": "INFO",
  "msg": "sql statement",
  "service": "sqlwise-api",
  "event": "sql.start",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "correlation_id": "f1438d90d9b8d9b689bc069383a53006",
  "trace_id": "8ff801e45f6ba48103125955d57699f7",
  "span_id": "cb7f2164162a9373",
  "query": "SELECT c.TABLE_NAME, c.COLUMN_NAME, c.COLUMN_TYPE, c.IS_NULLABLE = [REDACTED], c.COLUMN_DEFAULT\n\t\tFROM information_schema.COLUMNS c JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME AND t.TABLE_TYPE = [REDACTED]\n\t\tWHERE c.TABLE_SCHEMA = ? ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION",
  "sql_hash": "09d699943a7aee20343f86ce2257e78e91d81e2c7ce6aca4dbdfb919188cf0d7",
  "params": [
    "[REDACTED string]"
  ],
  "status": "started",
  "duration_ms": 0,
  "rows": 0,
  "sqlstate": "",
  "database": "3936b657f42353fa2e470ea563da2afd"
}
```

```json
{
  "time": "2026-11-27T04:19:05.570803734Z",
  "level": "INFO",
  "msg": "sql statement",
  "service": "sqlwise-api",
  "event": "sql.result",
  "actor": "b748cccda8fd6a194bf63bc60b23e105",
  "correlation_id": "f1438d90d9b8d9b689bc069383a53006",
  "trace_id": "8ff801e45f6ba48103125955d57699f7",
  "span_id": "cb7f2164162a9373",
  "query": "SELECT c.TABLE_NAME, c.COLUMN_NAME, c.COLUMN_TYPE, c.IS_NULLABLE = [REDACTED], c.COLUMN_DEFAULT\n\t\tFROM information_schema.COLUMNS c JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME AND t.TABLE_TYPE = [REDACTED]\n\t\tWHERE c.TABLE_SCHEMA = ? ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION",
  "sql_hash": "09d699943a7aee20343f86ce2257e78e91d81e2c7ce6aca4dbdfb919188cf0d7",
  "params": [
    "[REDACTED string]"
  ],
  "status": "success",
  "duration_ms": 7,
  "rows": -1,
  "sqlstate": "",
  "database": "3936b657f42353fa2e470ea563da2afd"
}
```

```json
{
  "Name": "db.query",
  "TraceID": "8ff801e45f6ba48103125955d57699f7",
  "SpanID": "cb7f2164162a9373",
  "ParentSpanID": "9ca121df53faeb53",
  "SpanKind": 3,
  "StartTime": "2026-11-27T04:19:05.563694509Z",
  "EndTime": "2026-11-27T04:19:05.570884545Z",
  "Attributes": {
    "db.system.name": "mysql",
    "db.query.text": "SELECT c.TABLE_NAME, c.COLUMN_NAME, c.COLUMN_TYPE, c.IS_NULLABLE = [REDACTED], c.COLUMN_DEFAULT   FROM information_schema.COLUMNS c JOIN information_schema.TABL \u2026",
    "db.namespace": "3936b657f42353fa2e470ea563da2afd"
  },
  "Status": {
    "Code": "Unset",
    "Description": ""
  },
  "service.name": "sqlwise-api"
}
```
