// Package db owns the SQLite schema: embedded goose migrations, the
// schema-version skew gate, and the sqlc-generated queries (see ./gen).
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const migrationsDir = "migrations"

// Dialect is the goose dialect for SQLite. The modernc driver registers under
// the name "sqlite"; goose maps this dialect to it.
const Dialect = "sqlite3"

// Migrate brings the store's schema up to the version embedded in this binary,
// gating on version skew (docs/SPEC.md §8, "Schema versioning"):
//
//   - store version <  binary → migrate up (backing up first)
//   - store version == binary → no-op
//   - store version >  binary → refuse; the store was written by a newer spor
//
// dbPath is the on-disk path of the SQLite file, used for the pre-upgrade backup.
func Migrate(ctx context.Context, db *sql.DB, dbPath string) error {
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect(Dialect); err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}

	binVer, err := maxEmbeddedVersion()
	if err != nil {
		return fmt.Errorf("reading embedded migrations: %w", err)
	}

	// GetDBVersion ensures the goose version table exists and returns 0 for a
	// fresh store. The version is a plain integer readable even by a binary that
	// does not know the newer migrations.
	dbVer, err := goose.GetDBVersionContext(ctx, db)
	if err != nil {
		return fmt.Errorf("reading store schema version: %w", err)
	}

	switch {
	case dbVer > binVer:
		return fmt.Errorf(
			"this project's spor store uses schema v%d, but your spor supports v%d; upgrade spor to continue",
			dbVer, binVer,
		)
	case dbVer == binVer:
		return nil
	}

	// dbVer < binVer: an upgrade will run. Back up an existing store first so an
	// interrupted migration is recoverable. VACUUM INTO writes a clean,
	// consistent single-file copy (no WAL/-shm to reconcile).
	if dbVer > 0 {
		bak := dbPath + ".bak"
		_ = os.Remove(bak)
		if _, err := db.ExecContext(ctx, "VACUUM INTO ?", bak); err != nil {
			return fmt.Errorf("backing up store before migration: %w", err)
		}
	}

	if err := goose.UpContext(ctx, db, migrationsDir); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

// maxEmbeddedVersion returns the highest migration version compiled into this
// binary, parsed from the leading number of each migrations/NNNN_*.sql file.
func maxEmbeddedVersion() (int64, error) {
	entries, err := fs.ReadDir(migrationsFS, migrationsDir)
	if err != nil {
		return 0, err
	}
	var max int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		numStr, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max, nil
}
