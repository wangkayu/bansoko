package businesslog

import (
	"context"
	"errors"
	"testing"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

type captureActivityWriter struct {
	record observabilityport.ActivityRecord
	calls  int
	err    error
}

func (w *captureActivityWriter) AppendTx(_ context.Context, _ txscope.Tx, record observabilityport.ActivityRecord) error {
	w.calls++
	w.record = record
	return w.err
}

func TestWriteMetadataAppliedTxDelegatesTypedRecord(t *testing.T) {
	writer := &captureActivityWriter{}
	if err := WriteMetadataAppliedTx(context.Background(), txscope.Tx{}, writer, "sub-1", "anime-1", 2); err != nil {
		t.Fatalf("write metadata activity: %v", err)
	}
	if writer.calls != 1 {
		t.Fatalf("writer calls = %d, want 1", writer.calls)
	}
	got := writer.record
	if got.SubscriptionID != "sub-1" || got.ManagedAnimeID != "anime-1" || got.Kind != "metadata_applied" ||
		got.Outcome != "success" || got.ReasonCode != "metadata_applied" || got.SubjectType != "metadata" ||
		got.SubjectID != "anime-1" || got.SubjectVersion != 2 ||
		got.MessageCode != observabilityport.MessageMetadataApplied || len(got.MessageParams) != 0 {
		t.Fatalf("unexpected metadata activity: %+v", got)
	}
}

func TestWriteMetadataAppliedTxPropagatesCentralWriterError(t *testing.T) {
	want := errors.New("writer rejected activity")
	writer := &captureActivityWriter{err: want}
	if err := WriteMetadataAppliedTx(context.Background(), txscope.Tx{}, writer, "sub-1", "anime-1", 2); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestWriteMetadataAppliedTxRequiresCentralWriter(t *testing.T) {
	if err := WriteMetadataAppliedTx(context.Background(), txscope.Tx{}, nil, "sub-1", "anime-1", 2); err == nil {
		t.Fatal("expected nil central writer error")
	}
}
