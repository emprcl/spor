// Package core is spor's UI-agnostic engine: it owns the store and the
// operations (snapshot, and later restore/prune/compact/…). Front-ends (the CLI,
// the watcher, a future TUI) are thin callers. See docs/SPEC.md §8.
package core

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go driver, registers as "sqlite"

	"github.com/emprcl/spor/internal/blob"
	"github.com/emprcl/spor/internal/db"
	"github.com/emprcl/spor/internal/db/gen"
)

// storageDir is the project-local directory spor owns.
const storageDir = ".spor"

// Engine holds an opened project store. Create it with Open and release it with
// Close.
type Engine struct {
	root     string
	storeDir string
	dbPath   string

	db    *sql.DB
	q     *gen.Queries
	blobs *blob.Store
}

// Open prepares the store for the project rooted at root: it creates the .spor
// layout, opens SQLite (WAL + pragmas), runs the schema-version gate and
// migrations, and clears stale temp files (crash-recovery stub). See
// docs/SPEC.md §3, §8.
func Open(ctx context.Context, root string) (*Engine, error) {
	storeDir := filepath.Join(root, storageDir)
	blobsDir := filepath.Join(storeDir, "blobs")
	tmpDir := filepath.Join(storeDir, "tmp")
	dbPath := filepath.Join(storeDir, "spor.db")

	for _, d := range []string{storeDir, blobsDir, tmpDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}

	// Crash-recovery stub: discard any temp files left by an interrupted write.
	// Full recovery lands with the watcher (docs/SPEC.md §8).
	if err := clearDir(tmpDir); err != nil {
		return nil, err
	}

	sqldb, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, err
	}
	// Single connection: this is a local single-writer store, and it sidesteps
	// SQLITE_BUSY entirely for now. Concurrent readers can be enabled later.
	sqldb.SetMaxOpenConns(1)

	if err := db.Migrate(ctx, sqldb, dbPath); err != nil {
		sqldb.Close()
		return nil, err
	}

	blobs, err := blob.New(blobsDir, tmpDir)
	if err != nil {
		sqldb.Close()
		return nil, err
	}

	return &Engine{
		root:     root,
		storeDir: storeDir,
		dbPath:   dbPath,
		db:       sqldb,
		q:        gen.New(sqldb),
		blobs:    blobs,
	}, nil
}

// Close releases the store.
func (e *Engine) Close() error {
	return e.db.Close()
}

// writeLockPath is the flock target for the per-operation write lock.
func (e *Engine) writeLockPath() string {
	return filepath.Join(e.storeDir, "write.lock")
}

// dsn builds the modernc SQLite connection string with the pragmas spor relies
// on: WAL journaling, a busy timeout, and enforced foreign keys.
func dsn(path string) string {
	return "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(10000)" +
		"&_pragma=foreign_keys(on)"
}

// clearDir removes the contents of dir without removing dir itself.
func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("clearing %s: %w", dir, err)
		}
	}
	return nil
}
