-- name: GetStateParent :one
SELECT parent_id FROM states WHERE id = ?;

-- name: MostRecentChild :one
SELECT s.id
FROM states s
JOIN head_history h ON h.state_id = s.id
WHERE s.parent_id = ?
GROUP BY s.id
ORDER BY MAX(h.seq) DESC
LIMIT 1;
