//go:build !linux && !darwin

package filesystem

import "time"

// sweepAbandonedCleanupNamespace has no effect where the private namespace is
// not supported: no capture namespace is ever created there.
func sweepAbandonedCleanupNamespace(string, string, time.Time) (bool, error) {
	return false, nil
}
