# Development

This guide covers local setup, demo target databases, and project checks. Run commands from the repository root unless noted otherwise.

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

For a disposable, pre-populated walkthrough, see [Seed a disposable demo](../deploy/demo-seed/README.md).

## Run manually

Install the Go version in [`go.mod`](../go.mod), a C compiler, Node.js, and Vite+ (`vp`). Use a POSIX shell.

### Initialize the platform database

Start the databases and create the platform administrator once:

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

### Start the frontend, API, and worker

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

## Demo target databases

The Docker stack includes a seeded PostgreSQL target. Register it under **Admin → Connections**. Start the optional seeded MySQL target with `docker compose --profile bonus up -d mysql`.

| Target     | Host and port in Docker | Host and port for manual Go | Database       |
| ---------- | ----------------------- | --------------------------- | -------------- |
| PostgreSQL | `target:5432`           | `localhost:5434`            | `sqlwise_demo` |
| MySQL      | `mysql:3306`            | `localhost:3307`            | `sqlwise_demo` |

Both demo targets use username `sqlwise_target`, password `sqlwise_target_dev_only`, and TLS disabled. To grant access, go to **Admin → Groups → Members and databases** for your group. Under **Database access**, select the target, choose **View** or **Edit**, and click **Save access**.

## Tests

### Database integration tests

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

### Development checks

```sh
go tool templ generate
go vet ./...
vp check
vp build
pnpm run spa:check
pnpm run spa:test
pnpm run spa:build
```

### Browser tests

Browser checks use Chromium at 1440px, 390px, and 320px:

```sh
vp exec playwright install chromium
# Keep the Go API running on port 8080; Playwright starts the SPA dev server.
pnpm run spa:e2e
# Or test a separately running Go server serving the production SPA build:
SQLWISE_E2E_URL=http://localhost:8080 pnpm run spa:e2e
```

Set `SQLWISE_E2E_USERNAME` and `SQLWISE_E2E_PASSWORD` if the test administrator differs from the local defaults. Two browser harness tests are development-only and are skipped against the production URL; all application flow tests still run.
