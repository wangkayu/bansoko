// Package apperrors defines the typed application-boundary errors the HTTP
// transport maps onto the generated ErrorResponse shape. Each condition has a
// stable code and carries no secrets, SQL, or private paths.
package apperrors

import "fmt"

// ImageCompatibilityError reports that an embedded payload requires a newer
// immutable base image. It is intentionally typed so API clients can switch to
// the image update channel without exposing internal implementation details.
type ImageCompatibilityError struct {
	Required string
	Current  string
}

func (e *ImageCompatibilityError) Error() string {
	return fmt.Sprintf("embedded update requires base image compatibility %q; current base is %q", e.Required, e.Current)
}

// BootstrapTooOldError reports that the bootstrap wrapper baked into the
// running base image cannot consume the candidate: the signed bootstrap floor
// is newer than the version that launched this process, or the process reports
// no usable version at all. The remedy is always an operator image switch, so
// the condition is typed instead of surfacing as a generic failure.
type BootstrapTooOldError struct {
	Required string
	Current  string
}

func (e *BootstrapTooOldError) Error() string {
	return fmt.Sprintf("embedded update requires bootstrap %q; running bootstrap is %q", e.Required, e.Current)
}

// NotFound reports an unknown owner or resource.
type NotFound struct {
	Resource string
	ID       string
}

func (e *NotFound) Error() string { return fmt.Sprintf("%s %s not found", e.Resource, e.ID) }

// VersionConflict reports a failed expected-version CAS and carries the
// current owner version for the typed 409 response.
type VersionConflict struct {
	Owner          string
	CurrentVersion int64
}

func (e *VersionConflict) Error() string {
	return fmt.Sprintf("%s owner version conflict (current %d)", e.Owner, e.CurrentVersion)
}

// Business reports a typed conflict/validation/availability condition from
// the contract error matrix (for example IDEMPOTENCY_KEY_REUSED or
// METADATA_REQUIRED). Status is one of 409, 422, or 503. Details optionally
// name what the condition is about (counts, for example).
type Business struct {
	Status  int
	Code    string
	Message string
	Details []FieldViolation
}

func (e *Business) Error() string { return e.Code + ": " + e.Message }

// BadRequest reports a malformed request (missing headers, bad input shape).
type BadRequest struct {
	Code    string
	Message string
}

func (e *BadRequest) Error() string { return e.Code + ": " + e.Message }

// FieldViolation is a transport-neutral validation detail. HTTP transports
// project it onto their generated error response shape.
type FieldViolation struct {
	Field   *string
	Code    string
	Message string
	// Start/End optionally locate a template source diagnostic (byte offsets
	// into the template named by Field).
	Start *int
	End   *int
	// Count optionally carries how many of the thing the detail names exist
	// (the blockers of a refused change, for example).
	Count *int64
}

// Validation carries typed 422 field violations. The first violation
// determines the stable response code.
type Validation struct {
	Code    string
	Details []FieldViolation
}

func (e *Validation) Error() string { return e.Code }
