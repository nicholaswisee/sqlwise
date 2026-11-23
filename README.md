# sqlwise

sqlwise is a database management web app built for the IF3110 Web Application Development course. Teams can explore databases, manage access, plan schema changes, and track migrations from one interface.

## Tech stack

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/stack-diagram-dark.png">
  <img src="docs/stack-diagram.png" alt="SQLwise stack: React and TypeScript browser UI with TanStack Start, Router, Query, and Tailwind CSS; same-origin Go and Gin JSON API; PostgreSQL platform database and PostgreSQL or MySQL target databases; Vite+ builds a static SPA served by Go in Docker without a Node.js production server.">
</picture>

[Open the standalone diagram](docs/stack-diagram.html).

## Features

### Milestone 1 foundation

- **Authentication and sessions:** Sign in and manage authenticated sessions.
- **Access control:** Manage users, groups, and view or edit permissions for each database.
- **Connection management:** Register, test, enable, and disable database connections.
- **Database explorer:** Browse database schemas, tables, columns, and data.
- **Linear migrations:** Write SQL drafts, apply them in order, and track database versions and execution results.

### Milestone 2 additions

- **Responsive SPA:** Use the Go JSON API from the SPA for both existing pages, including login and administration, and new migration workflows.
- **Rollback, forward, and history:** Define SQL up and down or mark a migration irreversible with a reason. Roll back the latest active reversible migration and reapply changes in order. Committed history is separate from attempts, and **Hide rollback** filters out canceled execution pairs without deleting records.
- **Review and approval:** Requests need approval from the leader of every group with view or edit access, including for rollback and forward.
- **Asynchronous execution:** A durable queue and Go worker run approved migrations, report progress in the SPA, and support checkpointed batches with reviewed resume or compensation. See [Jobs and large-data benchmark](#jobs-and-large-data-benchmark).
- **Activity logging and tracing:** Structured Go logs and OpenTelemetry traces correlate API requests, queued jobs, worker activity, and database operations.

Milestone 2 reviewed execution is PostgreSQL-only. MySQL connections, browsing, and existing migration records remain available, but there is no synchronous MySQL fallback.

## Bonus features

### Milestone 1

These statuses describe what shipped in Milestone 1.

| Feature       | Status                  | What it does                                                                                                              |
| ------------- | ----------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| MySQL support | Complete                | Adds MySQL connections, browsing, and migrations. Partial or uncertain migration outcomes can be reviewed and reconciled. |
| Schema diff   | Complete for PostgreSQL | Compares a live schema with a desired JSON snapshot and generates a migration draft with change warnings.                 |
| Query plans   | Complete for PostgreSQL | Shows Explain plans and supports Analyze for SELECT queries on an authorized sandbox connection.                          |

### Milestone 2

| Feature           | Status           | What it does                                                                                                                                                                                             |
| ----------------- | ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Observability     | Partial          | [Collector, Prometheus, and Grafana configuration](deploy/observability/) is included. The provisioned dashboard covers the API and platform database pool; queue and migration panels are not included. |
| k6 stress testing | Fixtures only    | [`cmd/k6fixture`](cmd/k6fixture/) prepares approved requests. No k6 workload or staged stress-test results are included.                                                                                 |
| CodeQL SAST       | Scanning enabled | The [CodeQL workflow](.github/workflows/codeql.yml) scans Go and JavaScript/TypeScript. Findings triage is not documented.                                                                               |

The separate early-submission bonus depends on the submission time and is not claimed here.

## Run with Docker

Install Docker with Compose, then run:

```sh
docker compose up -d
```

Open <http://localhost:8080/login>. On a **new platform database**, the default SQLwise **web login** is:

- Username: `admin`
- Password: `SqlwiseAdminLocal2026!`

These are application credentials, not database credentials. Existing administrator accounts keep their passwords. To set different initial credentials, copy `.env.example` to `.env` and change `SQLWISE_ADMIN_USERNAME` and `SQLWISE_ADMIN_PASSWORD` before the first startup.

Use `docker compose logs -f app` for logs, `docker compose up -d --build` after source changes, and `docker compose down` to stop the stack without removing database volumes. The image builds the SPA with Node and serves its static client from Go; no Node application server runs in production.

## Run manually

Install the Go version in `go.mod`, a C compiler, Node.js, and Vite+ (`vp`). Use a POSIX shell. Start the databases and create the platform administrator once:

```sh
docker compose up -d --wait postgres target
test -f .env || cp .env.example .env
set -a
. ./.env
set +a
go run ./cmd/admin -username admin
```

The admin command prompts for a password. Skip it if an administrator already exists.

When upgrading, stop the API and worker and back up the platform database. Run `go run ./cmd/admin -schema-only` before restarting both from the same version. Docker Compose applies the schema during app startup.

Start the SPA development server in one terminal:

```sh
vp install
pnpm run spa:dev
```

In another terminal, load the environment and start the Go server:

```sh
set -a
. ./.env
set +a
go run ./cmd/server
```

Approved migrations run only in the worker. In a third terminal, load the environment and start it:

```sh
set -a
. ./.env
set +a
go run ./cmd/worker
```

Open the URL printed by the SPA development server and sign in with the administrator password you chose. Its `/api/v1` requests proxy to Go on port 8080, keeping cookie and CSRF handling same-origin. The Go process can run API-only during development without a frontend build. To serve the built SPA directly from Go on <http://localhost:8080/login>, run `pnpm run spa:build` first.

## SPA and retained legacy frontend

The active frontend lives in `web/spa` and uses TanStack Start in static SPA mode, TanStack Router, and TanStack Query. It calls Go JSON endpoints under `/api/v1`; Go still owns authentication, session rotation, permissions, connections, queries, and migration execution. The frontend foundation follows Better T Stack's frontend-only approach, without adding its JavaScript backend, ORM, or authentication service.

The previous templ/HTMX frontend remains in `internal/web/views`, `web/src`, and the legacy Vite configuration. Its templates, generated Go, HTML handlers, and regression tests are retained; production no longer mounts those HTML routes. `vp build` and `go tool templ generate` still check the legacy frontend. The optional Compose `legacy` profile retains the old frontend asset-development service, not a second production UI.

## Demo target databases

The Docker stack includes a seeded PostgreSQL target. Register it under **Admin → Connections**. Start the optional seeded MySQL target with `docker compose --profile bonus up -d mysql`.

| Target     | Host and port in Docker | Host and port for manual Go | Database       |
| ---------- | ----------------------- | --------------------------- | -------------- |
| PostgreSQL | `target:5432`           | `localhost:5434`            | `sqlwise_demo` |
| MySQL      | `mysql:3306`            | `localhost:3307`            | `sqlwise_demo` |

Both demo targets use username `sqlwise_target`, password `sqlwise_target_dev_only`, and TLS disabled. Grant a group access after registering a target.

## Jobs and large-data benchmark

Approved executions are queued durably and run by `cmd/worker`, never inside an HTTP request. `202 Accepted` means the job was stored, not that the migration succeeded; poll `GET /api/v1/databases/:id/jobs/:jobID` (or open **Jobs** in the workspace) for `status` (`queued`, `running`, `succeeded`, `failed`, `blocked`) and `effect` on the target (`none`, `committed`, `partial`, `unknown`), plus `attempts`, `queued_at`, `started_at`, `finished_at` and committed batches/rows.

- One worker at a time holds a target's execution gate (a session advisory lock on a dedicated target connection) for the whole job, so the same database is serialized, including between batch commits, while different databases run in parallel.
- Confirmed transient failures before any target write are retried at most 3 times in total, after 1 s and 2 s. SQL, access and stale-request failures are final.
- A batch migration ("Run as checkpointed batches") commits each batch with its checkpoint and saved row values in one target transaction. If it stops after committing only some batches, those changes stay and later jobs on that database wait for recovery. An approved **resume** finishes the migration so it can enter committed history; an approved **compensate** restores saved values newest batch first and stops rather than overwrite rows changed since.
- Worker settings use `WORKER_*` variables (`WORKER_ID`, `WORKER_CONCURRENCY`, `WORKER_LEASE`, `WORKER_LOCK_WAIT`, `WORKER_JOB_TIMEOUT`, `WORKER_SHUTDOWN_GRACE`, `WORKER_RECHECK`, `WORKER_METRICS_ADDR`); the worker serves `/metrics` and `/health` on port 9091.

Recovery runbook:

| Situation                                    | What happens                                                                                                                                                                       | Action                                                                                                                                              |
| -------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| Worker died                                  | Its target session ends, releasing the gate. After the lease expires another worker reclaims the job and checks the target receipt or batch ledger instead of re-running SQL.      | None; a restart with the same `WORKER_ID` reclaims at once.                                                                                         |
| Worker stalled but alive                     | Another worker claims the expired job but waits for the gate, without spending attempts.                                                                                           | Stop the stalled process if it is hung.                                                                                                             |
| Target unreachable before execution          | Confirmed no-effect connection failures are retried after 1 s and 2 s.                                                                                                             | Restore connectivity; if the job already failed, create a new request.                                                                              |
| Outcome unknown                              | `blocked/unknown`; the worker reacquires the target lock and checks the receipt or batch checkpoints. It does not repeat the SQL or offer recovery while the outcome is uncertain. | Restore connectivity if the target stays unreachable.                                                                                               |
| Batch stopped with confirmed partial changes | `blocked/partial`; later jobs on that database wait until recovery succeeds.                                                                                                       | Request resume or compensation from the job page and get it approved. If any batches have already been compensated, only compensation can continue. |

Large-data benchmark (`cmd/batchbench`, disposable database only):

```sh
docker compose up -d --wait postgres-test
go run ./cmd/batchbench -admin-url 'postgres://sqlwise_test:sqlwise_test_only@localhost:5435/sqlwise_test?sslmode=disable' \
  -rows 1000000 -batch 50000 -fail-at 8
```

Recorded on 21 November 2026: Docker Desktop on WSL2, host with 28 CPUs and 31 GiB RAM, no container limits, `postgres:17-alpine` (PostgreSQL 17.11, default settings, `fsync` and `synchronous_commit` on). Table `bench.accounts (id bigint PRIMARY KEY, status text, balance numeric(12,2), email text)`, 1,000,000 rows (189 MB), only the primary-key index.

| Measurement                                                 | Result                                                        |
| ----------------------------------------------------------- | ------------------------------------------------------------- |
| Whole-table `UPDATE bench.accounts SET status = 'archived'` | 2.11 s (500,000 rows: 1.11 s)                                 |
| Same dataset, 50,000-row batches                            | 20.70 s, 20 batches, 1,000,000 rows                           |
| Peak Go heap during the batched run                         | 1.2 MB                                                        |
| Compensation of all 20 batches (reverse order)              | 3.59 s, every row restored                                    |
| Failure injected before batch 8                             | 7 batches / 350,000 rows stayed committed, the rest untouched |
| Resume on a new gate                                        | started at batch 8, 13.30 s, each row updated exactly once    |

Batching is about 10× slower than one statement because every batch also stores before/after values and a checkpoint and pays its own commit. In exchange each transaction is bounded, progress survives failures, and committed work can be resumed or compensated. Memory stays flat because rows never leave the server. One run per size on a shared developer machine; timings vary with disk and WAL.

Load-test fixtures for k6 go through real login, review and approval:

```sh
K6_EDITOR=editor:password K6_LEADERS=lead1:password,lead2:password \
  go run ./cmd/k6fixture prepare -url http://localhost:8080 -databases <id1>,<id2> -per-database 1 -run r1 > fixtures.json
go run ./cmd/k6fixture reset -target-url <disposable target URL> -run r1
```

Each fixture gives `execute_path`, `request_id` and a unique `idempotency_key`. Requests prepared together for one database share a head, so only the first can succeed; the others are the expected stale-head conflicts. `reset` drops the run's `k6_` tables on the target; platform history is append-only and is not deleted.

## Tests

The PostgreSQL and MySQL test databases are separate from the app and demo databases. Start either service as needed:

```sh
docker compose up -d --wait postgres-test
docker compose up -d --wait mysql-test
```

Run the integration suite with both test databases:

```sh
TEST_DATABASE_URL='postgres://sqlwise_test:sqlwise_test_only@localhost:5435/sqlwise_test?sslmode=disable' \
TEST_MYSQL_URL='root:mysql_test_only@tcp(localhost:3308)/' \
go test -race -p 1 -shuffle=on -count=1 ./...
```

Set only `TEST_DATABASE_URL` or `TEST_MYSQL_URL` to run one database's integration tests. Tests for a database without its variable are skipped. Stop the test services with `docker compose stop postgres-test mysql-test`.

Other development checks:

```sh
go tool templ generate
go vet ./...
vp check
vp build
pnpm run spa:check
pnpm run spa:test
pnpm run spa:build
```

Browser checks use Chromium at 1440px, 390px, and 320px:

```sh
vp exec playwright install chromium
# Keep the Go API running on port 8080; Playwright starts the SPA dev server.
pnpm run spa:e2e
# Or test a separately running Go server serving the production SPA build:
SQLWISE_E2E_URL=http://localhost:8080 pnpm run spa:e2e
```

Set `SQLWISE_E2E_USERNAME` and `SQLWISE_E2E_PASSWORD` if the test administrator differs from the local defaults. Two browser harness tests are development-only and are skipped against the production URL; all application flow tests still run.

## Team

| Student ID | Member                         | Main contributions                                                                                   |
| ---------- | ------------------------------ | ---------------------------------------------------------------------------------------------------- |
| 13524034   | Jason Edward Salim             | Database connections and explorer, durable queue and worker, batch recovery, Jobs UI and benchmark   |
| 13524037   | Nicholas Wise Saragih Sumbayak | Authentication, sessions, access control, administration, frontend foundation, CI                    |
| 13524106   | Michael James Liman            | Migration editor, approval workflow, rollback and forward, committed history, schema diff and CodeQL |
