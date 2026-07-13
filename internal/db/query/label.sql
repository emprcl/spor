-- name: GetStateIDByLabel :one
SELECT id FROM states WHERE label = ?;

-- name: ListLabels :many
SELECT label, id, created_at
FROM states
WHERE label IS NOT NULL
ORDER BY label ASC;

-- name: SetStateLabel :exec
UPDATE states SET label = ? WHERE id = ?;
