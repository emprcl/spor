-- +goose Up
-- Core state graph. See docs/SPEC.md §2 (Data Model) and §3 (Storage).

-- A state is an immutable snapshot of the project. Its id is an opaque ULID
-- (not derived from content or parent) so history editing can re-parent without
-- cascading ids. parent_id is an editable FK; the root state has none.
CREATE TABLE states (
    id            TEXT    PRIMARY KEY,
    created_at    INTEGER NOT NULL,               -- unix milliseconds, display only
    parent_id     TEXT    REFERENCES states(id) ON DELETE RESTRICT,
    manifest_hash TEXT    NOT NULL,               -- SHA-256 over the canonical manifest
    label         TEXT
);

CREATE INDEX idx_states_parent ON states(parent_id);

-- A label is a unique alias for a state, like its id (docs/SPEC.md §2, §6).
-- Unlabeled states store NULL, and SQLite treats NULLs as distinct under a
-- UNIQUE index, so any number of states may be unlabeled while every non-null
-- label names exactly one state.
CREATE UNIQUE INDEX idx_states_label ON states(label);

-- The manifest of a state, stored as rows (path -> blob hash) so GC, verify, and
-- dedup can query referenced blobs directly in SQL. Blob contents live on disk.
-- executable is the one preserved permission bit (0 or 1); see docs/SPEC.md §2.
CREATE TABLE manifest_entries (
    state_id   TEXT    NOT NULL REFERENCES states(id) ON DELETE CASCADE,
    path       TEXT    NOT NULL,
    blob_hash  TEXT    NOT NULL,                  -- SHA-256(content)
    executable INTEGER NOT NULL DEFAULT 0,        -- owner-execute bit, 0 or 1
    PRIMARY KEY (state_id, path)
);

CREATE INDEX idx_manifest_blob ON manifest_entries(blob_hash);

-- Single-row pointer to the current working state (HEAD). The CHECK pins it to
-- one row; state_id is NULL only before the first snapshot.
CREATE TABLE head (
    id       INTEGER PRIMARY KEY CHECK (id = 0),
    state_id TEXT REFERENCES states(id) ON DELETE SET NULL
);

INSERT INTO head (id, state_id) VALUES (0, NULL);

-- Append-only journal of HEAD moves (snapshot, restore, undo, redo, prune,
-- compact). Powers redo ("return to the state I just left"); purely additive
-- metadata the tree never depends on. Local, never synced. See docs/SPEC.md §2.
CREATE TABLE head_history (
    seq      INTEGER PRIMARY KEY AUTOINCREMENT,
    state_id TEXT    NOT NULL REFERENCES states(id) ON DELETE CASCADE,
    moved_at INTEGER NOT NULL                       -- unix milliseconds
);

CREATE INDEX idx_head_history_state ON head_history(state_id);

-- Advisory stat cache: lets a snapshot reuse the last recorded blob hash for a
-- file whose (size, mtime_ns, inode) are unchanged instead of re-reading it.
-- recorded_at (unix nanoseconds) drives the racily-clean rule: rows whose file
-- mtime is not older than recorded_at are never trusted. See docs/SPEC.md §4.
CREATE TABLE stat_cache (
    path        TEXT    PRIMARY KEY,
    size        INTEGER NOT NULL,
    mtime_ns    INTEGER NOT NULL,
    inode       INTEGER NOT NULL,                   -- 0 where unavailable (Windows)
    blob_hash   TEXT    NOT NULL,
    recorded_at INTEGER NOT NULL                    -- unix nanoseconds
);

-- +goose Down
DROP TABLE stat_cache;
DROP TABLE head_history;
DROP TABLE head;
DROP TABLE manifest_entries;
DROP TABLE states;
