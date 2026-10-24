package txscope

import (
	"database/sql"
	"testing"
)

func TestWrapPreservesTransaction(t *testing.T) {
	tx := &sql.Tx{}
	if got := Wrap(tx).SQLTx(); got != tx {
		t.Fatalf("unwrapped transaction = %p, want %p", got, tx)
	}
}

func TestWrapNilProducesInvalidScope(t *testing.T) {
	if got := Wrap(nil).SQLTx(); got != nil {
		t.Fatalf("unwrapped nil transaction = %p, want nil", got)
	}
	if got := (Tx{}).SQLTx(); got != nil {
		t.Fatalf("zero scope transaction = %p, want nil", got)
	}
}
