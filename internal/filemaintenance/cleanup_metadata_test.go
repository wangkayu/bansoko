package filemaintenance

import (
	"testing"

	"bansoko/internal/library/domain"
)

func TestCleanupMetadataObservationComparisonRules(t *testing.T) {
	expected := domain.FileObservation{
		Fingerprint: "meta:size=64;dev=2;ino=12", SizeBytes: 64,
		Identity: domain.FileIdentity{Device: 2, Inode: 12},
	}
	remounted := expected
	remounted.Fingerprint = "meta:size=64;dev=9;ino=12"
	remounted.Identity.Device = 9
	if !sameFrozenCleanupObservation(remounted, expected) {
		t.Fatal("device-only metadata change must retain path identity")
	}
	changedInode := remounted
	changedInode.Fingerprint = "meta:size=64;dev=9;ino=13"
	changedInode.Identity.Inode = 13
	if sameFrozenCleanupObservation(changedInode, expected) {
		t.Fatal("inode change must fail closed")
	}
	changedSize := remounted
	changedSize.SizeBytes = 65
	changedSize.Fingerprint = "meta:size=65;dev=9;ino=12"
	if sameFrozenCleanupObservation(changedSize, expected) {
		t.Fatal("size change must fail closed")
	}
}
