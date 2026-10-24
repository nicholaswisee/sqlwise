# sqlwise

sqlwise is a server-rendered SQL database management project for the IF3110 Web Application Development course, focused on PostgreSQL database exploration, migration workflows, and group-based access control.

## Technology

- Go and Gin for HTTP routing and backend services
- templ and HTMX for server-rendered pages and interactions
- Tailwind CSS, TypeScript, and Vite+ for styling and browser assets
- PostgreSQL for platform data and database management

## Project structure

```text
.
├── .github/workflows/ci.yml       # Generate, check, test, and build
├── cmd/server/                   # Go HTTP entrypoint and route tests
├── cmd/admin/                    # First-administrator setup
├── internal/auth/                # Users, password hashing, and sessions
├── internal/access/              # Groups, grants, and access control
├── internal/migration/           # Migration drafts, execution, and history
├── internal/schemadiff/          # Schema diff and migration generation
├── internal/database/            # Target connections, adapters, and metadata
├── internal/handlers/            # Server-rendered HTTP handlers
├── internal/web/views/           # templ pages, layouts, and asset loader
├── web/src/                      # Vite entry and Tailwind stylesheet
├── deploy/target/                # Seed for the demo target database
├── compose.yaml                  # Local platform and demo target PostgreSQL
├── .env.example                  # Development environment example
├── go.mod, go.sum                # Go module and dependencies
├── package.json, pnpm-lock.yaml  # Frontend tooling and dependencies
└── vite.config.ts, tsconfig.json # Asset build and TypeScript configuration
```

Generated `*_templ.go` files are committed alongside their `.templ` sources. `web/dist`, `bin`, `node_modules`, and `.env` are ignored by Git.

## Local development

Install the Go version specified in `go.mod`, Node.js LTS, Docker Compose, and Vite+ (`vp`). Set up the platform database and first administrator:

```sh
docker compose up -d postgres
cp .env.example .env
set -a
. ./.env
set +a
go run ./cmd/admin -username admin
```

The command reads the password without echoing it and initializes the platform schema.
Use at least 12 characters and at most 128 UTF-8 bytes. Existing administrators are
never overwritten. If a local PostgreSQL volume was initialized with different
credentials, configure `DATABASE_URL` for that database; changing Compose's example
variables does not rename an existing role or database.

Target database passwords are stored encrypted with AES-256-GCM using
`SQLWISE_CONNECTION_KEY`, 32 random bytes encoded as base64. The key in
`.env.example` is for local development only; generate a separate key elsewhere
with `openssl rand -base64 32` and keep it out of the repository. Changing the key
makes previously stored target passwords unreadable.

### Target databases

Target databases are separate from the platform database and use their own
credentials. Compose includes a seeded PostgreSQL demo target:

```sh
docker compose up -d target
```

As a platform administrator, open **Admin → Connections** (`/admin/databases`) and
register it as host `localhost`, port `5434`, database `sqlwise_demo`, user
`sqlwise_target`, password `sqlwise_target_dev_only`, TLS `disable`, then use
**Test saved connection**. Grant a group view or edit access to the connection
before its users can open it. Its `public`
and `sales` schemas hold related tables with keys, indexes, NULL, Unicode, binary
and long values. The seed runs only when the `target_data` volume is created.

Users open `/databases`, or the links on their account page, to browse schemas,
columns, constraints and indexes, and to page through table rows (50 per page by
default, at most 200). Cells are cut at 4 KiB and pages at 1 MiB; tables without a
primary key are marked as unordered.

Migrations record a receipt in `sqlwise_internal.migration_receipts` on the target.
Create that table once as a different role than the migration user, so migration
SQL cannot alter or remove receipts. As the owner role, in the target database:

```sql
CREATE SCHEMA IF NOT EXISTS sqlwise_internal;
CREATE TABLE IF NOT EXISTS sqlwise_internal.migration_receipts (
    attempt_id text PRIMARY KEY,
    database_id text NOT NULL,
    version bigint NOT NULL CHECK (version >= 1),
    sql_sha256 text NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('applied', 'fenced')),
    recorded_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON SCHEMA sqlwise_internal FROM PUBLIC;
REVOKE ALL ON sqlwise_internal.migration_receipts FROM PUBLIC;
GRANT USAGE ON SCHEMA sqlwise_internal TO "migration_user";
GRANT SELECT, INSERT ON sqlwise_internal.migration_receipts TO "migration_user";
```

