// Package k6fixture prepares unique, already approved migration requests for
// load tests through the public API: real logins, review and per-group
// approval. It never writes platform rows directly or skips authorization.
package k6fixture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Account struct {
	Username string
	Password string
}

type Config struct {
	BaseURL string
	// Editor creates and submits requests; Leaders approve for every group they lead.
	Editor      Account
	Leaders     []Account
	Databases   []string
	PerDatabase int
	// Run names the generated tables k6_<run>_<database index>_<n>.
	Run    string
	Client *http.Client
}

// Fixture is one approved request ready for POST ExecutePath.
type Fixture struct {
	DatabaseID      string `json:"database_id"`
	MigrationID     string `json:"migration_id"`
	RequestID       string `json:"request_id"`
	ExpectedVersion int64  `json:"expected_version"`
	ExecutePath     string `json:"execute_path"`
	IdempotencyKey  string `json:"idempotency_key"`
	Table           string `json:"table"`
}

var runName = regexp.MustCompile(`^[a-z0-9]{1,20}$`)

// Prepare creates PerDatabase approved requests on each database, all at its
// current head. Only the first one queued per database can succeed; the rest
// are the expected same-database stale-head conflicts.
func Prepare(ctx context.Context, cfg Config) ([]Fixture, error) {
	if !runName.MatchString(cfg.Run) {
		return nil, errors.New("run must be 1-20 lowercase letters or digits")
	}
	if cfg.PerDatabase < 1 || len(cfg.Databases) == 0 || len(cfg.Leaders) == 0 {
		return nil, errors.New("need at least one database, one leader and one request per database")
	}
	editor, err := login(ctx, cfg, cfg.Editor)
	if err != nil {
		return nil, fmt.Errorf("editor login: %w", err)
	}
	leaders := make([]*client, 0, len(cfg.Leaders))
	for _, a := range cfg.Leaders {
		c, err := login(ctx, cfg, a)
		if err != nil {
			return nil, fmt.Errorf("leader %s login: %w", a.Username, err)
		}
		leaders = append(leaders, c)
	}
	var out []Fixture
	for d, db := range cfg.Databases {
		base := "/databases/" + url.PathEscape(db)
		var history struct {
			CurrentVersion int64 `json:"current_version"`
		}
		if err := editor.do(ctx, http.MethodGet, base+"/migrations", nil, &history); err != nil {
			return nil, err
		}
		for n := 1; n <= cfg.PerDatabase; n++ {
			table := fmt.Sprintf("k6_%s_%d_%d", cfg.Run, d, n)
			var draft struct {
				ID             string `json:"id"`
				Revision       int64  `json:"revision"`
				DefinitionHash string `json:"definition_hash"`
			}
			if err := editor.do(ctx, http.MethodPost, base+"/migrations", map[string]any{"title": "k6 " + table,
				"sql": "CREATE TABLE " + table + " (id int);", "sql_down": "DROP TABLE " + table + ";"}, &draft); err != nil {
				return nil, err
			}
			var req request
			if err := editor.do(ctx, http.MethodPost, base+"/migrations/"+draft.ID+"/requests", map[string]any{"operation": "up",
				"expected_revision": draft.Revision, "expected_definition_hash": draft.DefinitionHash, "expected_version": history.CurrentVersion}, &req); err != nil {
				return nil, err
			}
			if err := editor.do(ctx, http.MethodPost, base+"/requests/"+req.ID+"/submit", map[string]any{}, &req); err != nil {
				return nil, err
			}
			for _, leader := range leaders {
				if err := leader.approve(ctx, base, req.ID); err != nil {
					return nil, err
				}
			}
			if err := editor.do(ctx, http.MethodGet, base+"/requests/"+req.ID, nil, &req); err != nil {
				return nil, err
			}
			if req.Status != "approved" {
				return nil, fmt.Errorf("request %s is %s after review; every authorized group needs an active leader among the given leaders", req.ID, req.Status)
			}
			out = append(out, Fixture{DatabaseID: db, MigrationID: draft.ID, RequestID: req.ID, ExpectedVersion: history.CurrentVersion,
				ExecutePath: "/api/v1" + base + "/migrations/" + draft.ID + "/execute", IdempotencyKey: cfg.Run + "-" + req.ID, Table: table})
		}
	}
	return out, nil
}

// Reset drops the tables a run created on a disposable target. Platform
// history is append-only by design and is never deleted.
func Reset(ctx context.Context, targetURL, run string) (int, error) {
	if run != "" && !runName.MatchString(run) {
		return 0, errors.New("run must be 1-20 lowercase letters or digits")
	}
	conn, err := pgx.Connect(ctx, targetURL)
	if err != nil {
		return 0, err
	}
	defer conn.Close(ctx)
	pattern := `k6\_` + run + `%`
	if run != "" {
		pattern = `k6\_` + run + `\_%`
	}
	rows, err := conn.Query(ctx, `SELECT quote_ident(schemaname) || '.' || quote_ident(tablename) FROM pg_tables
		WHERE schemaname = 'public' AND tablename LIKE $1`, pattern)
	if err != nil {
		return 0, err
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, table := range tables {
		if _, err := conn.Exec(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
			return 0, err
		}
	}
	return len(tables), nil
}

type request struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Approvals []struct {
		GroupID   string `json:"group_id"`
		CanDecide bool   `json:"can_decide"`
	} `json:"approvals"`
}

// client is one signed-in API session with its CSRF token.
type client struct {
	http *http.Client
	base string
	csrf string
}

func login(ctx context.Context, cfg Config, a Account) (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	hc := &http.Client{Timeout: 30 * time.Second, Jar: jar}
	if cfg.Client != nil {
		hc.Transport, hc.Timeout = cfg.Client.Transport, cfg.Client.Timeout
	}
	c := &client{http: hc, base: strings.TrimRight(cfg.BaseURL, "/")}
	var challenge struct {
		CSRF string `json:"csrf_token"`
	}
	if err := c.do(ctx, http.MethodGet, "/auth/login-challenge", nil, &challenge); err != nil {
		return nil, err
	}
	c.csrf = challenge.CSRF
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := c.do(ctx, http.MethodPost, "/auth/login", map[string]string{"username": a.Username, "password": a.Password}, &session); err != nil {
		return nil, err
	}
	c.csrf = session.CSRF
	return c, nil
}

func (c *client) approve(ctx context.Context, base, requestID string) error {
	var req request
	if err := c.do(ctx, http.MethodGet, base+"/requests/"+requestID, nil, &req); err != nil {
		return err
	}
	for _, a := range req.Approvals {
		if a.CanDecide {
			if err := c.do(ctx, http.MethodPost, base+"/requests/"+requestID+"/approve", map[string]string{"group_id": a.GroupID}, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// do calls /api/v1 and decodes the data envelope into out.
func (c *client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet && c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s %s: %d with an unreadable body", method, path, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s: %s", method, path, resp.StatusCode, envelope.Error.Code, envelope.Error.Message)
	}
	if out != nil && len(envelope.Data) > 0 {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}
