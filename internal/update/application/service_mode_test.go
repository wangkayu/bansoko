package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/runtime/buildinfo"
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
	service, err := NewService(Deps{UpdateMode: "embedded", UpdatesRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if service == nil {
		t.Fatal("service is nil")
	}
}

func TestAdvancesRunningRelease(t *testing.T) {
	running := buildinfo.Order{Series: buildinfo.CISeries, Lane: "test", ReleaseSequence: 42}
	for _, tc := range []struct {
		name, series, lane string
		sequence           int64
		want               bool
	}{
		{"lower", buildinfo.CISeries, "test", 41, false},
		{"equal", buildinfo.CISeries, "test", 42, false},
		{"higher", buildinfo.CISeries, "test", 43, true},
		{"older cross lane", buildinfo.CISeries, "stable", 41, false},
		{"equal cross lane", buildinfo.CISeries, "stable", 42, false},
		{"newer cross lane", buildinfo.CISeries, "stable", 43, true},
		{"cross series", "gh:other/bansoko:v1", "test", 41, true},
		{"legacy nightly", "", "nightly", 41, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := hardeningdomain.ReleaseManifest{Series: tc.series, Channel: hardeningdomain.ReleaseChannel(tc.lane), ReleaseSequence: tc.sequence}
			if got := advancesRunningRelease(running, candidate); got != tc.want {
				t.Fatalf("advancesRunningRelease = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestImageCompatibilityAtLeast(t *testing.T) {
	for _, tc := range []struct {
		current, required string
		want              bool
	}{{"base-v2", "base-v10", false}, {"base-v10", "base-v10", true}, {"base-v10", "base-v2", true}} {
		got, err := ImageCompatibilityAtLeast(tc.current, tc.required)
		if err != nil || got != tc.want {
			t.Fatalf("imageCompatibilityAtLeast(%q,%q)=%v,%v want %v", tc.current, tc.required, got, err, tc.want)
		}
	}
	if _, err := ImageCompatibilityAtLeast("base-vx", "base-v1"); err == nil {
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
