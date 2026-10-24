package application

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewServiceRequiresEmbeddedMode(t *testing.T) {
	for _, mode := range []string{"supervised", "manual", "", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			_, err := NewService(Deps{UpdateMode: mode})
			if err == nil || !strings.Contains(err.Error(), "BANSOKO_UPDATE_MODE=embedded") {
				t.Fatalf("constructor error = %v, want embedded mode refusal", err)
			}
		})
	}
}

func TestNewServiceDoesNotRequireGateFenceJournalOrBackup(t *testing.T) {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Deps{UpdateMode: "embedded", PublicKey: private.Public().(ed25519.PublicKey), UpdatesRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if service == nil {
		t.Fatal("service is nil")
	}
}

func TestImageCompatibilityAtLeast(t *testing.T) {
	for _, tc := range []struct {
		current, required string
		want              bool
	}{{"base-v2", "base-v10", false}, {"base-v10", "base-v10", true}, {"base-v10", "base-v2", true}} {
		got, err := imageCompatibilityAtLeast(tc.current, tc.required)
		if err != nil || got != tc.want {
			t.Fatalf("imageCompatibilityAtLeast(%q,%q)=%v,%v want %v", tc.current, tc.required, got, err, tc.want)
		}
	}
	if _, err := imageCompatibilityAtLeast("base-vx", "base-v1"); err == nil {
		t.Fatal("invalid compatibility label accepted")
	}
}

func TestPendingMarkerRefusesOverwriteAndHasExactIdentityFields(t *testing.T) {
	root := t.TempDir()
	marker := PendingMarker{OperationID: "embedded-op-1", ReleaseID: "release-1", ManifestDigest: strings.Repeat("a", 64), ReleaseSequence: 7, PreviousReleaseID: "release-0"}
	if err := WritePendingMarker(root, marker); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, PendingMarkerFileName))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	want := []string{"operation_id", "release_id", "manifest_digest", "release_sequence", "previous_release_id"}
	if len(fields) != len(want) {
		t.Fatalf("marker has %d fields, want exactly %d: %s", len(fields), len(want), data)
	}
	for _, key := range want {
		if _, ok := fields[key]; !ok {
			t.Fatalf("marker missing %q: %s", key, data)
		}
	}
	other := marker
	other.ReleaseID = "release-2"
	if err = WritePendingMarker(root, other); err == nil {
		t.Fatal("second marker creation overwrote existing pending marker")
	}
	got, err := ReadPendingMarker(filepath.Join(root, PendingMarkerFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got != marker {
		t.Fatalf("read marker = %+v, want %+v", got, marker)
	}
}
