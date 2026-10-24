package domain

import (
	"encoding/json"
	"fmt"
)

// SectionDownloadTimeouts is the Global-only download timeouts setting: how long a download may make no progress (stall) and how
// long an accepted download may wait in total. It is edited only in the
// settings page; there is no per-anime override, no draft field and no
// business-snapshot entry. Acceptance reads it once and freezes both values
// into the acquisition input limits (stall_seconds, download_wait_seconds and
// the derived sync.max_polls); runtime never reads it again, so a later edit
// only affects downloads accepted after the save.
const SectionDownloadTimeouts = "download_timeouts"

// DownloadTimeoutsSchemaVersion pins the stored download_timeouts shape.
const DownloadTimeoutsSchemaVersion = "download-timeouts-v1"

// Bounds of the two values, in seconds. The stall bound is the former
// deployment env range; the wait bound is the acquisition limits schema
// (LimitsV1.download_wait_seconds, at most 30 days). stall <= wait is the
// download_tasks budget shape.
const (
	MinDownloadStallSeconds     int64 = 10 * 60
	MaxDownloadStallSeconds     int64 = 7 * 86400
	DefaultDownloadStallSeconds int64 = 6 * 3600
	MinDownloadWaitSeconds      int64 = 3600
	MaxDownloadWaitSeconds      int64 = 30 * 86400
	DefaultDownloadWaitSeconds  int64 = 7 * 86400
)

// GlobalOnlySectionOrder lists the generic Global sections that have no
// per-anime override; the Global settings list shows them after the
// rules-owner sections.
var GlobalOnlySectionOrder = []string{SectionDownloadTimeouts, SectionMetadataRefresh}

// DownloadTimeouts is the complete download_timeouts value.
type DownloadTimeouts struct {
	StallSeconds        int64 `json:"stall_seconds"`
	DownloadWaitSeconds int64 `json:"download_wait_seconds"`
}

// DefaultDownloadTimeouts is the code default: 6 hours without progress, 7
// days in total.
func DefaultDownloadTimeouts() DownloadTimeouts {
	return DownloadTimeouts{StallSeconds: DefaultDownloadStallSeconds, DownloadWaitSeconds: DefaultDownloadWaitSeconds}
}

// DownloadTimeoutsError names the offending field of an invalid value.
type DownloadTimeoutsError struct {
	Field   string
	Message string
}

func (e *DownloadTimeoutsError) Error() string { return e.Message }

// Validate enforces both ranges and stall <= wait.
func (t DownloadTimeouts) Validate() error {
	if t.StallSeconds < MinDownloadStallSeconds || t.StallSeconds > MaxDownloadStallSeconds {
		return &DownloadTimeoutsError{Field: "stall_seconds", Message: fmt.Sprintf("download_timeouts.stall_seconds must be between %d and %d", MinDownloadStallSeconds, MaxDownloadStallSeconds)}
	}
	if t.DownloadWaitSeconds < MinDownloadWaitSeconds || t.DownloadWaitSeconds > MaxDownloadWaitSeconds {
		return &DownloadTimeoutsError{Field: "download_wait_seconds", Message: fmt.Sprintf("download_timeouts.download_wait_seconds must be between %d and %d", MinDownloadWaitSeconds, MaxDownloadWaitSeconds)}
	}
	if t.StallSeconds > t.DownloadWaitSeconds {
		return &DownloadTimeoutsError{Field: "stall_seconds", Message: "download_timeouts.stall_seconds must not exceed download_wait_seconds"}
	}
	return nil
}

// ValidateDownloadTimeoutsJSON enforces the typed shape of a stored or
// submitted download_timeouts value.
func ValidateDownloadTimeoutsJSON(raw json.RawMessage) error {
	_, err := DecodeDownloadTimeouts(raw)
	return err
}

// DecodeDownloadTimeouts decodes and validates one stored value. Both fields
// are required.
func DecodeDownloadTimeouts(raw json.RawMessage) (DownloadTimeouts, error) {
	var probe struct {
		StallSeconds        *int64 `json:"stall_seconds"`
		DownloadWaitSeconds *int64 `json:"download_wait_seconds"`
	}
	if err := decodeStrictJSON(raw, &probe); err != nil || probe.StallSeconds == nil || probe.DownloadWaitSeconds == nil {
		return DownloadTimeouts{}, &DownloadTimeoutsError{Field: "", Message: "download_timeouts value must carry integer stall_seconds and download_wait_seconds"}
	}
	value := DownloadTimeouts{StallSeconds: *probe.StallSeconds, DownloadWaitSeconds: *probe.DownloadWaitSeconds}
	if err := value.Validate(); err != nil {
		return DownloadTimeouts{}, err
	}
	return value, nil
}
