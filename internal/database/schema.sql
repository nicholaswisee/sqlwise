CREATE TABLE IF NOT EXISTS database_connections (
    database_id text PRIMARY KEY REFERENCES database_registrations(id),
    dbms text NOT NULL CHECK (dbms IN ('postgresql', 'mysql')),
    host text NOT NULL CHECK (length(btrim(host)) >= 1),
    port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
    database_name text NOT NULL CHECK (length(database_name) >= 1),
    username text NOT NULL CHECK (length(username) >= 1),
    tls_mode text NOT NULL CHECK (tls_mode IN ('disable', 'require', 'verify-full')),
    password_sealed bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
