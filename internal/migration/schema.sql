-- Requires the auth (users) and access (database_registrations) schemas.
CREATE TABLE IF NOT EXISTS migration_drafts (
    id text PRIMARY KEY,
    database_id text NOT NULL REFERENCES database_registrations(id),
    title text NOT NULL CHECK (length(btrim(title)) BETWEEN 1 AND 200),
    sql_text text NOT NULL CHECK (octet_length(sql_text) BETWEEN 1 AND 262144),
    dialect text NOT NULL CHECK (dialect IN ('postgresql', 'mysql')),
    revision bigint NOT NULL CHECK (revision >= 1),
    created_by text NOT NULL REFERENCES users(id),
    updated_by text NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (database_id, id)
);

CREATE INDEX IF NOT EXISTS migration_drafts_database_idx ON migration_drafts (database_id, created_at, id);

-- M2 definitions: SQL down, or irreversible with a reason (a non-null reason
-- is the irreversible flag). Incomplete drafts may be saved but not reviewed.
ALTER TABLE migration_drafts ADD COLUMN IF NOT EXISTS sql_down text
    CHECK (sql_down IS NULL OR octet_length(sql_down) BETWEEN 1 AND 262144);
ALTER TABLE migration_drafts ADD COLUMN IF NOT EXISTS irreversible_reason text
    CHECK (irreversible_reason IS NULL OR length(btrim(irreversible_reason)) BETWEEN 1 AND 500);
DO $$ BEGIN
    ALTER TABLE migration_drafts ADD CONSTRAINT migration_drafts_down_or_irreversible
        CHECK (sql_down IS NULL OR irreversible_reason IS NULL);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- Review requests bind one operation to an exact definition revision and
