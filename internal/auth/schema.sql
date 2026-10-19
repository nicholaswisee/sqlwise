CREATE TABLE IF NOT EXISTS users (
    id text PRIMARY KEY,
    username text NOT NULL UNIQUE CHECK (username ~ '^[a-z0-9][a-z0-9_.-]{2,63}$'),
    password_hash text NOT NULL,
    is_platform_admin boolean NOT NULL DEFAULT false,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
