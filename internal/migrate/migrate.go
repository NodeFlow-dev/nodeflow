// Package migrate applies the embedded PostgreSQL migrations with the same
// semantics as scripts/migrate.sh: one session-level advisory lock, a
// schema_migrations(version) ledger keyed by the numeric file prefix, and
// every pending *.up.sql file applied in its own transaction together with
// its ledger row. Databases migrated by either runner are interchangeable.
package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// advisoryLockKey matches scripts/migrate.sh so the shell and Go runners
// exclude each other.
const advisoryLockKey int64 = 813462739652019420

// Migration is one forward migration file.
type Migration struct {
	Version string
	Name    string
	SQL     string
}

// Load returns the *.up.sql files of files sorted by name. Versions must be
// unique numeric prefixes (000001_init.up.sql has version 000001).
func Load(files fs.FS) ([]Migration, error) {
	names, err := fs.Glob(files, "*.up.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	seen := make(map[string]string, len(names))
	out := make([]Migration, 0, len(names))
	for _, name := range names {
		version, _, ok := strings.Cut(path.Base(name), "_")
		if !ok || version == "" || strings.Trim(version, "0123456789") != "" {
			return nil, fmt.Errorf("migration %s has no numeric version prefix", name)
		}
		if previous, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %s", previous, name, version)
		}
		seen[version] = name
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: version, Name: name, SQL: string(body)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no migrations found")
	}
	return out, nil
}

// Apply runs every pending migration and returns the versions it applied.
// logf, when not nil, receives one line per applied migration.
func Apply(ctx context.Context, conn *pgx.Conn, migrations []Migration, logf func(format string, args ...any)) ([]string, error) {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// The lock is session scoped; closing the connection releases it too.
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, advisoryLockKey)
	}()
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	var applied []string
	for _, migration := range migrations {
		var pending bool
		if err := conn.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, migration.Version).Scan(&pending); err != nil {
			return applied, fmt.Errorf("check migration %s: %w", migration.Version, err)
		}
		if !pending {
			continue
		}
		if logf != nil {
			logf("applying migration %s", migration.Version)
		}
		if err := applyOne(ctx, conn, migration); err != nil {
			return applied, err
		}
		applied = append(applied, migration.Version)
	}
	return applied, nil
}

func applyOne(ctx context.Context, conn *pgx.Conn, migration Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", migration.Version, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Without arguments pgx uses the simple query protocol, so a file may
	// contain several statements, as with psql \ir.
	if _, err := tx.Exec(ctx, migration.SQL); err != nil {
		return fmt.Errorf("migration %s (%s): %w", migration.Version, migration.Name, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, migration.Version); err != nil {
		return fmt.Errorf("record migration %s: %w", migration.Version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %s: %w", migration.Version, err)
	}
	return nil
}