-- history position; approvals are recorded per authorized group and round.
CREATE TABLE IF NOT EXISTS migration_requests (
    id text PRIMARY KEY,
    database_id text NOT NULL REFERENCES database_registrations(id),
    migration_id text NOT NULL,
    operation text NOT NULL CHECK (operation IN ('up', 'rollback', 'forward')),
    definition_revision bigint NOT NULL CHECK (definition_revision >= 1),
    definition_sha256 text NOT NULL CHECK (definition_sha256 ~ '^[0-9a-f]{64}$'),
    expected_version bigint NOT NULL CHECK (expected_version >= 0),
    cancels_execution_id text,
    requested_by text NOT NULL REFERENCES users(id),
    status text NOT NULL CHECK (status IN ('draft', 'pending_review', 'approved', 'rejected', 'queued')),
    review_round integer NOT NULL DEFAULT 0 CHECK (review_round >= 0),
    policy_sha256 text CHECK (policy_sha256 IS NULL OR policy_sha256 ~ '^[0-9a-f]{64}$'),
    execution_id text UNIQUE,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    FOREIGN KEY (database_id, migration_id) REFERENCES migration_drafts(database_id, id),
    CHECK ((operation = 'rollback') = (cancels_execution_id IS NOT NULL)),
    CHECK ((status = 'queued') = (execution_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS migration_requests_migration_idx ON migration_requests (database_id, migration_id, created_at);

CREATE TABLE IF NOT EXISTS migration_approvals (
    request_id text NOT NULL REFERENCES migration_requests(id),
    review_round integer NOT NULL CHECK (review_round >= 1),
    group_id text NOT NULL REFERENCES groups(id),
    approver_id text NOT NULL REFERENCES users(id),
    decision text NOT NULL CHECK (decision IN ('approved', 'rejected')),
    decided_at timestamptz NOT NULL,
    PRIMARY KEY (request_id, review_round, group_id)
);

-- One row per target; holding it FOR UPDATE serializes executions across processes.
CREATE TABLE IF NOT EXISTS migration_locks (
    database_id text PRIMARY KEY REFERENCES database_registrations(id)
);

CREATE TABLE IF NOT EXISTS migration_attempts (
    id text PRIMARY KEY,
    seq bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    database_id text NOT NULL,
    migration_id text NOT NULL,
    draft_revision bigint NOT NULL CHECK (draft_revision >= 1),
    candidate_version bigint NOT NULL CHECK (candidate_version >= 1),
    applied_version bigint,
    sql_text text NOT NULL,
    sql_sha256 text NOT NULL CHECK (sql_sha256 ~ '^[0-9a-f]{64}$'),
    dialect text NOT NULL CHECK (dialect IN ('postgresql', 'mysql')),
    statement_count integer NOT NULL CHECK (statement_count >= 1),
    executor_id text NOT NULL REFERENCES users(id),
    status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'partially_applied', 'unknown')),
    error_message text CHECK (error_message IS NULL OR length(error_message) <= 2000),
    failed_statement integer CHECK (failed_statement IS NULL OR failed_statement >= 1),
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    reconciled_at timestamptz,
    FOREIGN KEY (database_id, migration_id) REFERENCES migration_drafts(database_id, id),
    CHECK ((status = 'running') = (finished_at IS NULL)),
    CHECK ((status = 'succeeded') = (applied_version IS NOT NULL)),
    CHECK (applied_version IS NULL OR applied_version = candidate_version)
);

-- M2 executions. Succeeded rows are the committed history; a rollback
-- references the successful execution it cancels.
ALTER TABLE migration_attempts ADD COLUMN IF NOT EXISTS operation text NOT NULL DEFAULT 'up'
    CHECK (operation IN ('up', 'rollback', 'forward'));
ALTER TABLE migration_attempts ADD COLUMN IF NOT EXISTS request_id text UNIQUE REFERENCES migration_requests(id);
ALTER TABLE migration_attempts ADD COLUMN IF NOT EXISTS cancels_execution_id text REFERENCES migration_attempts(id);

-- Batch-mode executions and their recovery (resume or compensate).
ALTER TABLE migration_attempts ADD COLUMN IF NOT EXISTS mode text NOT NULL DEFAULT 'transaction'
    CHECK (mode IN ('transaction', 'batch'));
ALTER TABLE migration_attempts ADD COLUMN IF NOT EXISTS recovered_by_request_id text REFERENCES migration_requests(id);
ALTER TABLE migration_attempts DROP CONSTRAINT IF EXISTS migration_attempts_status_check;
ALTER TABLE migration_attempts ADD CONSTRAINT migration_attempts_status_check
    CHECK (status IN ('running', 'succeeded', 'failed', 'partially_applied', 'unknown', 'reverted'));

ALTER TABLE migration_drafts ADD COLUMN IF NOT EXISTS batch_spec jsonb;
ALTER TABLE migration_drafts ADD COLUMN IF NOT EXISTS batch_size integer
    CHECK (batch_size IS NULL OR batch_size BETWEEN 1 AND 100000);
DO $$ BEGIN
    ALTER TABLE migration_drafts ADD CONSTRAINT migration_drafts_batch_mode
        CHECK ((batch_spec IS NULL) = (batch_size IS NULL));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

ALTER TABLE migration_requests ADD COLUMN IF NOT EXISTS recovers_execution_id text REFERENCES migration_attempts(id);
ALTER TABLE migration_requests DROP CONSTRAINT IF EXISTS migration_requests_operation_check;
ALTER TABLE migration_requests ADD CONSTRAINT migration_requests_operation_check
    CHECK (operation IN ('up', 'rollback', 'forward', 'resume', 'compensate'));
DO $$ BEGIN
    ALTER TABLE migration_requests ADD CONSTRAINT migration_requests_recovery
        CHECK ((operation IN ('resume', 'compensate')) = (recovers_execution_id IS NOT NULL));
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

CREATE UNIQUE INDEX IF NOT EXISTS migration_attempts_applied_version_idx
    ON migration_attempts (database_id, applied_version) WHERE applied_version IS NOT NULL;
-- Rollback and forward re-run a migration; only its first application is an up.
DROP INDEX IF EXISTS migration_attempts_one_success_per_draft_idx;
CREATE UNIQUE INDEX IF NOT EXISTS migration_attempts_one_up_per_migration_idx
    ON migration_attempts (migration_id) WHERE status = 'succeeded' AND operation = 'up';
CREATE UNIQUE INDEX IF NOT EXISTS migration_attempts_one_running_per_database_idx
    ON migration_attempts (database_id) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS migration_attempts_migration_idx ON migration_attempts (migration_id, seq);
CREATE INDEX IF NOT EXISTS migration_attempts_database_idx ON migration_attempts (database_id, seq);

-- History is append-only: snapshots never change, finished attempts are final,
-- and only running/unknown attempts may move to a later state.
CREATE OR REPLACE FUNCTION migration_attempts_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'migration history is immutable' USING ERRCODE = '55000';
    END IF;
    IF (NEW.id, NEW.seq, NEW.database_id, NEW.migration_id, NEW.draft_revision, NEW.candidate_version,
        NEW.sql_text, NEW.sql_sha256, NEW.dialect, NEW.statement_count, NEW.executor_id, NEW.started_at,
        NEW.operation, NEW.request_id, NEW.cancels_execution_id, NEW.mode)
        IS DISTINCT FROM
       (OLD.id, OLD.seq, OLD.database_id, OLD.migration_id, OLD.draft_revision, OLD.candidate_version,
        OLD.sql_text, OLD.sql_sha256, OLD.dialect, OLD.statement_count, OLD.executor_id, OLD.started_at,
        OLD.operation, OLD.request_id, OLD.cancels_execution_id, OLD.mode) THEN
        RAISE EXCEPTION 'migration attempt snapshot is immutable' USING ERRCODE = '55000';
    END IF;
    IF OLD.status IN ('partially_applied', 'unknown') AND OLD.dialect = 'mysql' AND OLD.reconciled_at IS NULL AND NEW.reconciled_at IS NOT NULL
        AND (NEW.status, NEW.applied_version, NEW.error_message, NEW.failed_statement, NEW.finished_at)
        IS NOT DISTINCT FROM (OLD.status, OLD.applied_version, OLD.error_message, OLD.failed_statement, OLD.finished_at) THEN
        RETURN NEW;
    END IF;
    -- A partial batch execution is settled by an approved resume or compensation.
    IF OLD.status = 'partially_applied' AND OLD.mode = 'batch' AND OLD.recovered_by_request_id IS NULL
        AND NEW.status IN ('partially_applied', 'succeeded', 'reverted') THEN
        RETURN NEW;
    END IF;
    IF OLD.status NOT IN ('running', 'unknown')
        OR (OLD.status = 'unknown' AND NEW.status NOT IN ('unknown', 'succeeded', 'failed')) THEN
        RAISE EXCEPTION 'finished migration attempt is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE TRIGGER migration_attempts_immutable
    BEFORE UPDATE OR DELETE ON migration_attempts
    FOR EACH ROW EXECUTE FUNCTION migration_attempts_immutable();

-- A definition is frozen once it has ever executed, including a failed
-- execution; corrections go through a new migration.
CREATE OR REPLACE FUNCTION migration_drafts_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND (NEW.id, NEW.database_id, NEW.dialect, NEW.created_by, NEW.created_at)
        IS DISTINCT FROM (OLD.id, OLD.database_id, OLD.dialect, OLD.created_by, OLD.created_at) THEN
        RAISE EXCEPTION 'migration identity is immutable' USING ERRCODE = '55000';
    END IF;
    IF EXISTS (SELECT 1 FROM migration_attempts WHERE migration_id = OLD.id) THEN
        RAISE EXCEPTION 'executed migration is immutable' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE TRIGGER migration_drafts_immutable
    BEFORE UPDATE OR DELETE ON migration_drafts
    FOR EACH ROW EXECUTE FUNCTION migration_drafts_immutable();

CREATE OR REPLACE FUNCTION migration_approvals_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'migration approvals are append-only' USING ERRCODE = '55000';
END $$;

CREATE OR REPLACE TRIGGER migration_approvals_append_only
    BEFORE UPDATE OR DELETE ON migration_approvals
    FOR EACH ROW EXECUTE FUNCTION migration_approvals_append_only();

-- Upgrade: migrations applied before SQL down existed become explicitly
-- irreversible; no down statement is guessed and nothing else changes.
ALTER TABLE migration_drafts DISABLE TRIGGER migration_drafts_immutable;
UPDATE migration_drafts d SET irreversible_reason = 'Applied before rollback support; no SQL down was recorded.'
    WHERE d.sql_down IS NULL AND d.irreversible_reason IS NULL
    AND EXISTS (SELECT 1 FROM migration_attempts a WHERE a.migration_id = d.id AND a.status = 'succeeded');
ALTER TABLE migration_drafts ENABLE TRIGGER migration_drafts_immutable;
