-- +goose Up
-- Optional single-user sync. See docs/design-spec.md §7.

-- Single-row sync configuration and the server generation the base reflects.
-- The CHECK pins it to one row, matching the head table's shape.
CREATE TABLE sync (
    id         INTEGER PRIMARY KEY CHECK (id = 0),
    project_id TEXT,                        -- ULID naming this project across machines
    remote_url TEXT,
    synced_gen INTEGER NOT NULL DEFAULT 0,  -- server generation sync_base reflects
    synced_at  INTEGER                      -- unix milliseconds, display only
);

INSERT INTO sync (id, synced_gen) VALUES (0, 0);

-- The last-synced graph: spor's equivalent of a remote-tracking branch, and what
-- makes push/pull a three-way merge rather than a set-difference. Without it,
-- "state X is absent locally" is ambiguous between "I deleted it" and "the server
-- added it", so deletions could never propagate (docs/design-spec.md §7).
--
-- manifest_hash is immutable for a given state id (history editing re-parents and
-- relabels, but never rewrites contents), so these columns are all a merge needs.
-- parent_id carries no foreign key on purpose: the base describes a past graph and
-- may reference states that no longer exist locally.
CREATE TABLE sync_base (
    state_id      TEXT PRIMARY KEY,
    parent_id     TEXT,
    created_at    INTEGER NOT NULL,
    manifest_hash TEXT    NOT NULL,
    label         TEXT
);

-- +goose Down
DROP TABLE sync_base;
DROP TABLE sync;
