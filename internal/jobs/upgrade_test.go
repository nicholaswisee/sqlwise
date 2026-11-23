package jobs_test

import (
	"testing"
	"time"

	"sqlwise/internal/jobs"
	"sqlwise/internal/jobs/jobstest"
	"sqlwise/internal/migration/migrationtest"
)

func TestJobsSchemaBackfillsOnlyProvenRecoveryBlockers(t *testing.T) {
	p := migrationtest.NewPlatform(t)
	p.Database(t, "a", "A")
	p.Database(t, "b", "B")
	actor := p.User(t, "jobs-upgrade", nil).ID
	store := jobstest.Store(t, p)

	type check struct{ name, db, job, want string }
	var checks []check
	for _, tc := range []struct {
		name, operation, attemptStatus, recoveryStatus string
		link, resolves                                 bool
	}{
		{"compensated", "compensate", "reverted", "succeeded", true, true},
		{"resumed", "resume", "succeeded", "succeeded", true, true},
		{"unfinished", "compensate", "partially_reverted", "succeeded", true, false},
		{"unknown", "resume", "unknown", "succeeded", true, false},
		{"no matching job success", "compensate", "reverted", "failed", true, false},
		{"no matching history request", "compensate", "reverted", "succeeded", false, false},
		{"wrong recovery direction", "resume", "reverted", "succeeded", true, false},
	} {
		request, execution, draft := jobstest.Request(t, p, "a", actor)
		p.Exec(t, `INSERT INTO migration_attempts (id, database_id, migration_id, request_id, draft_revision, candidate_version,
			sql_text, sql_sha256, dialect, statement_count, executor_id, status, started_at, finished_at, mode)
			VALUES ($1, 'a', $2, $3, 1, 1, 'SELECT 1', repeat('a', 64), 'postgresql', 1, $4, 'unknown', now(), now(), 'batch')`, execution, draft, request, actor)
		addRecovery := func(op string) jobs.Job {
			r, e := jobstest.ID(t), jobstest.ID(t)
			p.Exec(t, `INSERT INTO migration_requests (id, database_id, migration_id, operation, recovers_execution_id,
				definition_revision, definition_sha256, expected_version, requested_by, status, execution_id, created_at, updated_at)
				VALUES ($1, 'a', $2, $3, $4, 1, repeat('a', 64), 0, $5, 'queued', $6, now(), now())`, r, draft, op, execution, actor, e)
			return jobstest.Insert(t, p, jobs.Job{DatabaseID: "a", MigrationID: draft, RequestID: r, ExecutionID: e,
				RecoversExecutionID: execution, Operation: op, ActorID: actor})
		}
		stopped := addRecovery(tc.operation)
		done := addRecovery(tc.operation)
		p.Exec(t, `UPDATE jobs SET status=$2, finished_at=now(), effect=CASE WHEN $2='succeeded' THEN 'committed' ELSE 'none' END WHERE id=$1`, done.ID, tc.recoveryStatus)
		var recoveredBy *string
		if tc.link {
			recoveredBy = &done.RequestID
		}
		p.Exec(t, `UPDATE migration_attempts SET status=$2, recovered_by_request_id=$3,
			applied_version=CASE WHEN $2='succeeded' THEN candidate_version END WHERE id=$1`, execution, tc.attemptStatus, recoveredBy)
		original := jobstest.Insert(t, p, jobs.Job{DatabaseID: "a", MigrationID: draft, RequestID: request, ExecutionID: execution, Operation: "up", ActorID: actor})
		p.Exec(t, `UPDATE jobs SET status='blocked', effect='partial', finished_at=now() WHERE id IN ($1,$2)`, original.ID, stopped.ID)
		want, wantOriginal := "", ""
		if tc.resolves {
			want, wantOriginal = done.ID, done.ID
		}
		if tc.name == "compensated" {
			// The old worker linked the original but left the stopped recovery blocking.
			p.Exec(t, `UPDATE jobs SET resolved_by_job_id=$2 WHERE id=$1`, original.ID, stopped.ID)
			wantOriginal = stopped.ID
			// A bad cross-database recovery reference must not release another target.
			r, e, d := jobstest.Request(t, p, "b", actor)
			other := jobstest.Insert(t, p, jobs.Job{DatabaseID: "b", MigrationID: d, RequestID: r, ExecutionID: e,
				RecoversExecutionID: execution, Operation: "compensate", ActorID: actor})
			p.Exec(t, `UPDATE jobs SET status='blocked', effect='unknown', finished_at=now() WHERE id=$1`, other.ID)
			checks = append(checks, check{"other database", "b", other.ID, ""})
		}
		checks = append(checks, check{tc.name + " recovery", "a", stopped.ID, want}, check{tc.name + " original", "a", original.ID, wantOriginal})
	}

	updated := make(map[string]time.Time)
	for pass := 1; pass <= 2; pass++ {
		if err := store.ApplySchema(t.Context()); err != nil {
			t.Fatal(err)
		}
		for _, c := range checks {
			j, err := store.Get(t.Context(), c.db, c.job)
			if err != nil {
				t.Fatal(err)
			}
			if j.Status != jobs.Blocked || j.ResolvedBy != c.want {
				t.Errorf("pass %d, %s: job = %s, resolved_by = %q; want blocked, %q", pass, c.name, j.Status, j.ResolvedBy, c.want)
			}
			if pass == 2 && !j.UpdatedAt.Equal(updated[c.job]) {
				t.Errorf("second schema apply updated %s again", c.name)
			}
			updated[c.job] = j.UpdatedAt
		}
	}
}
