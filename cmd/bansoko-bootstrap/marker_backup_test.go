package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMarkerBackupReplayReusesOnlyAVerifiedArtifact pins the backup window's
// replay rule: a published dump whose sidecar digest agrees is reused, and
// anything else - absent, empty, truncated, tampered or missing a sidecar -
// forces a fresh dump. Reuse is the only reason a crash between "backup durable"
// and "journal written" converges instead of dumping, dying and repeating.
func TestMarkerBackupReplayReusesOnlyAVerifiedArtifact(t *testing.T) {
	const backupID = "embedded-op-marker-replay"
	for _, tc := range []struct {
		name        string
		dump        []byte
		sidecar     string
		writeSide   bool
		wantFresh   bool
		description string
	}{
		{name: "no artifact yet", wantFresh: true, description: "first attempt must dump"},
		{name: "verified artifact", dump: []byte("published dump"), writeSide: true, wantFresh: false, description: "replay reuses the verified artifact"},
		{name: "empty dump", dump: []byte{}, writeSide: true, wantFresh: true, description: "an empty dump is never reused"},
		{name: "digest mismatch", dump: []byte("published dump"), sidecar: "deadbeef", writeSide: true, wantFresh: true, description: "a tampered dump is never reused"},
		{name: "sidecar absent", dump: []byte("published dump"), wantFresh: true, description: "an unproven dump is never reused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			backupDir := filepath.Join(root, backupsDirName)
			if err := os.MkdirAll(backupDir, 0o700); err != nil {
				t.Fatal(err)
			}
			dumpPath := backupsDumpPath(root, backupID)
			if tc.dump != nil {
				if err := os.WriteFile(dumpPath, tc.dump, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.writeSide {
				body := tc.sidecar
				if body == "" {
					digest, err := hashFileSHA256(dumpPath)
					if err != nil {
						t.Fatal(err)
					}
					body = digest
				}
				if err := os.WriteFile(dumpPath+".sha256", []byte(body+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := markerBackupNeedsDump(root, backupID); got != tc.wantFresh {
				t.Fatalf("markerBackupNeedsDump = %v, want %v (%s)", got, tc.wantFresh, tc.description)
			}
		})
	}
}
