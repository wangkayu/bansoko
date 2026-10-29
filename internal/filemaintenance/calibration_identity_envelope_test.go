package filemaintenance

import (
	"strings"
	"testing"

	"bansoko/internal/library/domain"
)

// A recovered placement's target fingerprint is the typed metadata envelope
// the recovery writer produces (recoveryTargetFingerprintEnvelope). Calibration
// must read the inode inside it, not just its size_bytes: a same-size
// replacement with a different inode is a fingerprint_conflict.
func TestPlacementIdentityRecordUnwrapsRecoveryEnvelope(t *testing.T) {
	observed := domain.MetadataFingerprint(4096, domain.FileIdentity{Device: 2, Inode: 11})
	envelope, err := recoveryTargetFingerprintEnvelope(observed, 4096)
	if err != nil {
		t.Fatal(err)
	}
	record := placementIdentityRecord{
		// The recovery keeps the placement's content-digest source evidence.
		sourceFingerprint: `{"algorithm":"sha256","value":"` + strings.Repeat("ab", 32) + `"}`,
		targetFingerprint: envelope,
	}
	facts, known := record.metadataFacts()
	if !known {
		t.Fatal("recovered placement reported no recorded facts")
	}
	if facts.SizeBytes != 4096 || facts.Inode != 11 {
		t.Fatalf("recorded facts = %+v, want size 4096 and inode 11 from the envelope", facts)
	}
	replaced, _ := domain.ParseMetadataFingerprint(domain.MetadataFingerprint(4096, domain.FileIdentity{Device: 2, Inode: 99}))
	if domain.MetadataFactsMatch(replaced, facts) {
		t.Fatal("same-size different-inode replacement matched the recovered placement")
	}
	remounted, _ := domain.ParseMetadataFingerprint(domain.MetadataFingerprint(4096, domain.FileIdentity{Device: 9, Inode: 11}))
	if !domain.MetadataFactsMatch(remounted, facts) {
		t.Fatal("a device-only remount of the recovered file read as a change")
	}
}

// A copy-mode file operation publishes a new inode: the placement records the
// published target's identity and, as its source, the inspected download
// file's. A scan that finds the placement through its source-provenance
// locator observes that source object, which is the placement's file; any
// other same-size inode is a conflict, and a size-only source never widens
// the match.
func TestPlacementIdentityRecordAcceptsTheCopySourceObject(t *testing.T) {
	envelope := func(size int64, inode uint64) string {
		value := domain.MetadataFingerprint(size, domain.FileIdentity{Device: 3, Inode: inode})
		raw, err := recoveryTargetFingerprintEnvelope(value, size)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	observe := func(size int64, inode uint64) domain.MetadataFacts {
		facts, ok := domain.ParseMetadataFingerprint(domain.MetadataFingerprint(size, domain.FileIdentity{Device: 3, Inode: inode}))
		if !ok {
			t.Fatal("observation did not parse")
		}
		return facts
	}
	copied := placementIdentityRecord{sourceFingerprint: envelope(4096, 5), targetFingerprint: envelope(4096, 9)}
	if copied.observationConflicts(observe(4096, 9)) {
		t.Fatal("the published target conflicted with its placement")
	}
	if copied.observationConflicts(observe(4096, 5)) {
		t.Fatal("the source object the copy was published from conflicted with its placement")
	}
	if !copied.observationConflicts(observe(4096, 7)) {
		t.Fatal("a same-size file with another inode matched the placement")
	}
	if !copied.observationConflicts(observe(4097, 5)) {
		t.Fatal("a resized source object matched the placement")
	}
	sizeOnly := placementIdentityRecord{sourceFingerprint: `{"algorithm":"metadata","value":"meta:size=4096","size_bytes":4096}`, targetFingerprint: envelope(4096, 9)}
	if !sizeOnly.observationConflicts(observe(4096, 7)) {
		t.Fatal("a size-only source widened the match to any same-size inode")
	}
}
