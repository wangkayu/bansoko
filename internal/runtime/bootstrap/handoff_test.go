package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHandoffUnifiedBootstrapExecsVerifiedTarget(t *testing.T) {
	root := t.TempDir()
	releaseID := "release-handoff"
	releaseDir := writeReleaseTree(t, root, releaseID)
	target := releaseServerBin(releaseDir)
	anchor := filepath.Join(t.TempDir(), "anchor")
	if err := os.WriteFile(anchor, []byte("anchor"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldCurrent, oldExec := currentExecutable, execProcess
	t.Cleanup(func() { currentExecutable, execProcess = oldCurrent, oldExec })
	currentExecutable = func() (string, error) { return anchor, nil }

	marker := pendingMarker{
		OperationID: "handoff-op", ReleaseID: releaseID,
		ManifestDigest: strings.Repeat("a", 64), ReleaseSequence: 3,
	}
	markerBytes, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, pendingMarkerName), markerBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		plan plan
	}{
		{name: "overlay", plan: plan{kind: "server", mode: modeOverlay, releaseID: releaseID}},
		{name: "consume", plan: plan{kind: "server", mode: modeConsume, handoffReleaseID: releaseID}},
		{name: "pending marker", plan: plan{kind: "server", mode: modePendingMarker}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var gotPath string
			var gotArgs []string
			execProcess = func(path string, args []string, _ []string) error {
				gotPath, gotArgs = path, append([]string(nil), args...)
				return nil
			}
			code, handedOff := handoffUnifiedBootstrap(test.plan, root)
			if !handedOff || code != 0 {
				t.Fatalf("handoff = (%d, %v)", code, handedOff)
			}
			if gotPath != target || !reflect.DeepEqual(gotArgs, []string{target, "bootstrap"}) {
				t.Fatalf("exec = %q %v, want %q bootstrap", gotPath, gotArgs, target)
			}
		})
	}
}

func TestHandoffUnifiedBootstrapStopsAtTargetExecutable(t *testing.T) {
	root := t.TempDir()
	releaseDir := writeReleaseTree(t, root, "release-current")
	target := releaseServerBin(releaseDir)
	oldCurrent, oldExec := currentExecutable, execProcess
	t.Cleanup(func() { currentExecutable, execProcess = oldCurrent, oldExec })
	currentExecutable = func() (string, error) { return target, nil }
	execProcess = func(string, []string, []string) error {
		t.Fatal("the target bootstrap handed off to itself")
		return nil
	}
	code, handedOff := handoffUnifiedBootstrap(plan{
		kind: "server", mode: modeOverlay, releaseID: "release-current",
	}, root)
	if handedOff || code != 0 {
		t.Fatalf("same-file handoff = (%d, %v), want normal target startup", code, handedOff)
	}
}

func TestIsCurrentExecutableRecognizesHardlinks(t *testing.T) {
	dir := t.TempDir()
	anchor := filepath.Join(dir, "anchor")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(anchor, []byte("same inode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(anchor, target); err != nil {
		t.Fatal(err)
	}
	oldCurrent := currentExecutable
	t.Cleanup(func() { currentExecutable = oldCurrent })
	currentExecutable = func() (string, error) { return anchor, nil }
	same, err := isCurrentExecutable(target)
	if err != nil || !same {
		t.Fatalf("SameFile = %v, %v; want true", same, err)
	}
}
