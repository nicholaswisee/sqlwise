package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type queryKey struct{}

type queryState struct {
	start time.Time
	query string
	hash  string
	span  trace.Span
}

type PGXTracer struct{}

func (PGXTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	query := RedactSQL(data.SQL)
	hash := sha256.Sum256([]byte(data.SQL))
	ctx, span := otel.Tracer("sqlwise/pgx").Start(ctx, "db.query", trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(attribute.String("db.system.name", "postgresql"), attribute.String("db.query.text", query))
	state := queryState{start: time.Now(), query: query, hash: hex.EncodeToString(hash[:]), span: span}
	logSQL(ctx, "sql.start", state, "started", 0, "")
	return context.WithValue(ctx, queryKey{}, state)
}

func (PGXTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	state, ok := ctx.Value(queryKey{}).(queryState)
	if !ok {
		return
	}
	status, sqlstate := "success", ""
	if data.Err != nil {
		status = "failure"
		var pgErr *pgconn.PgError
		if errors.As(data.Err, &pgErr) {
			sqlstate = pgErr.Code
		}
		state.span.SetStatus(codes.Error, "query failed")
	}
	logSQL(ctx, "sql.result", state, status, data.CommandTag.RowsAffected(), sqlstate)
	state.span.End()
}

func logSQL(ctx context.Context, event string, state queryState, status string, rows int64, sqlstate string) {
	traceID, spanID := "", ""
	if sc := state.span.SpanContext(); sc.IsValid() {
		traceID, spanID = sc.TraceID().String(), sc.SpanID().String()
	}
	slog.InfoContext(ctx, "sql statement", "event", event, "actor", Actor(ctx),
		"correlation_id", Correlation(ctx), "trace_id", traceID, "span_id", spanID,
		"query", state.query, "sql_hash", state.hash, "status", status,
		"duration_ms", time.Since(state.start).Milliseconds(), "rows", rows, "sqlstate", sqlstate)
}

func RedactSQL(sql string) string {
	if strings.Contains(sql, "://") {
		return "[REDACTED]"
	}
	var out strings.Builder
	for i := 0; i < len(sql); {
		switch {
		case strings.HasPrefix(sql[i:], "--") || sql[i] == '#':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			out.WriteByte(' ')
		case strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return "[REDACTED]"
			}
			i += end + 4
			out.WriteByte(' ')
		case sql[i] == '\'' || sql[i] == '"' || sql[i] == '`':
			quote := sql[i]
			i++
			closed := false
			for i < len(sql) {
				if sql[i] == '\\' && i+1 < len(sql) {
					i += 2
				} else if sql[i] == quote {
					i++
					if i < len(sql) && sql[i] == quote {
						i++
					} else {
						closed = true
						break
					}
				} else {
					i++
				}
			}
			if !closed {
				return "[REDACTED]"
			}
			out.WriteString("[REDACTED]")
		case sql[i] == '$':
			end := strings.IndexByte(sql[i+1:], '$')
			if end >= 0 && validDollarTag(sql[i+1:i+1+end]) {
				delimiter := sql[i : i+end+2]
				close := strings.Index(sql[i+len(delimiter):], delimiter)
				if close < 0 {
					return "[REDACTED]"
				}
				i += len(delimiter) + close + len(delimiter)
				out.WriteString("[REDACTED]")
			} else {
				out.WriteByte(sql[i])
				i++
			}
		case sql[i] >= '0' && sql[i] <= '9' && (i == 0 || !identifierByte(sql[i-1]) && sql[i-1] != '$'):
			for i < len(sql) && (identifierByte(sql[i]) || sql[i] == '.') {
				i++
			}
			out.WriteString("[REDACTED]")
		default:
			out.WriteByte(sql[i])
			i++
		}
	}
	return out.String()
}

func identifierByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func validDollarTag(tag string) bool {
	for i := 0; i < len(tag); i++ {
		if !identifierByte(tag[i]) {
			return false
		}
	}
	return true
}

var _ pgx.QueryTracer = PGXTracer{}
