# sqlwise

sqlwise is a database management web app built for the IF3110 Web Application Development course. Teams can explore databases, manage access, plan schema changes, and track migrations from one interface.

## Tech stack

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/stack-diagram-dark.png">
  <img src="docs/stack-diagram.png" alt="SQLwise stack: browser UI with HTMX, TypeScript, and Tailwind CSS; Go, Gin, and templ application; PostgreSQL and optional MySQL databases; Vite+ and Docker tooling.">
</picture>

[Open the standalone diagram](docs/stack-diagram.html).

## Features

- **Database explorer:** Browse PostgreSQL and MySQL schemas, tables, columns, and data.
- **Migrations:** Write SQL drafts, run migrations, and review version history and execution results.
- **Access control:** Manage users, groups, and view or edit permissions for each database.
- **Connection management:** Register, test, enable, and disable database connections.

## Bonus features

## Milestone 1

| Feature       | Status                  | What it does                                                                                                              |
| ------------- | ----------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| MySQL support | Complete                | Adds MySQL connections, browsing, and migrations. Partial or uncertain migration outcomes can be reviewed and reconciled. |
| Schema diff   | Complete for PostgreSQL | Compares a live schema with a desired JSON snapshot and generates a migration draft with change warnings.                 |
| Query plans   | Complete for PostgreSQL | Shows Explain plans and supports Analyze for SELECT queries on an authorized sandbox connection.                          |

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

Open the URL printed by the SPA development server and sign in with the administrator password you chose. Its `/api/v1` requests proxy to Go on port 8080, keeping cookie and CSRF handling same-origin. The Go process can run API-only during development without a frontend build. To serve the built SPA directly from Go on <http://localhost:8080/login>, run `pnpm run spa:build` first.

## SPA and retained legacy frontend

The active frontend lives in `web/spa` and uses TanStack Start in static SPA mode, TanStack Router, and TanStack Query. It calls Go JSON endpoints under `/api/v1`; Go still owns authentication, session rotation, permissions, connections, queries, and migration execution. The frontend foundation follows Better T Stack's frontend-only approach, without adding its JavaScript backend, ORM, or authentication service.

The previous templ/HTMX frontend remains in `internal/web/views`, `web/src`, and the legacy Vite configuration. Its templates, generated Go, HTML handlers, and regression tests are retained; production no longer mounts those HTML routes. `vp build` and `go tool templ generate` still check the legacy frontend. The optional Compose `legacy` profile retains the old frontend asset-development service, not a second production UI.

This refactor changes only the frontend and its API/serving boundary. It does **not** introduce M2 approval, rollback/forward, queue, worker, or database-schema changes; migration execution retains its existing synchronous behavior. Keeping the legacy source is not a guarantee that an older application release will be safe after future M2 data/schema changes.

## Demo target databases

The Docker stack includes a seeded PostgreSQL target. Register it under **Admin → Connections**. Start the optional seeded MySQL target with `docker compose --profile bonus up -d mysql`.

| Target     | Host and port in Docker | Host and port for manual Go | Database       |
| ---------- | ----------------------- | --------------------------- | -------------- |
| PostgreSQL | `target:5432`           | `localhost:5434`            | `sqlwise_demo` |
| MySQL      | `mysql:3306`            | `localhost:3307`            | `sqlwise_demo` |

Both demo targets use username `sqlwise_target`, password `sqlwise_target_dev_only`, and TLS disabled. Grant a group access after registering a target.

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

| Student ID | Member                         | Main contributions                                                                          |
| ---------- | ------------------------------ | ------------------------------------------------------------------------------------------- |
| 13524034   | Jason Edward Salim             | Database connections, PostgreSQL adapter and explorer, demo target, test database isolation |
| 13524037   | Nicholas Wise Saragih Sumbayak | Authentication, sessions, access control, administration, frontend foundation, CI           |
| 13524106   | Michael James Liman            | Migration editor and execution, history, schema diff and generation                         |
