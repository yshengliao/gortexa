-- name: GetResource :one
SELECT * FROM resources WHERE id = $1;

-- name: ListResources :many
-- page_token is the last id of the previous page, empty for the first page;
-- request page_size+1 rows to learn whether a next page exists.
SELECT * FROM resources
WHERE (sqlc.arg(owner)::text = '' OR owner = sqlc.arg(owner)::text)
  AND (sqlc.arg(page_token)::text = '' OR id > sqlc.arg(page_token)::text)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- name: CreateResource :one
INSERT INTO resources (id, name, owner, status)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateResource :one
-- A NULL argument leaves its column untouched (proto partial-update semantics).
UPDATE resources
SET name   = COALESCE(sqlc.narg(name), name),
    owner  = COALESCE(sqlc.narg(owner), owner),
    status = COALESCE(sqlc.narg(status), status)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteResource :exec
DELETE FROM resources WHERE id = $1;
