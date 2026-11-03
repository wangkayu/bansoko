package domain

import "testing"

func TestMetadataFingerprintRoundTrip(t *testing.T) {
	value := MetadataFingerprint(4_200_000_000, FileIdentity{Device: 42, Inode: 7})
	if value != "meta:size=4200000000;dev=42;ino=7" {
		t.Fatalf("MetadataFingerprint = %q", value)
	}
	facts, ok := ParseMetadataFingerprint(value)
	if !ok || facts != (MetadataFacts{SizeBytes: 4_200_000_000, Device: 42, Inode: 7}) {
		t.Fatalf("parse = %+v %v", facts, ok)
	}
	if bare := MetadataFingerprint(10, FileIdentity{}); bare != "meta:size=10" {
		t.Fatalf("identity-less fingerprint = %q", bare)
	}
}

func TestParseMetadataFingerprintRejectsOtherValues(t *testing.T) {
	for _, value := range []string{
		"", "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		`{"algorithm":"sha256","value":"ab"}`, "meta:", "meta:size=-1", "meta:size=1;dev=2",
		"meta:size=1;ino=2", "meta:size=1;size=2", "meta:size=1;color=red", "meta:dev=1;ino=2",
		// A directory carries no size; a file requires one; both reject a
		// duplicated dir marker and a half identity.
		"meta:dir;size=1", "meta:size=1;dir", "meta:dir;dir", "meta:dir;dev=1", "meta:dir;ino=2",
	} {
		if _, ok := ParseMetadataFingerprint(value); ok {
			t.Fatalf("ParseMetadataFingerprint(%q) accepted", value)
		}
	}
}

func TestDirectoryMetadataFingerprintRoundTrip(t *testing.T) {
	value := DirectoryMetadataFingerprint(FileIdentity{Device: 42, Inode: 7})
	if value != "meta:dir;dev=42;ino=7" {
		t.Fatalf("DirectoryMetadataFingerprint = %q", value)
	}
	facts, ok := ParseMetadataFingerprint(value)
	if !ok || facts != (MetadataFacts{Device: 42, Inode: 7, Directory: true}) {
		t.Fatalf("parse = %+v %v", facts, ok)
	}
	bare := DirectoryMetadataFingerprint(FileIdentity{})
	if bare != "meta:dir" {
		t.Fatalf("identity-less directory fingerprint = %q", bare)
	}
	if facts, ok := ParseMetadataFingerprint(bare); !ok || !facts.Directory || facts.SizeBytes != 0 || facts.Inode != 0 {
		t.Fatalf("bare parse = %+v %v", facts, ok)
	}
}

func TestMetadataFingerprintsMatch(t *testing.T) {
	cases := []struct {
		left, right string
		want        bool
	}{
		{"meta:size=10;dev=1;ino=7", "meta:size=10;dev=1;ino=7", true},
		// A remount may change the client device number; the inode decides.
		{"meta:size=10;dev=1;ino=7", "meta:size=10;dev=2;ino=7", true},
		{"meta:size=10;dev=1;ino=7", "meta:size=10;dev=1;ino=8", false},
		{"meta:size=10;dev=1;ino=7", "meta:size=11;dev=1;ino=7", false},
		// Without a recorded identity the size alone decides.
		{"meta:size=10", "meta:size=10;dev=1;ino=7", true},
		{"meta:size=10", "abc", false},
		// A directory never matches a file; directories compare by inode only.
		{"meta:dir;dev=1;ino=7", "meta:size=0;dev=1;ino=7", false},
		{"meta:size=0;dev=1;ino=7", "meta:dir;dev=1;ino=7", false},
		{"meta:dir;dev=1;ino=7", "meta:dir;dev=2;ino=7", true},
		{"meta:dir;dev=1;ino=7", "meta:dir;dev=1;ino=8", false},
		{"meta:dir", "meta:dir;dev=1;ino=7", true},
		{"meta:dir", "meta:size=0", false},
	}
	for _, tc := range cases {
		if got := MetadataFingerprintsMatch(tc.left, tc.right); got != tc.want {
			t.Fatalf("MetadataFingerprintsMatch(%q, %q) = %v, want %v", tc.left, tc.right, got, tc.want)
		}
	}
}
