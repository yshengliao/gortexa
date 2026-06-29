-- name: GetResource :one
SELECT * FROM resources WHERE id = $1;

-- name: ListResources :many
SELECT * FROM resources
WHERE (sqlc.arg(owner)::text = '' OR owner = sqlc.arg(owner)::text)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- name: CreateResource :one
INSERT INTO resources (id, name, owner, status)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateResource :one
UPDATE resources
SET name = $2, owner = $3, status = $4
WHERE id = $1
RETURNING *;

-- name: DeleteResource :exec
DELETE FROM resources WHERE id = $1;
