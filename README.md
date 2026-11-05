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

Use `docker compose logs -f app frontend` for logs, `docker compose up -d --build` after source changes, and `docker compose down` to stop the stack without removing database volumes.

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

Start Vite+ in one terminal:

```sh
vp install
vp dev
```

In another terminal, load the environment and start the Go server:

```sh
set -a
. ./.env
set +a
go tool templ generate
go run ./cmd/server
```

Then open <http://localhost:8080/login> and sign in with the administrator password you chose.

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
```

## Team

| Student ID | Member                         | Main contributions                                                                          |
| ---------- | ------------------------------ | ------------------------------------------------------------------------------------------- |
| 13524034   | Jason Edward Salim             | Database connections, PostgreSQL adapter and explorer, demo target, test database isolation |
| 13524037   | Nicholas Wise Saragih Sumbayak | Authentication, sessions, access control, administration, frontend foundation, CI           |
| 13524106   | Michael James Liman            | Migration editor and execution, history, schema diff and generation                         |
