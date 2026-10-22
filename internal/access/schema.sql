-- Canonical shared target identity; Jason owns the one-to-one connection details, not this identity.
CREATE TABLE IF NOT EXISTS database_registrations (
    id text PRIMARY KEY,
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 128),
    active boolean NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS groups (
    id text PRIMARY KEY,
    name text NOT NULL UNIQUE CHECK (length(btrim(name)) BETWEEN 1 AND 128),
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS group_memberships (
    user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id text NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, group_id)
);

CREATE TABLE IF NOT EXISTS group_database_grants (
    group_id text NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    database_id text NOT NULL REFERENCES database_registrations(id),
    permission text NOT NULL CHECK (permission IN ('view', 'edit')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (group_id, database_id)
);
