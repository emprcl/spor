-- name: ListStates :many
SELECT id, created_at, parent_id, label
FROM states
ORDER BY created_at ASC, id ASC;
