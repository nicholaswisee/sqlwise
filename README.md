# sqlwise

sqlwise is a database management web app built for the IF3110 Web Application Development course. Teams can explore databases, manage access, plan schema changes, and track migrations from one interface.

<div align="center">
  <h3>Tech Stacks</h3>

[![Go](https://img.shields.io/badge/Go-00ADD8?style=for-the-badge&logo=go&logoColor=white)](https://go.dev/)
[![Gin](https://img.shields.io/badge/Gin-008ECF?style=for-the-badge&logo=gin&logoColor=white)](https://github.com/gin-gonic/gin)
[![templ](https://img.shields.io/badge/templ-0A7C86?style=for-the-badge&logo=go&logoColor=white)](https://templ.guide/)
[![HTMX](https://img.shields.io/badge/HTMX-3366CC?style=for-the-badge&logo=htmx&logoColor=white)](https://htmx.org/)
[![PostgreSQL](https://img.shields.io/badge/PostgreSQL-4169E1?style=for-the-badge&logo=postgresql&logoColor=white)](https://www.postgresql.org/)
[![MySQL](https://img.shields.io/badge/MySQL-4479A1?style=for-the-badge&logo=mysql&logoColor=white)](https://www.mysql.com/)
[![Tailwind CSS](https://img.shields.io/badge/Tailwind_CSS-06B6D4?style=for-the-badge&logo=tailwindcss&logoColor=white)](https://tailwindcss.com/)
[![TypeScript](https://img.shields.io/badge/TypeScript-3178C6?style=for-the-badge&logo=typescript&logoColor=white)](https://www.typescriptlang.org/)
[![Vite+](https://img.shields.io/badge/Vite%2B-646CFF?style=for-the-badge&logo=vite&logoColor=white)](https://www.viteplus.dev/)
[![Docker](https://img.shields.io/badge/Docker-2496ED?style=for-the-badge&logo=docker&logoColor=white)](https://www.docker.com/)

</div>

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
