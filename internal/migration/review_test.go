package migration_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/migration"
	"sqlwise/internal/migration/migrationtest"
)

// review is a database "db" whose access comes from two groups: Editors
// (edit, led by editor) and Auditors (view only, led by auditor). outsider
// leads a group that only reaches "other".
type review struct {
	p                          *migrationtest.Platform
	targets                    *migrationtest.Targets
	target                     *pgxpool.Pool
	service                    *migration.Service
	queue                      *migration.LocalQueue
	editor, auditor, outsider  migrationtest.User
	editors, auditors, another string
}

func newReview(t *testing.T) *review {
	t.Helper()
	p := migrationtest.NewPlatform(t)
	p.Database(t, "db", "Shop")
	p.Database(t, "other", "Other")
	r := &review{p: p, targets: migrationtest.NewTargets(), target: migrationtest.TargetDatabase(t)}
	r.targets.Add("db", r.target)
	editor, auditor, outsider := p.User(t, "editor", nil), p.User(t, "auditor", nil), p.User(t, "outsider", nil)
	r.editors = p.Group(t, "Editors", map[string]string{"db": "edit"}, editor.ID, editor.ID)
	r.auditors = p.Group(t, "Auditors", map[string]string{"db": "view"}, auditor.ID, auditor.ID)
	r.another = p.Group(t, "Elsewhere", map[string]string{"other": "edit"}, outsider.ID, outsider.ID)
	r.login(t)
	r.service = migration.NewService(migration.NewStore(p.Pool), p.Access, r.targets, time.Now)
	r.queue = migration.NewLocalQueue(r.service)
	return r
}

// login refreshes every session after a policy change.
func (r *review) login(t *testing.T) {
	t.Helper()
	r.editor, r.auditor, r.outsider = r.p.Login(t, "editor"), r.p.Login(t, "auditor"), r.p.Login(t, "outsider")
}

