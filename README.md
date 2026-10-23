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
├── internal/handlers/            # Server-rendered HTTP handlers
├── internal/web/views/           # templ pages, layouts, and asset loader
├── web/src/                      # Vite entry and Tailwind stylesheet
├── compose.yaml                  # Optional local PostgreSQL service
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
each test creates and cleans up its own schema. Without that variable, DB tests
are explicitly skipped. CI provisions PostgreSQL and sets it, so these tests run.

Set `APP_ENV=production` behind HTTPS, with `DATABASE_URL` configured and `web/dist`
present in the working directory. Production session cookies use Secure. Static
assets are deployed alongside the Go binary. CI runs generation, checks, tests,
and both builds.

## Members

| Student ID | Name                           |
| ---------- | ------------------------------ |
| 13524034   | Jason Edward Salim             |
| 13524037   | Nicholas Wise Saragih Sumbayak |
| 13524106   | Michael James Liman            |
