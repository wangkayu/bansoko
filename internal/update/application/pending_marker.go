package application

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const PendingMarkerFileName = "pending.json"

// PendingMarker is the complete identity handed to the bootstrap. Keep this
// contract deliberately small: bootstrap owns every post-marker effect.
type PendingMarker struct {
	OperationID       string `json:"operation_id"`
	ReleaseID         string `json:"release_id"`
	ManifestDigest    string `json:"manifest_digest"`
	ReleaseSequence   int64  `json:"release_sequence"`
	PreviousReleaseID string `json:"previous_release_id"`
}

// WritePendingMarker durably publishes the handoff using an exclusive rename.
// A pre-existing marker is never replaced, even when it names the same update.
func WritePendingMarker(updatesRoot string, marker PendingMarker) error {
	if strings.TrimSpace(updatesRoot) == "" || strings.TrimSpace(marker.OperationID) == "" ||
		strings.TrimSpace(marker.ReleaseID) == "" || strings.TrimSpace(marker.ManifestDigest) == "" || marker.ReleaseSequence < 1 {
		return errors.New("pending marker identity is incomplete")
	}
	if err := os.MkdirAll(updatesRoot, 0o700); err != nil {
		return fmt.Errorf("create updates root: %w", err)
	}
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encode pending marker: %w", err)
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(updatesRoot, ".pending-*.tmp")
	if err != nil {
		return fmt.Errorf("create pending marker temporary file: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("protect pending marker: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write pending marker: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync pending marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close pending marker: %w", err)
	}
	destination := filepath.Join(updatesRoot, PendingMarkerFileName)
	if err := renameNoReplace(tempPath, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("pending marker already exists: %w", err)
		}
		return fmt.Errorf("publish pending marker: %w", err)
	}
	dir, err := os.Open(updatesRoot)
	if err != nil {
		return fmt.Errorf("open updates root for sync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync updates root after pending marker: %w", err)
	}
	return nil
}
func ReadPendingMarker(path string) (PendingMarker, error) {
	data, err := readFileBounded(path, 4096)
	if err != nil {
		return PendingMarker{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return PendingMarker{}, err
	}
	want := map[string]bool{"operation_id": true, "release_id": true, "manifest_digest": true, "release_sequence": true, "previous_release_id": true}
	if len(fields) != len(want) {
		return PendingMarker{}, errors.New("pending marker fields do not match the handoff contract")
	}
	for key := range fields {
		if !want[key] {
			return PendingMarker{}, errors.New("pending marker fields do not match the handoff contract")
		}
	}
	var marker PendingMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return PendingMarker{}, err
	}
	if !validMarkerID(marker.OperationID) || !validMarkerID(marker.ReleaseID) || marker.ManifestDigest == "" || len(marker.ManifestDigest) != 64 || marker.ReleaseSequence < 1 || (marker.PreviousReleaseID != "" && !validMarkerID(marker.PreviousReleaseID)) {
		return PendingMarker{}, errors.New("pending marker identity is invalid")
	}
	if _, err := hex.DecodeString(marker.ManifestDigest); err != nil {
		return PendingMarker{}, errors.New("pending marker digest is invalid")
	}
	return marker, nil
}

func validMarkerID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return value != "." && value != ".."
}
