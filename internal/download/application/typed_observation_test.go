package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"bansoko/internal/download/domain"
	"bansoko/internal/download/qbittorrent"
)

func TestCorrelationKeepsDisplayStateWithoutIntegerObservationCopy(t *testing.T) {
	bytes, total := int64(0), int64(1000)
	raw, _, err := encodeCorrelation(domain.ProviderTask{ID: "hash:typed", Hash: "typed", State: domain.TaskDownloading, ProviderState: "pauseddl", ContentCompleteBytes: &bytes, PayloadTotalBytes: &total})
	if err != nil {
		t.Fatal(err)
	}
	var projection map[string]any
	if err = json.Unmarshal(raw, &projection); err != nil {
		t.Fatal(err)
	}
	if _, ok := projection["content_complete_bytes"]; ok {
		t.Fatal("complete bytes leaked into display correlation")
	}
	if _, ok := projection["payload_total_bytes"]; ok {
		t.Fatal("total bytes leaked into display correlation")
	}
	if projection["provider_state"] != "pauseddl" || projection["external_id"] != "hash:typed" {
		t.Fatalf("display fields lost: %s", raw)
	}
}

func TestClassifySyncErrorSeparatesAbsenceFromLookupErrors(t *testing.T) {
	provider := qbittorrent.Client{}
	absent := fmt.Errorf("qB task %q was not found: %w", "hash:abc", domain.ErrProviderTaskAbsent)
	if code, _, observationError := classifySyncError(provider, absent); code != SyncCodeTaskAbsent || !observationError {
		t.Fatalf("absence = %q %v", code, observationError)
	}
	for _, err := range []error{
		errors.New("request qB task lookup: dial tcp: connection refused"),
		errors.New("look up qB task: qB task lookup failed with HTTP 500"),
		errors.New("look up qB task: decode qB task status: unexpected EOF"),
		fmt.Errorf("look up qB task: %w", context.DeadlineExceeded),
		fmt.Errorf("%w: unavailable", qbittorrent.ErrRuntimeConfigUnavailable),
		qbittorrent.ErrPathSnapshotChanged,
		errors.New("qB login failed with HTTP 403"),
		errors.New("qB task \"hash:abc\" was not found"),
	} {
		if code, _, _ := classifySyncError(provider, err); code == SyncCodeTaskAbsent {
			t.Fatalf("%v must not be absence", err)
		}
	}
}

type syncTagCleanupProvider struct {
	*fakeProvider
	cleanupKeys []string
	cleanupErr  error
}

func (p *syncTagCleanupProvider) DeleteOperationTag(_ context.Context, operationKey string) error {
	p.cleanupKeys = append(p.cleanupKeys, operationKey)
	return p.cleanupErr
}

func TestTypedSyncAbsentCleansOperationTagBeforeRecordingAbsence(t *testing.T) {
	absent := fmt.Errorf("qB task absent: %w", domain.ErrProviderTaskAbsent)
	provider := &syncTagCleanupProvider{fakeProvider: &fakeProvider{}}
	code, _, observationError, cause := classifyTypedSyncError(context.Background(), provider, "qb:release-1", absent)
	if code != SyncCodeTaskAbsent || !observationError || !errors.Is(cause, domain.ErrProviderTaskAbsent) {
		t.Fatalf("successful absent cleanup classification = %q %v %v", code, observationError, cause)
	}
	if len(provider.cleanupKeys) != 1 || provider.cleanupKeys[0] != "qb:release-1" {
		t.Fatalf("cleanup operation keys = %v", provider.cleanupKeys)
	}
}

func TestTypedSyncAbsentCleanupFailureStaysRetryable(t *testing.T) {
	absent := fmt.Errorf("qB task absent: %w", domain.ErrProviderTaskAbsent)
	cleanupErr := errors.New("connection reset")
	provider := &syncTagCleanupProvider{fakeProvider: &fakeProvider{}, cleanupErr: cleanupErr}
	code, _, observationError, cause := classifyTypedSyncError(context.Background(), provider, "qb:release-2", absent)
	if code != "qb_sync_retryable" || !observationError || !errors.Is(cause, cleanupErr) {
		t.Fatalf("failed absent cleanup classification = %q %v %v", code, observationError, cause)
	}
	if code == SyncCodeTaskAbsent {
		t.Fatal("absence must not be recorded before cleanup succeeds")
	}
}
