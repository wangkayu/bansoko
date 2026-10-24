package filemaintenance

import (
	"database/sql"

	filemaintenancepolicy "bansoko/internal/filemaintenance/policy"
)

// PolicyRow is kept as a source-compatible alias while the policy owner lives
// in its dedicated subpackage.
type PolicyRow = filemaintenancepolicy.PolicyRow

// EffectivePolicy is kept as a source-compatible alias while the policy owner
// lives in its dedicated subpackage.
type EffectivePolicy = filemaintenancepolicy.EffectivePolicy

// PolicyService is the dedicated policy owner, exposed here as a compatibility
// facade for existing FileMaintenance callers.
type PolicyService = filemaintenancepolicy.PolicyService

// ErrPolicyConflict is the policy owner's explicit CAS conflict.
var ErrPolicyConflict = filemaintenancepolicy.ErrPolicyConflict

// GlobalScope is the global policy scope key.
const GlobalScope = filemaintenancepolicy.GlobalScope

// NewPolicyService is the compatibility constructor for the dedicated policy
// owner. New code should import filemaintenance/policy directly.
func NewPolicyService(db *sql.DB) *PolicyService {
	return filemaintenancepolicy.NewPolicyService(db)
}