The migration user otherwise needs the privileges its migrations use, such as
ownership of the tables they alter. Without the table, the first execution creates
it with the migration user, which then requires `CREATE` on the database and leaves
receipts owned by that user. The demo target is already set up this way.

The server keeps one pool per registered target (at most 4 connections, 5 second
connect timeout) and closes it when the connection is disabled or its settings
change, after running transactions finish. Callers receive handles and close them;
they never own the pool. Ping, metadata and table reads time out after 5 seconds.

From the repository root, run the asset server:

```sh
vp install
vp dev
```

In another terminal, export the same environment, generate templates and start Go:

```sh
set -a
. ./.env
set +a
go tool templ generate
go run ./cmd/server
```

Open <http://localhost:8080/login>. Successful login opens `/account`; Sign out
revokes the session. Vite+ serves assets on port 5173 and Go serves pages on port 8080. The server requires a reachable platform database and initialized auth schema.
It reads environment variables directly and does not load `.env` automatically.

## Checks and production build

```sh
go tool templ generate
go test -race -shuffle=on -count=1 ./...
go vet ./...
vp check
vp build
go build -o bin/sqlwise ./cmd/server
go build -o bin/sqlwise-admin ./cmd/admin
```

For PostgreSQL integration tests, set `TEST_DATABASE_URL` to a disposable database;
each test creates and cleans up its own schema. Target database tests also create
and drop their own databases and login roles, so the role needs `CREATEDB` and
`CREATEROLE` (the Compose `sqlwise` user has both):

```sh
TEST_DATABASE_URL=postgres://sqlwise:sqlwise_dev_only@localhost:5433/sqlwise_platform?sslmode=disable   go test -race -shuffle=on -count=1 ./...
```

Without that variable, DB tests are explicitly skipped, and a skipped run is not a
passing one. CI provisions PostgreSQL and sets it, so these tests run.

Set `APP_ENV=production` behind HTTPS, with `DATABASE_URL` and
`SQLWISE_CONNECTION_KEY` configured and `web/dist` present in the working directory. Production session cookies use Secure. Static
assets are deployed alongside the Go binary. CI runs generation, checks, tests,
and both builds.

## Bonus features

| Bonus                                | Status                  | Notes                                                                                                                                                                                                                                                                                                                     |
| ------------------------------------ | ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| MySQL support                        | Not implemented         | Groundwork only: the `mysql` database type, identifier quoting and a connection form option marked "not supported yet". MySQL connections are rejected as unsupported; there is no MySQL driver, viewer or migration path.                                                                                                |
| Schema diff and migration generation | Complete for PostgreSQL | Compares the live schema with a desired JSON snapshot, lists ordered changes with destructive and unsupported warnings, generates `CREATE TABLE` and `ADD COLUMN` SQL, and saves it as an editable draft after a drift check. Covered by `internal/schemadiff` and `cmd/server` adapter tests. MySQL diff is unsupported. |
| Query execution plan analysis        | Not implemented         | There is no Explain or Analyze endpoint.                                                                                                                                                                                                                                                                                  |

## Members

| Student ID | Name                           |
| ---------- | ------------------------------ |
| 13524034   | Jason Edward Salim             |
| 13524037   | Nicholas Wise Saragih Sumbayak |
| 13524106   | Michael James Liman            |

## Contributions

| Member                         | Areas                                                                                                                                                                                                                                                                                                                |
| ------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Jason Edward Salim             | Target connection management (encrypted credentials, connection test, enable and disable), the PostgreSQL adapter (pooled resolver, transactions with uncertain-commit detection, schema snapshots, bounded table reads), the database viewer, migration receipt setup, the demo target and test database isolation. |
| Nicholas Wise Saragih Sumbayak | Authentication with Argon2id, sessions (timeouts, rotation, grace, revocation), CSRF protection, group-based authorization and middleware, the administration console for users, groups, memberships and grants, the base layout with HTMX, Tailwind and Vite+, and CI.                                              |
| Michael James Liman            | Migration drafts and editor, PostgreSQL statement parsing, atomic execution with locks, receipts and reconciliation, linear version history, and schema diff with migration generation.                                                                                                                              |
