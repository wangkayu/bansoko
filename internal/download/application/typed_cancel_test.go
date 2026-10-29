package application

import (
	"context"
	"fmt"
	"testing"
)

const cancelTestFrozenInput = `{
    "release_stable_item_key":"stable",
    "source_locator_display":"https://feed.example.test/rss",
    "torrent_input_id":"torrent-input",
    "torrent_payload_digest":"sha256:payload",
    "torrent_filename":"episode.torrent",
    "torrent_parser_version":"torrent-bencode-v1",
    "torrent_version":"v1"%s
}`

type recordingCancelRemover struct {
	calls   []string
	removal CancelRemoval
}

func (r *recordingCancelRemover) RemoveCancelledDownload(_ context.Context, acquisitionID string, _, _ []byte) CancelRemoval {
	r.calls = append(r.calls, acquisitionID)
	return r.removal
}

func cancelTestTask(extra string) taskSnapshot {
	input := []byte(fmt.Sprintf(cancelTestFrozenInput, extra))
	return taskSnapshot{id: "task-cancel", acquisitionID: "acq-cancel", providerRaw: []byte(`{}`), frozenInputRaw: input}
}

// An adopted qB task was not created by this download: the cancel only
// detaches it and never asks the remover to delete it.
func TestCancelRemovalDetachesAnAdoptedTask(t *testing.T) {
	remover := &recordingCancelRemover{removal: CancelRemoval{Effect: CancelEffectDeletedWithData}}
	service := &Service{cancelRemover: remover}
	task := cancelTestTask(`,"adoption_provenance":{"kind":"unique_infohash_path","provider_task_id":"hash:x","provider_info_hash":"x"}`)
	if _, err := decodeFrozenInput(task.frozenInputRaw); err != nil {
		t.Fatalf("fixture frozen input must decode: %v", err)
	}
	got := service.cancelRemoval(context.Background(), task)
	if got.Effect != CancelEffectDetached || got.Code != "adopted_task" {
		t.Fatalf("adopted task removal = %+v", got)
	}
	if len(remover.calls) != 0 {
		t.Fatalf("an adopted task must not reach the remover: %v", remover.calls)
	}
}

// The remover's effect passes through; a missing remover or an unknown effect
// is a transient failure spent from the sync error budget.
func TestCancelRemovalEffects(t *testing.T) {
	task := cancelTestTask("")
	for _, effect := range []string{CancelEffectDeletedWithData, CancelEffectAbsent, CancelEffectDetached, CancelEffectRetry} {
		remover := &recordingCancelRemover{removal: CancelRemoval{Effect: effect, Code: "code"}}
		got := (&Service{cancelRemover: remover}).cancelRemoval(context.Background(), task)
		if got.Effect != effect {
			t.Fatalf("effect %s became %+v", effect, got)
		}
		if len(remover.calls) != 1 || remover.calls[0] != "acq-cancel" {
			t.Fatalf("the remover must be asked once for the task's acquisition: %v", remover.calls)
		}
	}
	if got := (&Service{}).cancelRemoval(context.Background(), task); got.Effect != CancelEffectRetry {
		t.Fatalf("no remover must retry: %+v", got)
	}
	remover := &recordingCancelRemover{removal: CancelRemoval{Effect: "deleted", Code: " odd "}}
	if got := (&Service{cancelRemover: remover}).cancelRemoval(context.Background(), task); got.Effect != CancelEffectRetry || got.Code != "odd" {
		t.Fatalf("an unknown effect must retry: %+v", got)
	}
}

func TestCancelCodes(t *testing.T) {
	for _, code := range []string{FailureProviderGroupNotAllowed, FailureUserCancelled} {
		if !isCancelCode(code) {
			t.Fatalf("%s must be a cancel code", code)
		}
	}
	for _, code := range []string{FailureDownloaderTaskRemoved, "SYNC_ERROR_BUDGET_EXHAUSTED", ""} {
		if isCancelCode(code) {
			t.Fatalf("%s is not a cancel code", code)
		}
	}
}
