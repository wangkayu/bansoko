package application

import (
	"context"
	"path/filepath"
	"testing"

	"bansoko/internal/update/journal"
)

func TestActiveAdmissionStatusDoesNotReadPreviousJournal(t *testing.T) {
	root := t.TempDir()
	store, err := journal.Open(filepath.Join(root, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Create(journal.Record{OperationID: "previous-op", Channel: "embedded", TargetReleaseID: "old-release", TargetReleaseSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Advance(record.OperationID, record.Version, journal.StepFailed, "fixture", "previous attempt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	service := &Service{updatesRoot: root, chainRunning: true, lastStatus: Status{OperationID: "new-op", Channel: "embedded", Step: "accepted", TargetReleaseID: "new-release"}}
	status, found, err := service.GetUpdateStatus(context.Background())
	if err != nil || !found || status.OperationID != "new-op" || status.Step != "accepted" {
		t.Fatalf("active status = %+v found=%v err=%v", status, found, err)
	}
	// A fresh process without an admission still reads the durable history.
	restarted := &Service{updatesRoot: root}
	status, found, err = restarted.GetUpdateStatus(context.Background())
	if err != nil || !found || status.OperationID != "previous-op" || status.Step != "failed" {
		t.Fatalf("restart status = %+v found=%v err=%v", status, found, err)
	}
}
