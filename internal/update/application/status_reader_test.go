package application

import (
	"context"
	"path/filepath"
	"testing"

	embeddedupdate "bansoko/internal/update/journal"
)

// A manual deployment wires the status reader instead of the embedded update
// service: /update/status must keep reporting durable history while the manual
// mode refuses every online effect.
func TestStatusReaderReportsDurableHistoryWithoutAnUpdateSource(t *testing.T) {
	root := t.TempDir()
	journal, err := embeddedupdate.Open(filepath.Join(root, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := journal.Create(embeddedupdate.Record{OperationID: "embedded-reader-1", Channel: "embedded", TargetReleaseID: "release-7", TargetReleaseSequence: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	reader := NewStatusReader(root)
	status, found, err := reader.GetUpdateStatus(context.Background())
	if err != nil || !found {
		t.Fatalf("status = %+v, found=%v, err=%v", status, found, err)
	}
	if status.Step != "accepted" || status.OperationID != record.OperationID || status.TargetReleaseID != "release-7" {
		t.Fatalf("status = %+v, want the durable journal record", status)
	}
	if _, _, err := reader.AdmitUpdate(context.Background(), AdmitInput{TargetReleaseID: "release-8", ManifestDigest: "sha256:" + "a"}, "manual-key"); err == nil {
		t.Fatal("manual status reader admitted an online update")
	}
	if _, err := reader.CheckUpdate(context.Background()); err == nil {
		t.Fatal("manual status reader checked for online updates")
	}
}

func TestStatusReaderReportsNothingWithoutARecord(t *testing.T) {
	status, found, err := NewStatusReader(t.TempDir()).GetUpdateStatus(context.Background())
	if err != nil || found || status.OperationID != "" {
		t.Fatalf("status = %+v, found=%v, err=%v; want no record and no error", status, found, err)
	}
}
