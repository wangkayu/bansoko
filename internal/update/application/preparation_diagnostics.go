package application

import (
	"context"
	"errors"
	"net/http"
	"os"
	"syscall"

	"bansoko/internal/observability/outbound"
)

type preparationClass string

const (
	prepSourceInvalid    preparationClass = "source_invalid"
	prepManifestInvalid  preparationClass = "manifest_invalid"
	prepRedirectRejected preparationClass = "redirect_rejected"
	prepTransfer         preparationClass = "payload_transfer"
	prepPayloadSize      preparationClass = "payload_size"
	prepPayloadVerify    preparationClass = "payload_verification"
	prepPayloadPublish   preparationClass = "payload_publication"
	prepStorage          preparationClass = "storage_io"
	prepArchive          preparationClass = "archive_extraction"
	prepReleaseVerify    preparationClass = "release_verification"
	prepReleaseConflict  preparationClass = "release_conflict"
	prepReleasePublish   preparationClass = "release_publication"
	prepPointerInvalid   preparationClass = "current_release_pointer_invalid"
	prepHandoff          preparationClass = "handoff_marker"
	prepHandoffExists    preparationClass = "handoff_already_pending"
)

type preparationError struct {
	class preparationClass
	cause error
}

func (e *preparationError) Error() string { return e.cause.Error() }
func (e *preparationError) Unwrap() error { return e.cause }

func classifyPreparation(err error, fallback preparationClass) preparationClass {
	var detail *preparationError
	switch {
	case errors.As(err, &detail):
		return detail.class
	case errors.Is(err, os.ErrPermission):
		return "storage_permission"
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return "storage_capacity"
	default:
		return fallback
	}
}

func labelPreparationError(err error, fallback preparationClass) error {
	if err == nil {
		return nil
	}
	var detail *preparationError
	if errors.As(err, &detail) {
		return err
	}
	return &preparationError{class: classifyPreparation(err, fallback), cause: err}
}

// Only typed classifications are used in logs. Transport errors may carry
// full request URLs and local errors may carry paths or archive entry names.
func classifyPayloadTransport(err error) preparationClass {
	switch {
	case errors.Is(err, ErrUpdateRedirect):
		return prepRedirectRejected
	case errors.Is(err, ErrUpdateSourceURL):
		return prepSourceInvalid
	case errors.Is(err, context.Canceled):
		return "payload_request_canceled"
	}
	if class := outbound.Classify(err); class != outbound.ClassUnknown {
		return preparationClass(class)
	}
	return prepTransfer
}

func classifyPayloadHTTP(status int) preparationClass {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return "payload_access_denied"
	case status == http.StatusNotFound, status == http.StatusGone:
		return "payload_unavailable"
	case status == http.StatusTooManyRequests:
		return "payload_rate_limited"
	case status >= 500 && status < 600:
		return "payload_upstream_failure"
	default:
		return "payload_http_status"
	}
}
