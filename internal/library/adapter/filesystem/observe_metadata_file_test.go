package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/library/domain"
)

// The recovery confirm compares the candidate's fingerprint with what its
// observer reports through domain.MetadataFingerprintsMatch, so the observer
// injected in production MUST answer in metadata form. Injecting the
// content-hashing ObserveFile instead produced a hex-64 digest that
// ParseMetadataFingerprint rejects, which made EVERY production confirm answer
// target_changed. These tests pin the real adapter's contract.

// ObserveMetadataFile answers a metadata fingerprint that the recovery
// candidate machinery can parse — and never opens the file.
func TestObserveMetadataFileAnswersParseableMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "episode.mkv")
	content := []byte("not a real video, but 64 bytes of stable content for the stat test")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	observation, err := OSFileObserver{}.ObserveMetadataFile(context.Background(), path)
	if err != nil {
		t.Fatalf("observe metadata: %v", err)
	}
	if !domain.IsMetadataFingerprint(observation.Fingerprint) {
		t.Fatalf("fingerprint = %q, want a metadata fingerprint (the confirm parses it)", observation.Fingerprint)
	}
	facts, ok := domain.ParseMetadataFingerprint(observation.Fingerprint)
	if !ok {
		t.Fatalf("ParseMetadataFingerprint(%q) failed: every confirm would refuse target_changed", observation.Fingerprint)
	}
	if facts.SizeBytes != int64(len(content)) {
		t.Fatalf("size = %d, want %d", facts.SizeBytes, len(content))
	}
	if observation.SizeBytes != int64(len(content)) {
		t.Fatalf("observation size = %d, want %d", observation.SizeBytes, len(content))
	}
}

// The candidate the scan freezes and the observation the confirm makes must
// compare equal, which is the whole point of the metadata mode: a confirm on an
// unchanged file succeeds, and the same comparison still rejects a rewritten
// file of the same size.
func TestObserveMetadataFileMatchesFrozenCandidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "episode.mkv")
	original := []byte("original episode bytes, stable identity for the confirm")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	frozen, err := OSFileObserver{}.ObserveMetadataFile(context.Background(), path)
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}

	// An unchanged file matches: the confirm proceeds.
	confirm, err := OSFileObserver{}.ObserveMetadataFile(context.Background(), path)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if !domain.MetadataFingerprintsMatch(confirm.Fingerprint, frozen.Fingerprint) {
		t.Fatalf("unchanged file did not match: frozen=%q confirm=%q",
			frozen.Fingerprint, confirm.Fingerprint)
	}

	// A rewritten file of the SAME size is a different inode with the same
	// byte count: the metadata comparison must reject it rather than treat
	// size equality as identity.
	replacement := filepath.Join(dir, "replacement.mkv")
	if err := os.WriteFile(replacement, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	rewritten, err := OSFileObserver{}.ObserveMetadataFile(context.Background(), path)
	if err != nil {
		t.Fatalf("rewritten: %v", err)
	}
	if rewritten.SizeBytes != frozen.SizeBytes {
		t.Fatalf("fixture is wrong: sizes differ (%d vs %d)", rewritten.SizeBytes, frozen.SizeBytes)
	}
	if domain.MetadataFingerprintsMatch(rewritten.Fingerprint, frozen.Fingerprint) {
		t.Fatalf("a replaced file of the same size matched: %q", rewritten.Fingerprint)
	}
}

// A missing target is an error, not a zero observation: the confirm turns it
// into target_missing instead of comparing a fabricated identity.
func TestObserveMetadataFileFailsOnMissingTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.mkv")
	if _, err := (OSFileObserver{}).ObserveMetadataFile(context.Background(), path); err == nil {
		t.Fatal("observing a missing file succeeded")
	}
}

// The hashing observer must NOT be what the recovery ports receive. This pins
// the distinction the production wiring relies on: ObserveFile answers a
// content digest that ParseMetadataFingerprint rejects.
func TestObserveFileIsNotMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "episode.mkv")
	if err := os.WriteFile(path, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	hashed, err := OSFileObserver{}.ObserveFile(context.Background(), path)
	if err != nil {
		t.Fatalf("observe file: %v", err)
	}
	if domain.IsMetadataFingerprint(hashed.Fingerprint) {
		t.Fatalf("ObserveFile answered a metadata fingerprint (%q): the two modes are not distinct",
			hashed.Fingerprint)
	}
	if _, ok := domain.ParseMetadataFingerprint(hashed.Fingerprint); ok {
		t.Fatalf("ParseMetadataFingerprint accepted the content digest %q", hashed.Fingerprint)
	}
	if domain.MetadataFingerprintsMatch(hashed.Fingerprint, hashed.Fingerprint) {
		t.Fatal("a content digest matched as metadata: the confirm would compare apples to oranges")
	}
}
