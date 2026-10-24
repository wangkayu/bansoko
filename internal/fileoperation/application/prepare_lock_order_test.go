package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Artifact is retained during prepare. Existing file WorkItem/owner locks must
// never wait in that transaction: file execution owns those before Artifact.
func TestPrepareExistingOwnerLocksNeverWaitBehindArtifact(t *testing.T) {
	raw, err := os.ReadFile("prepare.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`WHERE idempotency_key = $1 FOR UPDATE NOWAIT`,
		`WHERE id = $1 FOR UPDATE NOWAIT`,
		`workitems.LockLatestFileExecuteTx(ctx, tx, workitems.OwnerFileOperation, operation.id`,
		`workitems.LockFileOperationInputsTx(ctx, tx, workitems.OwnerFileOperation, operationID`,
		"FOR UPDATE NOWAIT`, artifactFileID",
	} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("prepare may wait on earlier publication lock: %s", required)
		}
	}
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "migrations", "001_baseline.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"latest_file_execute", "file_operation_inputs"} {
		marker := "CREATE FUNCTION work_item_lock_" + name + "("
		_, body, ok := strings.Cut(string(migration), marker)
		if !ok {
			t.Fatalf("missing fixed lock function %s", name)
		}
		body, _, ok = strings.Cut(body, "$$;")
		if !ok {
			t.Fatalf("missing function boundary %s", name)
		}
		if !strings.Contains(body, "FOR UPDATE NOWAIT;") {
			t.Fatalf("%s may wait behind Artifact", name)
		}
	}
}
