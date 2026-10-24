package database

import "sqlwise/internal/database/dialect"

func ReceiptSetupSQL(migrationRole string) string {
	role := dialect.Postgres.Quote(migrationRole)
	return `CREATE SCHEMA IF NOT EXISTS ` + InternalSchema + `;
CREATE TABLE IF NOT EXISTS ` + InternalSchema + `.migration_receipts (
    attempt_id text PRIMARY KEY,
    database_id text NOT NULL,
    version bigint NOT NULL CHECK (version >= 1),
    sql_sha256 text NOT NULL,
    outcome text NOT NULL CHECK (outcome IN ('applied', 'fenced')),
    recorded_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON SCHEMA ` + InternalSchema + ` FROM PUBLIC;
REVOKE ALL ON ` + InternalSchema + `.migration_receipts FROM PUBLIC;
GRANT USAGE ON SCHEMA ` + InternalSchema + ` TO ` + role + `;
GRANT SELECT, INSERT ON ` + InternalSchema + `.migration_receipts TO ` + role + `;
`
}
