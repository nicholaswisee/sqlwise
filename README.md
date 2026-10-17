# WiseQL

WiseQL is a PostgreSQL management platform foundation for IF3110 Web Application Development Milestone 1. It is deliberately **not an SPA**: Go and `templ` render full documents and HTMX fragments, while Vite+ builds browser assets and Alpine handles local presentation state.

## Prerequisites

- Go 1.24 or newer
- Node.js LTS and Vite+ (`vp`)
- Docker Compose for the local PostgreSQL environment

## Local development

```sh
cp .env.example .env
vp install
go tool templ generate
docker compose up -d
vp dev
```

In another terminal, load the example variables and start Go with `set -a; . ./.env; set +a; go run ./cmd/server`. The Go server does not load `.env` files itself. Open the application at <http://localhost:8080/>. Vite+ serves assets and HMR on port 5173. PostgreSQL is bound to `127.0.0.1:5433`; Compose creates `wiseql_platform` for future platform metadata. The demonstration target database, `wiseql_demo`, can be created with:

```sh
docker compose exec -T postgres psql -U wiseql -d wiseql_platform -c 'CREATE DATABASE wiseql_demo;'
```

The committed `.env.example` contains development-only values. Copy it to `.env` for local overrides; `.env` is ignored by Git. The bootstrap server does not yet open a PostgreSQL connection.

## Build and verify

```sh
go tool templ generate
vp check
go test ./...
go vet ./...
vp build
go build -o bin/wiseql ./cmd/server
```

For an end-to-end local build, run `vp run build:all`. Set `APP_ENV=production` to load `web/dist/.vite/manifest.json` and serve the compiled assets. Deploy `web/dist` alongside the Go binary; assets are not embedded in the binary. CI regenerates templ sources, checks Go formatting, vets and tests Go code, then builds both asset and server outputs.

## Scope

This is infrastructure and interaction scaffolding only. Authentication and sessions, backend-enforced group and database permissions, connection management, schema inspection, transactional versioned migrations, history, and PostgreSQL integration tests remain future work. Alpine state and HTMX requests must never be used as authorization decisions.

Planned modules are `internal/auth`, `internal/access`, `internal/database`, and `internal/migration`.
