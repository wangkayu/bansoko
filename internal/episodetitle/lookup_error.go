package episodetitle

import (
	"fmt"
	"time"
)

type LookupErrorKind string

const (
	LookupUnconfigured        LookupErrorKind = "unconfigured"
	LookupCredentialsRejected LookupErrorKind = "credentials_rejected"
	LookupRateLimited         LookupErrorKind = "rate_limited"
	LookupUnreachable         LookupErrorKind = "unreachable"
	LookupInvalidResponse     LookupErrorKind = "invalid_response"
)

// LookupError is a provider failure, never a definite answer about a title.
// Legacy callers can still recognize every kind as ErrTransient.
type LookupError struct {
	Source     Source
	Kind       LookupErrorKind
	RetryAfter time.Duration
	Cause      error
}

func (e *LookupError) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("%v: %s episode lookup: %s", ErrTransient, e.Source, e.Kind)
	}
	return fmt.Sprintf("%v: %s episode lookup: %s: %v", ErrTransient, e.Source, e.Kind, e.Cause)
}

func (e *LookupError) Unwrap() error        { return e.Cause }
func (e *LookupError) Is(target error) bool { return target == ErrTransient }

// ConfigurationError preserves infrastructure failures for batch callers
// while the strict, legacy lookup still exposes ErrTransient.
type ConfigurationError struct{ Cause error }

func (e *ConfigurationError) Error() string { return e.Cause.Error() }
func (e *ConfigurationError) Unwrap() error { return e.Cause }
