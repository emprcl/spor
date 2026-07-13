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
