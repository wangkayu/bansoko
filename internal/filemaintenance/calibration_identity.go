package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"bansoko/internal/library/domain"
)

// placementObservationConflictTx decides whether a Placement-owned scan
// observation contradicts the placement's frozen identity
// (fingerprint_conflict).
//
// File verification is path-level: the scan records a metadata fingerprint (size plus device/inode,
// domain.MetadataFingerprint) and never reads content, so "changed" means the
// observed size differs from the size recorded for the placement, or the
// observed inode differs from an inode the placement recorded. A placement
// with no recorded size is treated as unchanged. A same-size, same-inode
// content swap is not detected (accepted cost).
//
// An observation that still carries a content digest (a generation scanned
// before the metadata scan) keeps the digest comparison, so replaying the
// calibration of such a generation is deterministic.
func placementObservationConflictTx(ctx context.Context, tx *sql.Tx, placementID, observed string) (bool, error) {
	record, err := readPlacementIdentityTx(ctx, tx, placementID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if facts, ok := domain.ParseMetadataFingerprint(observed); ok {
		recorded, known := record.metadataFacts()
		if !known {
			return false, nil
		}
		return !domain.MetadataFactsMatch(facts, recorded), nil
	}
	if strings.TrimSpace(record.sourceFingerprint) == "" {
		return false, nil
	}
	return fingerprintValue(record.sourceFingerprint) != observed, nil
}

// placementIdentityRecord is the frozen identity a placement carries: its
// source/target fingerprints and, for a file-operation placement, the sizes
// the publication recorded.
type placementIdentityRecord struct {
	sourceFingerprint string
	targetFingerprint string
	operationSize     sql.NullInt64
}

func readPlacementIdentityTx(ctx context.Context, tx *sql.Tx, placementID string) (placementIdentityRecord, error) {
	var record placementIdentityRecord
	err := tx.QueryRowContext(ctx, `
SELECT p.source_fingerprint, p.target_fingerprint,
       (SELECT COALESCE(o.observed_target_size_bytes, o.expected_size_bytes)
          FROM file_operations o
         WHERE p.source_evidence_type = 'file_operation' AND o.id = p.source_owner_ref)
FROM placements p
WHERE p.id = $1`, placementID).Scan(&record.sourceFingerprint, &record.targetFingerprint, &record.operationSize)
	return record, err
}

// metadataFacts returns the path-level facts recorded for the placement, in
// this order: a metadata fingerprint it was admitted with (target, then
// source; raw or wrapped in the typed {"algorithm":"metadata"} envelope a
// scan-alignment recovery writes), the size_bytes of its typed target
// fingerprint (the published size of a file-operation placement), then the
// file operation's observed or expected target size. known is false when none
// of them records a size.
//
// The envelope must be unwrapped here: reading only its size_bytes would drop
// the inode a recovery froze, and a same-size replacement of a recovered file
// would calibrate as unchanged instead of fingerprint_conflict.
func (r placementIdentityRecord) metadataFacts() (domain.MetadataFacts, bool) {
	for _, value := range []string{r.targetFingerprint, r.sourceFingerprint} {
		raw, ok := metadataFingerprintValue(value)
		if !ok {
			continue
		}
		if facts, ok := domain.ParseMetadataFingerprint(raw); ok {
			return facts, true
		}
	}
	for _, value := range []string{r.targetFingerprint, r.sourceFingerprint} {
		if size, ok := typedFingerprintSize(value); ok {
			return domain.MetadataFacts{SizeBytes: size}, true
		}
	}
	if r.operationSize.Valid && r.operationSize.Int64 >= 0 {
		return domain.MetadataFacts{SizeBytes: r.operationSize.Int64}, true
	}
	return domain.MetadataFacts{}, false
}

// typedFingerprintSize reads size_bytes from a typed fingerprint envelope
// ({"algorithm":...,"value":...,"size_bytes":n}).
func typedFingerprintSize(stored string) (int64, bool) {
	trimmed := strings.TrimSpace(stored)
	if !strings.HasPrefix(trimmed, "{") {
		return 0, false
	}
	var envelope struct {
		SizeBytes *int64 `json:"size_bytes"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil || envelope.SizeBytes == nil || *envelope.SizeBytes < 0 {
		return 0, false
	}
	return *envelope.SizeBytes, true
}
