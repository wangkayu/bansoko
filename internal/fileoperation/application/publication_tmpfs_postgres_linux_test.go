//go:build linux

package application

import (
	"encoding/json"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// Path-level verification treats every filesystem alike: a real
// typed copy whose source and library live on tmpfs completes through the
// worker entry with size-only (PathObjectV1) evidence and opens no
// diagnostic episode.
func TestTypedPublicationCompletesOnTmpfs(t *testing.T) {
	shm, err := os.MkdirTemp("/dev/shm", "publication-path-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(shm) })
	var fs unix.Statfs_t
	if err := unix.Statfs(shm, &fs); err != nil || int64(fs.Type) != unix.TMPFS_MAGIC {
		t.Fatalf("/dev/shm must be tmpfs: %v", err)
	}
	t.Setenv("TMPDIR", shm)
	t.Setenv("GOTMPDIR", shm) // t.TempDir prefers GOTMPDIR since Go 1.26
	x := newTypedRuntimeFixture(t, nil, true)
	wireRealDiagnostics(t, x.db, x.s)
	for run := 0; run < 2; run++ { // video, then the handed-off subtitle
		if handled, err := x.runFileWorker(t); !handled || err != nil {
			t.Fatalf("run %d: handled=%v err=%v", run, handled, err)
		}
	}
	for _, op := range []string{x.opID, x.subtile} {
		if got := x.state(t, op); got.opState != ownerStateCompleted {
			t.Fatalf("operation %s did not complete on tmpfs: %+v", op, got)
		}
		var source []byte
		if err := x.db.QueryRow(`SELECT publication_frozen_objects->'source' FROM file_operations WHERE id=$1`, op).Scan(&source); err != nil {
			t.Fatal(err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(source, &object); err != nil {
			t.Fatal(err)
		}
		if _, content := object["content"]; content || len(object) != 5 || string(object["file_type"]) != `"regular"` {
			t.Fatalf("publication source evidence is not PathObjectV1: %s", source)
		}
	}
	var episodes int
	if err := x.db.QueryRow(`SELECT count(*) FROM diagnostic_episodes WHERE succeeded_after_at IS NULL`).Scan(&episodes); err != nil {
		t.Fatal(err)
	}
	if episodes != 0 {
		t.Fatalf("path-level publication on tmpfs opened %d diagnostic episodes", episodes)
	}
}
