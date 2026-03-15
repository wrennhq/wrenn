-- name: InsertUser :one
INSERT INTO users (id, email, password_hash)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: InsertUserOAuth :one
INSERT INTO users (id, email)
VALUES ($1, $2)
RETURNING *;
