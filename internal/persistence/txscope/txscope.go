// Package txscope defines the opaque transaction scope shared by application ports.
package txscope

import "database/sql"

// Tx carries the caller-owned SQL transaction across an application boundary
// without exposing database/sql in that boundary's port signature.
type Tx struct {
	tx *sql.Tx
}

// Wrap creates an opaque scope for the caller-owned transaction. A nil
// transaction produces an invalid scope; consumers must reject it rather than
// dereference it.
func Wrap(tx *sql.Tx) Tx {
	return Tx{tx: tx}
}

// SQLTx unwraps the caller-owned transaction for the owning persistence helper.
// It returns nil for a zero or nil-wrapped scope.
func (scope Tx) SQLTx() *sql.Tx {
	return scope.tx
}
