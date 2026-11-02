package filesystem

import (
	"testing"

	"bansoko/internal/library/domain"
)

func TestSamePlacementMutationObservationUsesOneObjectDefinition(t *testing.T) {
	target := domain.FileObservation{
		Fingerprint: "same", SizeBytes: 4,
		Identity: domain.FileIdentity{Device: 1, Inode: 2}, ChangeNanos: 42,
	}
	source := target
	source.Identity = domain.FileIdentity{}
	if sameFrozenFileObservation(target, source) {
		t.Fatal("test setup did not remove frozen identity evidence")
	}
	if !sameMutationFileObservation(target, source) {
		t.Fatal("test setup did not preserve matching mutation evidence")
	}
	if !samePlacementMutationObservation(target, source) {
		t.Fatal("identity-less source with matching mutation evidence was classified as a different object")
	}

	source.ChangeNanos++
	if samePlacementMutationObservation(target, source) {
		t.Fatal("different mutation evidence was accepted without frozen identity")
	}

	source = target
	source.ChangeNanos++
	if !samePlacementMutationObservation(target, source) {
		t.Fatal("matching frozen identity was rejected merely because the change marker differed")
	}
	source.Identity.Inode++
	if samePlacementMutationObservation(target, source) {
		t.Fatal("different frozen identity was accepted")
	}
}
