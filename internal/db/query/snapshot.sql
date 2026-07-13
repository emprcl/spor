-- name: GetHead :one
SELECT state_id FROM head WHERE id = 0;

-- name: GetStateManifestHash :one
SELECT manifest_hash FROM states WHERE id = ?;

-- name: CreateState :exec
INSERT INTO states (id, created_at, parent_id, manifest_hash, label)
VALUES (?, ?, ?, ?, ?);

-- name: AddManifestEntry :exec
INSERT INTO manifest_entries (state_id, path, blob_hash, executable)
VALUES (?, ?, ?, ?);

-- name: ListManifestEntries :many
SELECT path, blob_hash, executable
FROM manifest_entries
WHERE state_id = ?
ORDER BY path ASC;

-- name: SetHead :exec
UPDATE head SET state_id = ? WHERE id = 0;

-- name: AppendHeadHistory :exec
INSERT INTO head_history (state_id, moved_at)
VALUES (?, ?);

-- name: ListStatCache :many
SELECT path, size, mtime_ns, inode, blob_hash, recorded_at
FROM stat_cache;

-- name: UpsertStatCacheEntry :exec
INSERT INTO stat_cache (path, size, mtime_ns, inode, blob_hash, recorded_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (path) DO UPDATE SET
    size        = excluded.size,
    mtime_ns    = excluded.mtime_ns,
    inode       = excluded.inode,
    blob_hash   = excluded.blob_hash,
    recorded_at = excluded.recorded_at;

-- name: DeleteStatCacheEntry :exec
DELETE FROM stat_cache WHERE path = ?;
