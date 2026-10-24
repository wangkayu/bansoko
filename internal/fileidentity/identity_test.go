package fileidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bansoko/internal/library/domain"
)

func TestMatchesRequiresDeviceAndInode(t *testing.T) {
	identity := domain.FileIdentity{Device: 1, Inode: 2}
	if Matches(identity, domain.FileIdentity{}) {
		t.Fatal("missing identity matched")
	}
	if Matches(domain.FileIdentity{Device: 1}, identity) {
		t.Fatal("partial identity matched")
	}
}
func TestHandleEvidenceSameFileAndRecreatedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	left, err := HandleEvidenceFromFile(first)
	if errors.Is(err, ErrEvidenceUnavailable) {
		if right, rightErr := HandleEvidenceFromFile(first); !errors.Is(rightErr, ErrEvidenceUnavailable) || right != nil {
			t.Fatalf("unavailable capability was not reported consistently: evidence=%v err=%v", right, rightErr)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	right, err := HandleEvidenceFromFile(second)
	second.Close()
	if err != nil {
		t.Fatal(err)
	}
	if left == nil {
		t.Fatal("handle evidence unexpectedly returned nil without an unavailable error")
	}
	if !MatchesHandleEvidence(left, right) {
		t.Fatal("same file handles did not match")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	recreated, err := HandleEvidenceFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if recreated == nil {
		t.Fatal("recreated handle evidence unexpectedly unavailable")
	}
	if MatchesHandleEvidence(left, recreated) {
		t.Fatal("unlink/recreate reused file handle evidence")
	}
}
func TestMatchesHandleEvidenceRequiresCompleteEvidence(t *testing.T) {
	valid := func(opaque string) *domain.HandleEvidence {
		return &domain.HandleEvidence{MountID: 1, Type: 2, Opaque: opaque}
	}
	if !MatchesHandleEvidence(nil, valid("x")) {
		t.Fatal("missing handle should remain inconclusive")
	}
	if MatchesHandleEvidence(valid(""), valid("")) {
		t.Fatal("empty opaque handles must not match")
	}
	if MatchesHandleEvidence(valid("x"), &domain.HandleEvidence{MountID: 0, Type: 2, Opaque: "x"}) {
		t.Fatal("invalid mount id must not match")
	}
	if !MatchesHandleEvidence(valid("x"), valid("x")) {
		t.Fatal("valid equal handles did not match")
	}
}
func TestMatchesTreatsGenerationAsOptionalUntilBothArePresent(t *testing.T) {
	base := domain.FileIdentity{Device: 1, Inode: 2}
	if !Matches(base, domain.FileIdentity{Device: 1, Inode: 2, GenerationNanos: 11}) {
		t.Fatal("legacy zero generation should remain comparable")
	}
	if Matches(
		domain.FileIdentity{Device: 1, Inode: 2, GenerationNanos: 11},
		domain.FileIdentity{Device: 1, Inode: 2, GenerationNanos: 12},
	) {
		t.Fatal("different generations matched")
	}
	if !Matches(
		domain.FileIdentity{Device: 1, Inode: 2, GenerationNanos: 11},
		domain.FileIdentity{Device: 1, Inode: 2, GenerationNanos: 11},
	) {
		t.Fatal("equal generations did not match")
	}
}
