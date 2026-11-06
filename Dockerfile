FROM node:24-bookworm-slim AS frontend-build
WORKDIR /app
RUN npm install --global vite-plus@1.3.0 pnpm@10.33.0
COPY package.json pnpm-lock.yaml ./
RUN vp install --frozen-lockfile
COPY web ./web
COPY vite.config.ts tsconfig.json ./
COPY internal/web/views ./internal/web/views
RUN pnpm run spa:build

FROM golang:1.27.1-bookworm AS go-build
WORKDIR /app
ENV CGO_ENABLED=1
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/sqlwise ./cmd/server \
    && go build -o /out/sqlwise-admin ./cmd/admin

FROM debian:bookworm-slim AS app
RUN apt-get update \
    && apt-get install --yes --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 sqlwise
WORKDIR /app
COPY --from=go-build /out/sqlwise /out/sqlwise-admin /usr/local/bin/
COPY --from=frontend-build /app/web/spa/dist/client ./web/spa/dist/client
ENV APP_ENV=production PORT=8080
USER sqlwise
EXPOSE 8080
CMD ["sqlwise"]
