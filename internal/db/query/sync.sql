-- name: GetSync :one
SELECT project_id, remote_url, synced_gen, synced_at FROM sync WHERE id = 0;

-- name: SetRemote :exec
UPDATE sync SET project_id = ?, remote_url = ? WHERE id = 0;

-- name: ClearRemote :exec
UPDATE sync
SET project_id = NULL, remote_url = NULL, synced_gen = 0, synced_at = NULL
WHERE id = 0;

-- name: SetSyncedGen :exec
UPDATE sync SET synced_gen = ?, synced_at = ? WHERE id = 0;

-- ListStatesForSync carries manifest_hash, which ListStates omits; a sync needs it
-- to name each state's manifest blob on the wire.
-- name: ListStatesForSync :many
SELECT id, created_at, parent_id, manifest_hash, label
FROM states
ORDER BY created_at ASC, id ASC;

-- name: ListSyncBase :many
SELECT state_id, parent_id, created_at, manifest_hash, label
FROM sync_base;

-- name: ClearSyncBase :exec
DELETE FROM sync_base;

-- name: AddSyncBaseEntry :exec
INSERT INTO sync_base (state_id, parent_id, created_at, manifest_hash, label)
VALUES (?, ?, ?, ?, ?);
