package filesystem

import (
	"testing"

	"bansoko/internal/library/domain"
)

func TestSameMutationMetadataObservationIgnoresDeviceOnly(t *testing.T) {
	expected := domain.FileObservation{
		Fingerprint: "meta:size=64;dev=2;ino=12", SizeBytes: 64,
		Identity: domain.FileIdentity{Device: 2, Inode: 12, GenerationNanos: 99}, ChangeNanos: 100,
	}
	remounted := expected
	remounted.Fingerprint = "meta:size=64;dev=9;ino=12"
	remounted.Identity.Device = 9
	if !sameMutationFileObservation(remounted, expected) {
		t.Fatal("metadata mutation identity rejected a device-only remount")
	}
	changedInode := remounted
	changedInode.Fingerprint = "meta:size=64;dev=9;ino=13"
	changedInode.Identity.Inode = 13
	if sameMutationFileObservation(changedInode, expected) {
		t.Fatal("metadata mutation identity accepted a different inode")
	}
	changedSize := remounted
	changedSize.Fingerprint = "meta:size=65;dev=9;ino=12"
	changedSize.SizeBytes = 65
	if sameMutationFileObservation(changedSize, expected) {
		t.Fatal("metadata mutation identity accepted a different size")
	}
}

func TestSameMutationSHAObservationStillRequiresDevice(t *testing.T) {
	expected := domain.FileObservation{
		Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 64,
		Identity: domain.FileIdentity{Device: 2, Inode: 12, GenerationNanos: 99}, ChangeNanos: 100,
	}
	changedDevice := expected
	changedDevice.Identity.Device = 9
	if sameMutationFileObservation(changedDevice, expected) {
		t.Fatal("SHA-256 mutation identity unexpectedly ignored device")
	}
}