func (r *review) define(t *testing.T, title, up, down, irreversible string) migration.Draft {
	t.Helper()
	d, err := r.service.Create(r.editor.Ctx, "db", migration.DraftInput{Title: title, SQL: up, SQLDown: down,
		Irreversible: irreversible != "", IrreversibleReason: irreversible})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// request asks for op on the current revision and history position.
func (r *review) request(t *testing.T, user migrationtest.User, d migration.Draft, op migration.Operation) (migration.Request, error) {
	t.Helper()
	detail, err := r.service.Detail(r.editor.Ctx, "db", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return r.service.CreateRequest(user.Ctx, "db", d.ID, migration.RequestInput{Operation: op,
		ExpectedRevision: detail.Draft.Revision, ExpectedDefinition: detail.Draft.DefinitionHash(), ExpectedVersion: detail.CurrentVersion})
}

func (r *review) approve(t *testing.T, req migration.Request) migration.Request {
	t.Helper()
	req, err := r.service.SubmitRequest(r.editor.Ctx, "db", req.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, leader := range []struct {
		user  migrationtest.User
		group string
	}{{r.editor, r.editors}, {r.auditor, r.auditors}} {
		if req, err = r.service.Decide(leader.user.Ctx, "db", req.ID, leader.group, true); err != nil {
			t.Fatal(err)
		}
	}
	if req.Status != migration.RequestApproved {
		t.Fatalf("request after both approvals = %s", req.Status)
	}
	return req
}

// run takes op through request, review and the queue, and returns the
// recorded execution, whatever its outcome.
func (r *review) run(t *testing.T, d migration.Draft, op migration.Operation) migration.Attempt {
	t.Helper()
	req, err := r.request(t, r.editor, d, op)
	if err != nil {
		t.Fatalf("%s %s: %v", op, d.Title, err)
	}
	req = r.approve(t, req)
	job, err := r.queue.Enqueue(r.editor.Ctx, "db", req.ID, "key-"+req.ID)
	if err != nil {
		t.Fatal(err)
	}
	r.queue.Wait()
	if req, err = r.service.Request(r.editor.Ctx, "db", req.ID); err != nil || req.Execution == nil {
		t.Fatalf("request after job %+v = %+v, %v", job, req, err)
	}
	if job.ExecutionID != req.Execution.ID || req.Execution.ID == req.ID || req.Execution.ID == d.ID {
		t.Fatalf("execution %q must be new and distinct from job %+v and request %q", req.Execution.ID, job, req.ID)
	}
	return *req.Execution
}

func (r *review) has(t *testing.T, table string) bool {
	t.Helper()
	var exists bool
	if err := r.target.QueryRow(t.Context(), `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func (r *review) history(t *testing.T) migration.History {
	t.Helper()
	h, err := r.service.History(r.auditor.Ctx, "db")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func executionIDs(attempts []migration.Attempt) []string {
	out := []string{}
	for _, a := range attempts {
		out = append(out, a.ID)
	}
	return out
}

func TestReviewedRollbackForwardAndHistory(t *testing.T) {
	r := newReview(t)
	a := r.define(t, "A", "CREATE TABLE a (id int)", "DROP TABLE a", "")
	b := r.define(t, "B", "CREATE TABLE b (id int)", "DROP TABLE b", "")
	c := r.define(t, "C", "CREATE TABLE c (id int)", "DROP TABLE c", "")

	e1, e2 := r.run(t, a, migration.OpUp), r.run(t, b, migration.OpUp)
	e3 := r.run(t, b, migration.OpRollback)
	if e3.Status != migration.StatusSucceeded || e3.CancelsExecutionID != e2.ID || e3.SQL != "DROP TABLE b" || r.has(t, "b") {
		t.Fatalf("rollback B = %+v, table b present %v", e3, r.has(t, "b"))
	}
	e4 := r.run(t, b, migration.OpForward)
	if e4.Status != migration.StatusSucceeded || e4.SQL != "CREATE TABLE b (id int)" || !r.has(t, "b") {
		t.Fatalf("forward B = %+v", e4)
	}
	e5 := r.run(t, c, migration.OpUp)
	h := r.history(t)
	if got := executionIDs(h.Committed); !reflect.DeepEqual(got, []string{e1.ID, e2.ID, e3.ID, e4.ID, e5.ID}) {
		t.Fatalf("committed history = %v", got)
	}
	if got := executionIDs(migration.HideRollbacks(h.Committed)); !reflect.DeepEqual(got, []string{e1.ID, e4.ID, e5.ID}) {
		t.Fatalf("hide rollback = %v, want E1 E4 E5", got)
	}

	// A → B → C rolls back C before B; forward re-applies B before C, and
	// nothing new is applied while they wait.
	if _, err := r.request(t, r.editor, b, migration.OpRollback); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("rollback B before C = %v", err)
	}
	r.run(t, c, migration.OpRollback)
	r.run(t, b, migration.OpRollback)
	if r.has(t, "b") || r.has(t, "c") || !r.has(t, "a") {
		t.Fatal("rollbacks did not restore the A-only schema")
	}
	d := r.define(t, "D", "CREATE TABLE d (id int)", "DROP TABLE d", "")
	for _, blocked := range []struct {
		d  migration.Draft
		op migration.Operation
	}{{d, migration.OpUp}, {c, migration.OpForward}} {
		if _, err := r.request(t, r.editor, blocked.d, blocked.op); !errors.Is(err, migration.ErrConflict) {
			t.Fatalf("%s %s while B waits = %v", blocked.op, blocked.d.Title, err)
		}
	}
	r.run(t, b, migration.OpForward)
	r.run(t, c, migration.OpForward)
	if !r.has(t, "b") || !r.has(t, "c") {
		t.Fatal("forward did not re-apply B and C")
	}

	// A failed rollback is a visible attempt, not history, and the head stays.
	f := r.define(t, "F", "CREATE TABLE f (id int)", "DROP TABLE missing_table", "")
	ef := r.run(t, f, migration.OpUp)
	before := r.history(t)
	failed := r.run(t, f, migration.OpRollback)
	after := r.history(t)
	if failed.Status != migration.StatusFailed || !r.has(t, "f") || len(after.Committed) != len(before.Committed) ||
		after.Attempts[0].ID != failed.ID || !reflect.DeepEqual(after.Head, before.Head) {
		t.Fatalf("failed rollback = %+v; head %+v -> %+v", failed, before.Head, after.Head)
	}
	if top, _ := after.Head.Top(); top.ExecutionID != ef.ID {
		t.Fatalf("head after failed rollback = %+v", top)
	}

	// Irreversible migrations apply but never roll back; executed definitions are frozen.
	g := r.define(t, "G", "CREATE TABLE g (id int)", "", "drops nothing but is one-way by policy")
	r.run(t, g, migration.OpUp)
	if _, err := r.request(t, r.editor, g, migration.OpRollback); !errors.Is(err, migration.ErrConflict) || !strings.Contains(migration.Message(err), "irreversible") {
		t.Fatalf("rollback irreversible = %v", err)
	}
	if _, err := r.service.Update(r.editor.Ctx, "db", f.ID, migration.UpdateInput{ExpectedRevision: 1,
		DraftInput: migration.DraftInput{Title: "F", SQL: "CREATE TABLE f (id int)", SQLDown: "DROP TABLE f"}}); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("editing an executed definition = %v", err)
	}
}

func TestApprovalNeedsEveryAuthorizedGroupLeader(t *testing.T) {
	r := newReview(t)
	d := r.define(t, "Items", "CREATE TABLE items (id int)", "DROP TABLE items", "")
	enqueue := func(req migration.Request) error {
		_, err := r.queue.Enqueue(r.editor.Ctx, "db", req.ID, "key")
		r.queue.Wait()
		return err
	}

	// A view-only leader reviews but cannot request.
	if _, err := r.request(t, r.auditor, d, migration.OpUp); !errors.Is(err, access.ErrPermissionDenied) {
		t.Fatalf("view-only request = %v", err)
	}
	req, err := r.request(t, r.editor, d, migration.OpUp)
	if err != nil {
		t.Fatal(err)
	}
	if err := enqueue(req); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("enqueue draft request = %v", err)
	}
	if req, err = r.service.SubmitRequest(r.editor.Ctx, "db", req.ID); err != nil || req.Status != migration.RequestPendingReview || len(req.Approvals) != 2 {
		t.Fatalf("submitted = %+v, %v", req, err)
	}
	for _, denied := range []struct {
		user  migrationtest.User
		group string
	}{{r.outsider, r.another}, {r.outsider, r.auditors}, {r.auditor, r.editors}} {
		if _, err := r.service.Decide(denied.user.Ctx, "db", req.ID, denied.group, true); !errors.Is(err, access.ErrPermissionDenied) {
			t.Fatalf("%s deciding for %s = %v", denied.user.Username, denied.group, err)
		}
	}
	if req, err = r.service.Decide(r.editor.Ctx, "db", req.ID, r.editors, true); err != nil || req.Status != migration.RequestPendingReview {
		t.Fatalf("one of two approvals = %+v, %v", req, err)
	}
	if _, err := r.service.Decide(r.editor.Ctx, "db", req.ID, r.editors, true); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("second decision for one group = %v", err)
	}
	if err := enqueue(req); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("enqueue with a missing approval = %v", err)
	}
	if req, err = r.service.Decide(r.auditor.Ctx, "db", req.ID, r.auditors, true); err != nil || req.Status != migration.RequestApproved {
		t.Fatalf("all approvals = %+v, %v", req, err)
	}

	// Any change to who must approve invalidates the round.
	if err := r.p.Access.AddMembership(r.p.Admin, r.outsider.ID, r.auditors); err != nil {
		t.Fatal(err)
	}
	r.login(t)
	if err := enqueue(req); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("enqueue after policy change = %v", err)
	}
	if req, err = r.service.Request(r.editor.Ctx, "db", req.ID); err != nil || req.Stale == "" {
		t.Fatalf("stale request = %+v, %v", req, err)
	}
	if r.targets.Opens() != 0 || r.has(t, "items") {
		t.Fatal("an unapproved request reached the target")
	}
	req = r.approve(t, req)
	if err := enqueue(req); err != nil || !r.has(t, "items") {
		t.Fatalf("approved enqueue = %v", err)
	}

	// A rejection ends the round; nothing runs until a new round passes.
	other := r.define(t, "Other", "CREATE TABLE other (id int)", "DROP TABLE other", "")
	rejected, err := r.request(t, r.editor, other, migration.OpUp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.SubmitRequest(r.editor.Ctx, "db", rejected.ID); err != nil {
		t.Fatal(err)
	}
	if rejected, err = r.service.Decide(r.auditor.Ctx, "db", rejected.ID, r.auditors, false); err != nil || rejected.Status != migration.RequestRejected {
		t.Fatalf("rejected = %+v, %v", rejected, err)
	}
	if err := enqueue(rejected); !errors.Is(err, migration.ErrConflict) || r.has(t, "other") {
		t.Fatalf("enqueue rejected = %v", err)
	}
	if rejected, err = r.service.SubmitRequest(r.editor.Ctx, "db", rejected.ID); err != nil || rejected.Status != migration.RequestPendingReview || rejected.ReviewRound != 2 {
		t.Fatalf("resubmitted = %+v, %v", rejected, err)
	}
}

func TestStaleAndRevokedRequestsNeverWrite(t *testing.T) {
	r := newReview(t)

	// Every authorized group needs an active leader before review starts.
	readers := r.p.Group(t, "Readers", map[string]string{"db": "view"}, "", r.outsider.ID)
	r.login(t)
	x := r.define(t, "X", "CREATE TABLE x (id int)", "DROP TABLE x", "")
	req, err := r.request(t, r.editor, x, migration.OpUp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.SubmitRequest(r.editor.Ctx, "db", req.ID); !errors.Is(err, migration.ErrConflict) || !strings.Contains(migration.Message(err), "Readers") {
		t.Fatalf("submit without leader = %v", err)
	}
	if err := r.p.Access.RemoveGrant(r.p.Admin, readers, "db"); err != nil {
		t.Fatal(err)
	}
	r.login(t)

	// Editing the definition makes earlier requests stale.
	if _, err := r.service.Update(r.editor.Ctx, "db", x.ID, migration.UpdateInput{ExpectedRevision: 1,
		DraftInput: migration.DraftInput{Title: "X", SQL: "CREATE TABLE x2 (id int)", SQLDown: "DROP TABLE x2"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.service.SubmitRequest(r.editor.Ctx, "db", req.ID); !errors.Is(err, migration.ErrConflict) {
		t.Fatalf("submit after edit = %v", err)
	}
	if req, err = r.service.Request(r.editor.Ctx, "db", req.ID); err != nil || req.Stale == "" {
		t.Fatalf("edited request = %+v, %v", req, err)
	}

	// Two requests approved at one head: the second becomes stale once the
	// first runs.
	y := r.define(t, "Y", "CREATE TABLE y (id int)", "DROP TABLE y", "")
	first, err := r.request(t, r.editor, x, migration.OpUp)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.request(t, r.editor, y, migration.OpUp)
	if err != nil {
		t.Fatal(err)
	}
	first, second = r.approve(t, first), r.approve(t, second)
	if _, err := r.queue.Enqueue(r.editor.Ctx, "db", first.ID, "1"); err != nil {
		t.Fatal(err)
	}
	r.queue.Wait()
	if _, err := r.queue.Enqueue(r.editor.Ctx, "db", second.ID, "2"); !errors.Is(err, migration.ErrConflict) || r.has(t, "y") {
		t.Fatalf("enqueue at a stale head = %v", err)
	}

	// A requester who loses access after the job was published is refused by
	// the worker, which has no browser session.
	z := r.define(t, "Z", "CREATE TABLE z (id int)", "DROP TABLE z", "")
	queued, err := r.request(t, r.editor, z, migration.OpUp)
	if err != nil {
		t.Fatal(err)
	}
	queued = r.approve(t, queued)
	if err := pgx.BeginFunc(r.editor.Ctx, r.p.Pool, func(tx pgx.Tx) error {
		_, err := r.service.Consume(r.editor.Ctx, tx, "db", queued.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.p.Access.RemoveMembership(r.p.Admin, r.editor.ID, r.editors); err != nil {
		t.Fatal(err)
	}
	target, err := r.targets.Open(t.Context(), "db")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if _, err := r.service.Run(t.Context(), target, queued.ID); !errors.Is(err, migration.ErrConflict) || r.has(t, "z") {
		t.Fatalf("run for a revoked requester = %v", err)
	}
	r.login(t)
	if got, err := r.service.Request(r.auditor.Ctx, "db", queued.ID); err != nil || got.Execution != nil {
		t.Fatalf("revoked run recorded an execution: %+v, %v", got.Execution, err)
	}
}
