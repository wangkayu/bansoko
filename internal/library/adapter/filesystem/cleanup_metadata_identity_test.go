package filesystem

import (
	"os"
	"path/filepath"
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

// A filesystem without birth time (statx btime absent: FUSE/mergerfs, Unraid
// shfs, some NFS clients) reports GenerationNanos == 0, and rename updates the
// moved inode's ctime. The post-quarantine identity check must accept the SAME
// object on the same device after a real rename, and still refuse a
// different inode. The observations come from the real metadata observer over
// a real rename; only the generation is cleared to model a btime-less mount.
func TestSameMutationMetadataObservationSurvivesRenameWithoutGeneration(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "episode.mkv")
	if err := os.WriteFile(source, []byte("metadata-identity"), 0o644); err != nil {
		t.Fatal(err)
	}
	expected, err := observeFileMetadata(source)
	if err != nil {
		t.Fatalf("observe source: %v", err)
	}
	quarantine := filepath.Join(dir, "quarantine.mkv")
	if err := os.Rename(source, quarantine); err != nil {
		t.Fatal(err)
	}
	observed, err := observeFileMetadata(quarantine)
	if err != nil {
		t.Fatalf("observe quarantine: %v", err)
	}
	expected.Identity.GenerationNanos = 0
	observed.Identity.GenerationNanos = 0
	if observed.ChangeNanos == expected.ChangeNanos {
		// Coarse ctime granularity may hide the rename's ctime bump; model it
		// so the case asserts what a btime-less mount really reports.
		observed.ChangeNanos = expected.ChangeNanos + 1
	}
	if !sameMutationFileObservation(observed, expected) {
		t.Fatal("metadata mutation identity rejected the same inode after a rename changed its ctime")
	}
	withoutHandles := func(o domain.FileObservation) domain.FileObservation {
		o.HandleEvidence = nil
		return o
	}
	if !sameMutationFileObservation(withoutHandles(observed), withoutHandles(expected)) {
		t.Fatal("dev+inode identity without handle evidence rejected a rename")
	}

	// A genuinely replaced object: a different inode at the same path, same size.
	other := filepath.Join(dir, "other.mkv")
	if err := os.WriteFile(other, []byte("metadata-identity"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, quarantine); err != nil {
		t.Fatal(err)
	}
	replaced, err := observeFileMetadata(quarantine)
	if err != nil {
		t.Fatalf("observe replaced quarantine: %v", err)
	}
	if replaced.Identity.Inode == expected.Identity.Inode {
		t.Skip("filesystem reused the inode; replacement case not observable here")
	}
	replaced.Identity.GenerationNanos = 0
	if sameMutationFileObservation(replaced, expected) {
		t.Fatal("metadata mutation identity accepted a replaced object with a different inode")
	}
	if sameMutationFileObservation(withoutHandles(replaced), withoutHandles(expected)) {
		t.Fatal("dev+inode identity accepted a replaced object with a different inode")
	}
}

func TestSameMutationMetadataObservationEvidenceOrder(t *testing.T) {
	base := domain.FileObservation{
		Fingerprint: "meta:size=64;dev=2;ino=12", SizeBytes: 64,
		Identity: domain.FileIdentity{Device: 2, Inode: 12}, ChangeNanos: 100,
		HandleEvidence: &domain.HandleEvidence{MountID: 7, Type: 1, Opaque: "aa"},
	}
	renamed := base
	renamed.ChangeNanos = 200
	if !sameMutationFileObservation(renamed, base) {
		t.Fatal("matching handle evidence rejected a renamed object")
	}
	otherHandle := renamed
	otherHandle.HandleEvidence = &domain.HandleEvidence{MountID: 7, Type: 1, Opaque: "bb"}
	if sameMutationFileObservation(otherHandle, base) {
		t.Fatal("a different file handle was accepted on inode equality")
	}
	remountedNoCtime := base
	remountedNoCtime.HandleEvidence = nil
	remountedNoCtime.Fingerprint = "meta:size=64;dev=9;ino=12"
	remountedNoCtime.Identity.Device = 9
	remountedNoCtime.ChangeNanos = 200
	baseNoHandle := base
	baseNoHandle.HandleEvidence = nil
	if sameMutationFileObservation(remountedNoCtime, baseNoHandle) {
		t.Fatal("a device change without generation or change-marker evidence was accepted")
	}
	remountedNoCtime.ChangeNanos = 100
	if !sameMutationFileObservation(remountedNoCtime, baseNoHandle) {
		t.Fatal("a device-only remount with an unchanged change marker was rejected")
	}
	missingIdentity := baseNoHandle
	missingIdentity.Identity = domain.FileIdentity{}
	if sameMutationFileObservation(missingIdentity, baseNoHandle) {
		t.Fatal("an observation without identity was accepted")
	}
}
