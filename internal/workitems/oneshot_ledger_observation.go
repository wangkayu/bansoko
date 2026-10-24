package workitems

import (
	"encoding/json"
	"fmt"
)

// ValidateOneShotLedgerObservation reuses the admission invariant for a ledger
// read from the caller's consistent snapshot. It takes no locks or write ports.
func ValidateOneShotLedgerObservation(count int32, raw json.RawMessage) error {
	var entries []oneShotLedgerEntry
	if len(raw) == 0 || string(raw) == "null" {
		return ErrInvalidOneShotLedger
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("%w: decode observed ledger", ErrInvalidOneShotLedger)
	}
	return validateOneShotLedger(count, entries)
}
