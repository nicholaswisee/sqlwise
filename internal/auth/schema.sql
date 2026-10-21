CREATE TABLE IF NOT EXISTS users (
    id text PRIMARY KEY,
    username text NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9][a-z0-9_.-]{2,63}$'),
    password_hash text NOT NULL,
    is_platform_admin boolean NOT NULL DEFAULT false,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE users ADD COLUMN IF NOT EXISTS authz_epoch bigint NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS auth_session_families (
    id text PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL CHECK (absolute_expires_at > created_at),
    csrf_token text NOT NULL CHECK (length(csrf_token) = 43),
    revoked_at timestamptz
);

CREATE INDEX IF NOT EXISTS auth_session_families_user_id_idx ON auth_session_families (user_id);

CREATE TABLE IF NOT EXISTS auth_sessions (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    user_id text REFERENCES users(id) ON DELETE CASCADE,
    csrf_token text NOT NULL CHECK (length(csrf_token) = 43),
    created_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at > created_at)
);

ALTER TABLE auth_sessions ADD COLUMN IF NOT EXISTS family_id text REFERENCES auth_session_families(id);

INSERT INTO auth_session_families (id, user_id, created_at, last_seen_at, absolute_expires_at, csrf_token)
SELECT encode(s.token_hash, 'hex'), s.user_id, s.created_at, s.last_seen_at, s.expires_at, s.csrf_token
FROM auth_sessions s
WHERE s.user_id IS NOT NULL AND s.family_id IS NULL
ON CONFLICT (id) DO NOTHING;

UPDATE auth_sessions s SET family_id = encode(s.token_hash, 'hex')
WHERE s.user_id IS NOT NULL AND s.family_id IS NULL;

CREATE INDEX IF NOT EXISTS auth_sessions_user_id_idx ON auth_sessions (user_id);
CREATE INDEX IF NOT EXISTS auth_sessions_expires_at_idx ON auth_sessions (expires_at);
CREATE INDEX IF NOT EXISTS auth_sessions_family_id_idx ON auth_sessions (family_id);
