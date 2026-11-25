package application

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"bansoko/internal/apperrors"
	"bansoko/internal/hardening/domain"
)

func TestLinuxChannelTarget(t *testing.T) {
	for _, tc := range []struct {
		arch, manifestArch, prefix string
	}{
		{"amd64", "amd64", ""},
		{"arm64", "arm64", "/arm64"},
		{"arm64", "amd64", "/arm64"},
	} {
		t.Run(tc.arch+"/"+tc.manifestArch, func(t *testing.T) {
			manifest := embeddedManifestFixture(domain.HashArtifact([]byte("payload")))
			manifest.EmbeddedUpdate.Architecture = tc.manifestArch
			manifest.Artifacts[0].Architecture = tc.manifestArch
			digest, err := manifest.CanonicalDigest()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			root := "/update-channel" + tc.prefix
			manifestPath := root + "/v1/release-manifest.json"
			var document []byte
			server := newRecordingServer(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case root + "/channel-v1.json":
					_, _ = w.Write(document)
				case manifestPath:
					_, _ = w.Write(raw)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
			t.Cleanup(server.Close)
			document = channelDocumentFixture(t, server.URL()+manifestPath, digest, manifest.ReleaseID, nil)
			s, err := NewService(Deps{UpdateMode: "embedded", UpdatesRoot: t.TempDir(), Lane: LaneStable, ChannelBase: server.URL(), TargetPlatform: "linux", TargetArchitecture: tc.arch})
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := s.resolve(t.Context(), nil, "")
			if tc.arch == tc.manifestArch {
				if err != nil {
					t.Fatal(err)
				}
				payloadURL, err := resolved.source.PayloadURL()
				if err != nil || payloadURL != server.URL()+root+"/v1/"+PayloadFileName {
					t.Fatalf("resolve %s package: %v", tc.arch, err)
				}
			} else {
				var failure *apperrors.Business
				if !errors.As(err, &failure) || failure.Code != "UPDATE_PLATFORM_MISMATCH" {
					t.Fatalf("expected platform refusal, got %v", err)
				}
			}
			if server.count() != 2 {
				t.Fatalf("expected only channel and manifest fetch, got %+v", server.snapshot())
			}
		})
	}
}

func TestMissingBackupToolsRefusesAdmissionBeforeIO(t *testing.T) {
	digest, manifest := buildManifestBody(t)
	rig := newChannelCounterexampleRig(t, manifest, nil)
	rig.setDocumentBody(channelDocumentFixture(t, rig.recording.URL()+channelCounterexampleManifestPath, digest, "release-test-1004", nil))
	updates := filepath.Join(t.TempDir(), "updates")
	var gate sync.Mutex
	checks := 0
	s, err := NewService(Deps{
		UpdateMode: "embedded", UpdatesRoot: updates, Lane: LaneStable, ChannelBase: rig.recording.URL(), RestartGate: &gate,
		CheckBackupTools: func() error { checks++; return errors.New("missing pg_restore") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.resolve(t.Context(), nil, ""); err != nil || checks != 0 {
		t.Fatalf("discovery should work without backup tools, got %v (%d checks)", err, checks)
	}
	requests := rig.recording.count()
	_, _, err = s.Admit(t.Context(), "", "key")
	var failure *apperrors.Business
	if !errors.As(err, &failure) || failure.Status != 503 || failure.Code != "UPDATE_BACKUP_TOOLS_UNAVAILABLE" {
		t.Fatalf("admission error = %v", err)
	}
	if checks != 1 || rig.recording.count() != requests || s.Running() {
		t.Fatal("refused admission did not release state or performed network I/O")
	}
	if _, err := os.Stat(updates); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused admission touched staging: %v", err)
	}
	if !gate.TryLock() {
		t.Fatal("refused admission held the restart gate")
	}
	gate.Unlock()
}
