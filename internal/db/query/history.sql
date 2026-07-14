-- name: DeleteState :exec
DELETE FROM states WHERE id = ?;

-- name: SetStateParentNull :exec
UPDATE states SET parent_id = NULL WHERE id = ?;

-- name: SetStateParent :exec
UPDATE states SET parent_id = ? WHERE id = ?;
