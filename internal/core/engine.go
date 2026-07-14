// Package core is spor's UI-agnostic engine: it owns the store and the
// operations (snapshot, and later restore/dropfrom/fold/…). Front-ends (the CLI,
// the watcher, a future TUI) are thin callers. See docs/design-spec.md §8.
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
	"github.com/emprcl/spor/internal/lock"
)

// storageDir is the project-local directory spor owns.
const storageDir = ".spor"

// dbMaxConns bounds the SQLite connection pool. WAL plus the flock write lock lets
// several readers run alongside the one serialized writer; a small pool is plenty
// for a single-user tool and avoids unbounded file descriptors.
const dbMaxConns = 8

// Engine holds an opened project store. Create it with OpenOrInit and release it
// with Close.
type Engine struct {
	root     string
	storeDir string
	dbPath   string

	db    *sql.DB
	q     *gen.Queries
	blobs *blob.Store
}

// Discover walks up from start looking for an existing project store (a .spor
// directory), mirroring how Git finds .git. It returns the project root (the
// directory containing .spor) and whether one was found. See docs/design-spec.md §8.
func Discover(start string) (root string, found bool, err error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", false, err
	}
	for {
		info, statErr := os.Stat(filepath.Join(dir, storageDir))
		if statErr == nil && info.IsDir() {
			return dir, true, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir { // reached the filesystem root
			return "", false, nil
		}
		dir = parent
	}
}

// OpenOrInit opens the project store for the directory tree containing start. If
// a store exists at or above start it is used (so running from a subdirectory
// finds the real root). Otherwise a new store is created in start (implicit
// init), unless the guard refuses it. This is the entry point for operations
// that may create a store, such as snapshot. See docs/design-spec.md §8.
func OpenOrInit(ctx context.Context, start string) (*Engine, error) {
	root, found, err := Discover(start)
	if err != nil {
		return nil, err
	}
	if !found {
		root, err = filepath.Abs(start)
		if err != nil {
			return nil, err
		}
		if err := guardImplicitInit(root); err != nil {
			return nil, err
		}
	}
	return openChecked(ctx, root)
}

// ErrNotProject is returned when no store is found at or above the starting
// directory and the operation is not allowed to create one.
var ErrNotProject = fmt.Errorf("not a spor project (no %s found); run 'spor snap' to start one", storageDir)

// OpenExisting opens the store for the tree containing start, discovering the
// project root by walking up. Unlike OpenOrInit it never creates a store: if none
// is found it returns ErrNotProject. This is the entry point for read commands
// such as log. See docs/design-spec.md §8.
func OpenExisting(ctx context.Context, start string) (*Engine, error) {
	root, err := discoverExisting(start)
	if err != nil {
		return nil, err
	}
	return openChecked(ctx, root)
}

// OpenForRepair opens an existing store *without* the on-open consistency check,
// for the two commands that must work on a damaged store: `verify` (which runs its
// own full check) and `forget` (which removes the store). See docs/design-spec.md §8.
func OpenForRepair(ctx context.Context, start string) (*Engine, error) {
	root, err := discoverExisting(start)
	if err != nil {
		return nil, err
	}
	return openAt(ctx, root)
}

// discoverExisting resolves start to a project root, returning ErrNotProject when
// there is no store at or above it.
func discoverExisting(start string) (string, error) {
	root, found, err := Discover(start)
	if err != nil {
		return "", err
	}
	if !found {
		return "", ErrNotProject
	}
	return root, nil
}

// openChecked opens the store at root and runs the on-open consistency check
// (docs/design-spec.md §8), so no command builds on a structurally broken store.
// The store is closed again if the check fails.
func openChecked(ctx context.Context, root string) (*Engine, error) {
	e, err := openAt(ctx, root)
	if err != nil {
		return nil, err
	}
	if err := e.checkConsistency(ctx); err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// guardImplicitInit refuses to implicitly create a store in a directory that is
// almost certainly not a project root: the filesystem root or the user's home
// directory. This stops a stray command in the wrong place from snapshotting an
// enormous tree.
func guardImplicitInit(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if filepath.Dir(abs) == abs {
		return fmt.Errorf("refusing to create a spor store at the filesystem root %q; cd into a project directory first", abs)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if absHome, err := filepath.Abs(home); err == nil && absHome == abs {
			return fmt.Errorf("refusing to create a spor store directly in your home directory (%s); cd into a project directory first", abs)
		}
	}
	return nil
}

// openAt prepares the store rooted at root: it creates the .spor layout, opens
// SQLite (WAL + pragmas), runs the schema-version gate and migrations, and clears
// stale temp files (crash-recovery stub). root is used as-is; discovery and the
// init guard are handled by the callers above. See docs/design-spec.md §3, §8.
func openAt(ctx context.Context, root string) (*Engine, error) {
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
	// Full recovery lands with the watcher (docs/design-spec.md §8).
	if err := clearDir(tmpDir); err != nil {
		return nil, err
	}

	sqldb, err := sql.Open("sqlite", dsn(dbPath))
	if err != nil {
		return nil, err
	}
	// Many readers may run concurrently with the single writer: WAL gives readers a
	// committed snapshot without blocking the writer, the flock write lock (not the
	// DB) serializes writers so two never race, and busy_timeout absorbs any
	// contention. Funnelling everything through one connection is unnecessary.
	sqldb.SetMaxOpenConns(dbMaxConns)

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

// Root returns the project root directory (the parent of .spor). The watcher
// needs it to place its per-directory watches on the whole tree, not just the
// directory a command happened to run from.
func (e *Engine) Root() string {
	return e.root
}

// writeLockPath is the flock target for the per-operation write lock.
func (e *Engine) writeLockPath() string {
	return filepath.Join(e.storeDir, "write.lock")
}

// watcherLockPath is the flock target for the lifetime watcher lock.
func (e *Engine) watcherLockPath() string {
	return filepath.Join(e.storeDir, "watcher.lock")
}

// AcquireWatcher takes the project's watcher lock without blocking, so a second
// `spor watch` fails immediately (docs/design-spec.md §8). Hold the returned lock for
// the watcher's lifetime and release it on stop.
func (e *Engine) AcquireWatcher() (*lock.Watcher, error) {
	return lock.AcquireWatcher(e.watcherLockPath())
}

// WatcherRunning reports whether a `spor watch` currently holds the watcher lock
// for this project. It probes the lock without keeping it, so read commands like
// status and the forget guard can call it safely.
func (e *Engine) WatcherRunning() (bool, error) {
	return lock.WatcherHeld(e.watcherLockPath())
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
