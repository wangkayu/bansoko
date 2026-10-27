package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/workitems"
)

func TestPrepareFailureMapsHashOccupiedToExplicitTerminalReason(t *testing.T) {
	code, retry := prepareFailure(fmt.Errorf("prepare: %w", qbittorrent.ErrHashOccupied), domain.ProviderErrorTerminal)
	if code != ReasonDownloaderHashOccupied || retry {
		t.Fatalf("occupied prepare failure code=%q retry=%v", code, retry)
	}
	code, retry = prepareFailure(errors.New("qB task lookup failed with HTTP 503"), domain.ProviderErrorRetryable)
	if code != "qb_submit_prepare_failed" || !retry {
		t.Fatalf("ordinary prepare failure code=%q retry=%v", code, retry)
	}
}

func TestSubmitRefusedAsOccupiedRequiresAnsweredRefusal(t *testing.T) {
	occupied := fmt.Errorf("%w (add refused with HTTP 409)", qbittorrent.ErrHashOccupied)
	answered := qbittorrent.SubmissionResult{ResponseReceived: true, StatusCode: http.StatusConflict}
	if !submitRefusedAsOccupied(answered, occupied) {
		t.Fatal("an answered refusal attributed to another task must close as occupied")
	}
	if submitRefusedAsOccupied(qbittorrent.SubmissionResult{}, occupied) {
		t.Fatal("an unanswered add never closes as occupied")
	}
	if submitRefusedAsOccupied(answered, errors.New("qB task submission failed with HTTP 409")) {
		t.Fatal("an unattributed refusal stays unknown")
	}
	if submitRefusedAsOccupied(qbittorrent.SubmissionResult{ResponseReceived: true, Accepted: true}, nil) {
		t.Fatal("an accepted add is not occupied")
	}
}

func assertOccupiedFailure(t *testing.T, taskID string) {
	t.Helper()
	db := ownerTestDB(t)
	var state, code, category string
	if err := db.QueryRowContext(context.Background(), `SELECT state,failure_code,download_failure_category(failure_code) FROM download_tasks WHERE id=$1`, taskID).Scan(&state, &code, &category); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || code != ReasonDownloaderHashOccupied || category != "occupied" {
		t.Fatalf("occupied task state=%q code=%q category=%q", state, code, category)
	}
}

func TestQBSubmitHashOccupiedFailsWithoutAdd(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-occupied", "queued", "null")
	provider := &fakeProvider{hashInfo: []string{`[{"hash":"other","tags":"bansoko-qb:other"}]`}}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-occupied", workitems.KindQBSubmit)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != ReasonDownloaderHashOccupied || provider.fileSubmitCalls != 0 {
		t.Fatalf("occupied submit outcome=%+v adds=%d", outcome, provider.fileSubmitCalls)
	}
	assertOccupiedFailure(t, "task-qb-occupied")
}

func TestQBSubmitRefusedAddOnOccupiedHashFailsWithoutRecovery(t *testing.T) {
	db := openDownloadDB(t)
	seedDownloadTask(t, db, "task-qb-occupied-race", "queued", "null")
	provider := &fakeProvider{
		hashInfo:  []string{"[]", `[{"hash":"other","tags":"user-added"}]`},
		addStatus: http.StatusConflict, addBody: "Torrent is already present",
	}
	config := &fakeRuntimeConfig{config: qbittorrent.RuntimeConfig{ConnectionID: "qb-main", SaveRoot: "/downloads"}}
	service, store := newDownloadService(t, db, provider, config)
	claim := createClaimedQBWorkItem(t, db, store, "task-qb-occupied-race", workitems.KindQBSubmit)
	outcome, err := service.Handle(context.Background(), claim)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != worker.OutcomeCommitted || outcome.OwnerResult.ReasonCode != ReasonDownloaderHashOccupied || provider.fileSubmitCalls != 1 {
		t.Fatalf("refused add outcome=%+v adds=%d", outcome, provider.fileSubmitCalls)
	}
	var effect string
	var retrySafe bool
	if err := ownerTestDB(t).QueryRowContext(context.Background(), `SELECT submit_effect_class,submit_retry_safe FROM download_tasks WHERE id='task-qb-occupied-race'`).Scan(&effect, &retrySafe); err != nil {
		t.Fatal(err)
	}
	if effect != "no_external_effect" || !retrySafe {
		t.Fatalf("refused add receipt effect=%q retry_safe=%v", effect, retrySafe)
	}
	assertOccupiedFailure(t, "task-qb-occupied-race")
}
