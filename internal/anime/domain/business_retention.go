package domain

import (
	"bytes"
	"encoding/json"
	"fmt"

	"bansoko/internal/qualityprofile"
)

type businessCleanupPolicy struct {
	Version      string `json:"version"`
	Effect       string `json:"effect"`
	GraceSeconds int64  `json:"grace_seconds"`
}

func strictBusinessJSON(raw json.RawMessage, destination interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return nil
}
func validateBusinessRetention(value RetentionValue) error {
	if _, err := qualityprofile.Parse(value.QualityProfile); err != nil {
		return fmt.Errorf("quality profile: %w", err)
	}
	var cleanup businessCleanupPolicy
	if err := strictBusinessJSON(value.CleanupPolicy, &cleanup); err != nil {
		return fmt.Errorf("cleanup policy: %w", err)
	}
	if cleanup.Version != "cleanup-v1" || cleanup.GraceSeconds < 0 || (cleanup.Effect != "quarantine" && cleanup.Effect != "unlink" && cleanup.Effect != "purge") {
		return fmt.Errorf("invalid complete cleanup policy")
	}
	return nil
}
