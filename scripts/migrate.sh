#!/bin/sh
set -eu

: "${PGHOST:?PGHOST is required}"
: "${PGDATABASE:?PGDATABASE is required}"
: "${PGUSER:?PGUSER is required}"
: "${PGPASSWORD:?PGPASSWORD is required}"

# All statements and included migration files run through one psql backend.
# The session-level advisory lock therefore covers baseline detection, every
# migration and its schema_migrations marker; disconnect also releases it.
{
  cat <<'SQL'
SELECT pg_advisory_lock(813462739652019420);

CREATE TABLE IF NOT EXISTS schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);
SQL

  for file in /migrations/*.up.sql; do
    version=$(basename "$file" | cut -d_ -f1)
    printf "SELECT NOT EXISTS(SELECT 1 FROM schema_migrations WHERE version='%s') AS migration_pending \\gset\n" "$version"
    printf '%s\n' '\if :migration_pending'
    printf '%s\n' "\\echo applying migration $version"
    printf '%s\n' 'BEGIN;'
    printf '%s\n' "\\ir $file"
    printf "INSERT INTO schema_migrations(version) VALUES('%s');\n" "$version"
    printf '%s\n' 'COMMIT;'
    printf '%s\n' '\endif'
  done

  cat <<'SQL'

SELECT pg_advisory_unlock(813462739652019420);
SQL
} | psql -X -q -v ON_ERROR_STOP=1
