-- Requires the auth, access and migration schemas.
-- Durable job queue: one job per consumed request, claimed by Go workers.
CREATE TABLE IF NOT EXISTS jobs (
    id text PRIMARY KEY,
    database_id text NOT NULL REFERENCES database_registrations(id),
    migration_id text NOT NULL,
    request_id text NOT NULL UNIQUE REFERENCES migration_requests(id),
    execution_id text NOT NULL UNIQUE,
    recovers_execution_id text,
    operation text NOT NULL CHECK (operation IN ('up', 'rollback', 'forward', 'resume', 'compensate')),
    actor_id text NOT NULL REFERENCES users(id),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    traceparent text CHECK (traceparent IS NULL OR length(traceparent) <= 128),
    tracestate text CHECK (tracestate IS NULL OR length(tracestate) <= 512),
    status text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'blocked')),
    effect text NOT NULL DEFAULT 'none' CHECK (effect IN ('none', 'committed', 'partial', 'unknown')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts integer NOT NULL CHECK (max_attempts >= 1),
    next_run_at timestamptz NOT NULL,
    claimed_by text,
    lease_until timestamptz,
    committed_batches bigint NOT NULL DEFAULT 0 CHECK (committed_batches >= 0),
    committed_rows bigint NOT NULL DEFAULT 0 CHECK (committed_rows >= 0),
    total_rows bigint CHECK (total_rows IS NULL OR total_rows >= 0),
    error_message text CHECK (error_message IS NULL OR length(error_message) <= 2000),
    resolved_by_job_id text REFERENCES jobs(id),
    created_at timestamptz NOT NULL,
    started_at timestamptz,
    finished_at timestamptz,
    updated_at timestamptz NOT NULL,
    UNIQUE (database_id, idempotency_key),
    FOREIGN KEY (database_id, migration_id) REFERENCES migration_drafts(database_id, id),
    CHECK ((status = 'running') = (claimed_by IS NOT NULL AND lease_until IS NOT NULL)),
    CHECK ((status IN ('succeeded', 'failed', 'blocked')) = (finished_at IS NOT NULL)),
    CHECK (status <> 'blocked' OR effect IN ('partial', 'unknown')),
    CHECK (status <> 'succeeded' OR effect = 'committed'),
    CHECK ((operation IN ('resume', 'compensate')) = (recovers_execution_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS jobs_claim_idx ON jobs (next_run_at, created_at) WHERE status IN ('queued', 'running', 'blocked');
CREATE INDEX IF NOT EXISTS jobs_database_idx ON jobs (database_id, created_at DESC);
CREATE INDEX IF NOT EXISTS jobs_blocking_idx ON jobs (database_id) WHERE status = 'blocked' AND resolved_by_job_id IS NULL;

-- Every claim of a job is one delivery with its own ID; retries are new deliveries
-- of the same job and logical execution.
CREATE TABLE IF NOT EXISTS job_deliveries (
    id text PRIMARY KEY,
    job_id text NOT NULL REFERENCES jobs(id),
    worker_id text NOT NULL,
    claimed_at timestamptz NOT NULL,
    finished_at timestamptz,
    outcome text CHECK (outcome IN ('succeeded', 'failed', 'blocked', 'requeued', 'abandoned')),
    CHECK ((finished_at IS NULL) = (outcome IS NULL))
);
CREATE INDEX IF NOT EXISTS job_deliveries_job_idx ON job_deliveries (job_id, claimed_at);

-- Finished jobs are final; a blocked job may only settle or be resolved by recovery.
CREATE OR REPLACE FUNCTION jobs_terminal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'jobs are retained' USING ERRCODE = '55000';
    END IF;
    IF (NEW.id, NEW.database_id, NEW.migration_id, NEW.request_id, NEW.execution_id, NEW.recovers_execution_id, NEW.operation,
        NEW.actor_id, NEW.idempotency_key, NEW.created_at)
        IS DISTINCT FROM
       (OLD.id, OLD.database_id, OLD.migration_id, OLD.request_id, OLD.execution_id, OLD.recovers_execution_id, OLD.operation,
        OLD.actor_id, OLD.idempotency_key, OLD.created_at) THEN
        RAISE EXCEPTION 'job identity is immutable' USING ERRCODE = '55000';
    END IF;
    IF OLD.status IN ('succeeded', 'failed') THEN
        RAISE EXCEPTION 'finished job is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE TRIGGER jobs_terminal
    BEFORE UPDATE OR DELETE ON jobs
    FOR EACH ROW EXECUTE FUNCTION jobs_terminal();

-- Older workers could leave intermediate blockers after a recovery succeeded.
UPDATE jobs AS blocked
SET resolved_by_job_id = recovery.id, updated_at = now()
FROM migration_attempts AS attempt
JOIN migration_requests AS request ON request.id = attempt.recovered_by_request_id
    AND request.database_id = attempt.database_id AND request.migration_id = attempt.migration_id
    AND request.recovers_execution_id = attempt.id
JOIN jobs AS recovery ON recovery.request_id = request.id AND recovery.execution_id = request.execution_id
    AND recovery.database_id = request.database_id AND recovery.migration_id = request.migration_id
    AND recovery.operation = request.operation AND recovery.recovers_execution_id = attempt.id
WHERE recovery.status = 'succeeded' AND attempt.mode = 'batch'
    AND ((recovery.operation = 'compensate' AND attempt.status = 'reverted')
      OR (recovery.operation = 'resume' AND attempt.status = 'succeeded'))
    AND blocked.status = 'blocked' AND blocked.resolved_by_job_id IS NULL
    AND blocked.database_id = attempt.database_id AND blocked.migration_id = attempt.migration_id
    AND COALESCE(blocked.recovers_execution_id, blocked.execution_id) = attempt.id;
