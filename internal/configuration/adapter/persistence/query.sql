-- Configuration secret persistence is static and allowlisted; the migration chain is authoritative.

-- name: InsertSecret :exec
INSERT INTO secret_values(ref, purpose, nonce, ciphertext)
VALUES ($1, $2, $3, $4);

-- name: DeleteSecret :exec
DELETE FROM secret_values
WHERE ref = $1;

-- name: GetSecret :one
SELECT ref, purpose, nonce, ciphertext
FROM secret_values
WHERE ref = $1;
