# sqlwise: sql database viewer client

sqlwise is the starting point for an IF3110 PostgreSQL management platform. Go and `templ` render application pages on the server. Vite+ builds the Tailwind CSS and browser assets.

## Project structure

```text
.
├── .github/workflows/ci.yml       # Generate, check, test, and build
├── cmd/server/                    # Go HTTP entrypoint and route tests
├── internal/web/views/            # templ layout, entry page, and asset loader
├── web/src/                       # Vite entry and Tailwind stylesheet
├── compose.yaml                   # Optional local PostgreSQL service
├── .env.example                   # Development environment example
├── go.mod, go.sum                  # Go module and dependencies
├── package.json, pnpm-lock.yaml   # Frontend tooling and dependencies
├── vite.config.ts, tsconfig.json  # Asset build and TypeScript configuration
└── README.md
```

Generated `*_templ.go` files are committed alongside their `.templ` sources. `web/dist`, `bin`, `node_modules`, and `.env` are ignored by Git.

## Local development

Install the Go version specified in `go.mod`, Node.js LTS, and Vite+ (`vp`). From the repository root, run the asset server:

```sh
vp install
vp dev
```

In another terminal, generate the templates and start Go:

```sh
go tool templ generate
go run ./cmd/server
```

Open <http://localhost:8080/>. Vite+ runs on port 5173 for assets and HMR; Go serves the application page on port 8080. The entry page does not require a database connection.

PostgreSQL is available for future development at `127.0.0.1:5433` with the development-only values in `.env.example`:

```sh
docker compose up -d postgres
```

The server reads environment variables directly; it does not load `.env` files. Copy `.env.example` to `.env` and source it in your shell when you need custom local values.

## Checks and production build

```sh
go tool templ generate
go test ./...
go vet ./...
vp check
vp build
go build -o bin/wiseql ./cmd/server
```

Set `APP_ENV=production` when running the binary with `web/dist` present in the working directory. The static assets are deployed alongside the Go binary. CI runs the generation, checks, tests, and builds.

## Members

| Student ID | Name                           |
| ---------- | ------------------------------ |
| 13524034   | Jason Edward Salim             |
| 13524037   | Nicholas Wise Saragih Sumbayak |
| 13524106   | Michael James Liman            |
