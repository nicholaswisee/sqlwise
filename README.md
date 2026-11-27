# sqlwise

SQLwise is a database management app for the IF3110 Web Application Development course. Explore databases, manage group access, and review, run, or roll back SQL migrations from the browser.

## Quick start

From the repository root, with Docker Compose installed:

```sh
docker compose up -d
```

Open <http://localhost:8080/login>. On a fresh **local** platform database, sign in as `admin` with `SqlwiseAdminLocal2026!`.

See the [development guide](docs/development.md) to configure the initial admin, connect demo targets, or run the API, worker, and frontend separately.

### Seed a disposable demo

The [demo seed guide](deploy/demo-seed/README.md) sets up five sample databases with users, permissions, pending reviews, and migration history in a separate Compose project. It includes a smoke test and reset instructions.

## Features

- Register PostgreSQL and MySQL connections, browse schemas and data, and give groups view or edit access.
- Compare PostgreSQL schemas, inspect query plans, and draft migrations for review.
- Queue approved PostgreSQL migrations, track jobs, roll back or reapply changes, and recover checkpointed batches. MySQL browsing remains available, but reviewed execution is PostgreSQL-only.

See the [architecture diagram, tech stack, and milestone features](docs/features.md).

## Operations and testing

| Guide                                                 | What it covers                                                                             |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| [Migration jobs and recovery](docs/jobs.md)           | Job status, worker settings, resume and compensation, and the million-row batch benchmark. |
| [Local observability](deploy/observability/README.md) | Grafana dashboard, Prometheus metrics, Loki logs, and Tempo traces for the API and worker. |
| [Tests](docs/development.md#tests)                    | Go and SPA checks, database integration tests, and Playwright browser tests.               |
| [CodeQL](docs/codeql/README.md)                       | Scan configuration, findings, and remediation evidence.                                    |

### k6 stress test

The staged benchmark uses a disposable stack. Its thresholds are API p95 below 500 ms and unexpected HTTP errors below 1%. The 16-user burst breached both thresholds; the four-user recovery stage passed. See the [k6 guide for setup, per-stage results, and evidence](docs/k6/README.md).

## Team

Jason Edward Salim, Nicholas Wise Saragih Sumbayak, and Michael James Liman. See [student IDs and contributions](docs/features.md#team).
