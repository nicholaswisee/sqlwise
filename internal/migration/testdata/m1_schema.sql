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

CREATE UNIQUE INDEX IF NOT EXISTS migration_attempts_applied_version_idx
    ON migration_attempts (database_id, applied_version) WHERE applied_version IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS migration_attempts_one_success_per_draft_idx
    ON migration_attempts (migration_id) WHERE status = 'succeeded';
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
        NEW.sql_text, NEW.sql_sha256, NEW.dialect, NEW.statement_count, NEW.executor_id, NEW.started_at)
        IS DISTINCT FROM
       (OLD.id, OLD.seq, OLD.database_id, OLD.migration_id, OLD.draft_revision, OLD.candidate_version,
        OLD.sql_text, OLD.sql_sha256, OLD.dialect, OLD.statement_count, OLD.executor_id, OLD.started_at) THEN
        RAISE EXCEPTION 'migration attempt snapshot is immutable' USING ERRCODE = '55000';
    END IF;
    IF OLD.status IN ('partially_applied', 'unknown') AND OLD.dialect = 'mysql' AND OLD.reconciled_at IS NULL AND NEW.reconciled_at IS NOT NULL
        AND (NEW.status, NEW.applied_version, NEW.error_message, NEW.failed_statement, NEW.finished_at)
        IS NOT DISTINCT FROM (OLD.status, OLD.applied_version, OLD.error_message, OLD.failed_statement, OLD.finished_at) THEN
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

-- Applied drafts are frozen; corrections go through a new migration. Drafts
-- with an unresolved attempt stay frozen until its outcome is known.
CREATE OR REPLACE FUNCTION migration_drafts_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' AND (NEW.id, NEW.database_id, NEW.dialect, NEW.created_by, NEW.created_at)
        IS DISTINCT FROM (OLD.id, OLD.database_id, OLD.dialect, OLD.created_by, OLD.created_at) THEN
        RAISE EXCEPTION 'migration identity is immutable' USING ERRCODE = '55000';
    END IF;
    IF EXISTS (SELECT 1 FROM migration_attempts WHERE migration_id = OLD.id
        AND status IN ('succeeded', 'running', 'unknown', 'partially_applied')) THEN
        RAISE EXCEPTION 'applied or executing migration is immutable' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE TRIGGER migration_drafts_immutable
    BEFORE UPDATE OR DELETE ON migration_drafts
    FOR EACH ROW EXECUTE FUNCTION migration_drafts_immutable();
