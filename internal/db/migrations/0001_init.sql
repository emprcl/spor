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

-- +goose Down
DROP TABLE head;
DROP TABLE manifest_entries;
DROP TABLE states;
