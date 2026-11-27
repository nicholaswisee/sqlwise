package telemetry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	start  time.Time
	query  string
	hash   string
	params []string
	span   trace.Span
}

// SQLTrace times one statement and logs its start and result separately.
// PGXTracer uses it through pgx's hook; drivers without one (MySQL through
// database/sql) call StartSQL and End around each statement.
type SQLTrace struct {
	ctx   context.Context
	state queryState
}

// StartSQL opens a db.query span and logs sql.start. Argument values are
// never logged; only their count and Go types are.
func StartSQL(ctx context.Context, system, sql string, args []any) (context.Context, SQLTrace) {
	query := RedactSQL(sql)
	hash := sha256.Sum256([]byte(sql))
	ctx, span := otel.Tracer("sqlwise/sql").Start(ctx, "db.query", trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(attribute.String("db.system.name", system), attribute.String("db.query.text", query))
	if namespace := logField(ctx, "database"); namespace != "" {
		span.SetAttributes(attribute.String("db.namespace", namespace))
	}
	params := make([]string, len(args))
	for i, arg := range args {
		params[i] = fmt.Sprintf("[REDACTED %T]", arg)
	}
	state := queryState{start: time.Now(), query: query, hash: hex.EncodeToString(hash[:]), params: params, span: span}
	logSQL(ctx, slog.LevelInfo, "sql.start", state, "started", 0, "", "")
	return ctx, SQLTrace{ctx: ctx, state: state}
}

// End logs sql.result and closes the span. sqlstate and message come from
// the server error, if any; the message is literal-masked before logging.
// rows is -1 when the driver cannot report a count.
func (t SQLTrace) End(rows int64, err error, sqlstate, message string) {
	status, level := "success", slog.LevelInfo
	if err != nil {
		status, level = "failure", slog.LevelWarn
		if message == "" {
			message = "The database connection or driver failed."
		}
		message = RedactMessage(message)
		t.state.span.RecordError(errors.New(message))
		t.state.span.SetStatus(codes.Error, "query failed")
		if sqlstate != "" {
			t.state.span.SetAttributes(attribute.String("db.response.status_code", sqlstate))
		}
	}
	if rows >= 0 {
		t.state.span.SetAttributes(attribute.Int64("db.response.returned_rows", rows))
	}
	logSQL(t.ctx, level, "sql.result", t.state, status, rows, sqlstate, message)
	t.state.span.End()
}

type PGXTracer struct{}

func (PGXTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx, trace := StartSQL(ctx, "postgresql", data.SQL, data.Args)
	return context.WithValue(ctx, queryKey{}, trace)
}

func (PGXTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	trace, ok := ctx.Value(queryKey{}).(SQLTrace)
	if !ok {
		return
	}
	sqlstate, message := "", ""
	var pgErr *pgconn.PgError
	if errors.As(data.Err, &pgErr) {
		// Message only: Detail and Where can echo row values.
		sqlstate, message = pgErr.Code, pgErr.Message
	}
	trace.End(data.CommandTag.RowsAffected(), data.Err, sqlstate, message)
}

func logSQL(ctx context.Context, level slog.Level, event string, state queryState, status string, rows int64, sqlstate, message string) {
	traceID, spanID := "", ""
	if sc := state.span.SpanContext(); sc.IsValid() {
		traceID, spanID = sc.TraceID().String(), sc.SpanID().String()
	}
	args := []any{"event", event, "actor", Actor(ctx),
		"correlation_id", Correlation(ctx), "trace_id", traceID, "span_id", spanID,
		"query", state.query, "sql_hash", state.hash, "params", state.params, "status", status,
		"duration_ms", time.Since(state.start).Milliseconds(), "rows", rows, "sqlstate", sqlstate}
	if message != "" {
		args = append(args, "error", message)
	}
	slog.Log(ctx, level, "sql statement", append(args, logFields(ctx)...)...)
}

// RedactMessage masks quoted text in a database or domain error message,
// where servers echo rejected values: invalid input syntax for type
// integer: "x".
// Unlike RedactSQL it keeps bare numbers such as statement positions and
// SQLSTATE codes readable. An unterminated quote masks the rest.
func RedactMessage(message string) string {
	var out strings.Builder
	for i := 0; i < len(message); i++ {
		quote := message[i]
		if quote != '\'' && quote != '"' && quote != '`' {
			out.WriteByte(quote)
			continue
		}
		end := strings.IndexByte(message[i+1:], quote)
		out.WriteString("[REDACTED]")
		if end < 0 {
			break
		}
		i += end + 1
	}
	return out.String()
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
