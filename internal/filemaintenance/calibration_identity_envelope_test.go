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
