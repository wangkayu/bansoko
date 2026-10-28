package filemaintenance

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"bansoko/internal/episodeidentity"
	facts "bansoko/internal/release/facts"
)

type frozenCoverageFakeReader struct {
	snapshot *facts.Snapshot
	rows     []facts.FileRow
	err      error
}

func (f frozenCoverageFakeReader) ReadSnapshot(context.Context, string) (*facts.Snapshot, error) {
	return f.snapshot, f.err
}

func (f frozenCoverageFakeReader) ListFileRows(context.Context, string) ([]facts.FileRow, error) {
	return f.rows, nil
}

// TestFrozenFileCoverageDistinguishesOutcomes pins the coverage-reader
// contract: resolved identity returns coverage, a genuinely unresolved
// identity returns ok=false without an error (the file is unknown), while
// missing snapshot/rows and read/decode failures return typed errors that
// must retry instead of ever recording the file as unknown.
func TestFrozenFileCoverageDistinguishesOutcomes(t *testing.T) {
	ctx := context.Background()
	coverageKey := "tv:e1"

	t.Run("resolved identity", func(t *testing.T) {
		reader := frozenCoverageFakeReader{
			snapshot: &facts.Snapshot{ID: "snap-1", SchemaVersion: facts.SchemaVersion},
			rows: []facts.FileRow{{
				SnapshotID: "snap-1", FileRef: "ep.mkv",
				EffectiveFacts: facts.EffectiveFileFacts{Identity: episodeidentity.EpisodeIdentity{
					State: episodeidentity.StateResolved, CoverageKey: &coverageKey,
				}},
			}},
		}
		coverage, version, ok, err := FrozenFileCoverage(ctx, reader, "rel-1", "ep.mkv")
		if err != nil || !ok || len(coverage) != 1 || coverage[0] != coverageKey || version != facts.SchemaVersion {
			t.Fatalf("resolved identity = %v/%v/%t/%v", coverage, version, ok, err)
		}
	})

	t.Run("unresolved identity is unknown", func(t *testing.T) {
		reader := frozenCoverageFakeReader{
			snapshot: &facts.Snapshot{ID: "snap-1", SchemaVersion: facts.SchemaVersion},
			rows: []facts.FileRow{{
				SnapshotID: "snap-1", FileRef: "ep.mkv",
				EffectiveFacts: facts.EffectiveFileFacts{Identity: episodeidentity.EpisodeIdentity{State: episodeidentity.StateUnresolved}},
			}},
		}
		_, _, ok, err := FrozenFileCoverage(ctx, reader, "rel-1", "ep.mkv")
		if err != nil || ok {
			t.Fatalf("unresolved identity must be ok=false/err=nil, got %t/%v", ok, err)
		}
	})

	t.Run("resolved without coverage key is unknown", func(t *testing.T) {
		reader := frozenCoverageFakeReader{
			snapshot: &facts.Snapshot{ID: "snap-1", SchemaVersion: facts.SchemaVersion},
			rows: []facts.FileRow{{
				SnapshotID: "snap-1", FileRef: "ep.mkv",
				EffectiveFacts: facts.EffectiveFileFacts{Identity: episodeidentity.EpisodeIdentity{State: episodeidentity.StateResolved}},
			}},
		}
		if _, _, ok, err := FrozenFileCoverage(ctx, reader, "rel-1", "ep.mkv"); err != nil || ok {
			t.Fatalf("resolved without coverage key must be ok=false/err=nil, got %t/%v", ok, err)
		}
	})

	t.Run("missing snapshot is typed evidence missing", func(t *testing.T) {
		reader := frozenCoverageFakeReader{snapshot: nil}
		_, _, ok, err := FrozenFileCoverage(ctx, reader, "rel-1", "ep.mkv")
		if ok || !errors.Is(err, ErrPlacementEvidenceMissing) {
			t.Fatalf("missing snapshot = %t/%v, want typed evidence missing", ok, err)
		}
	})

	t.Run("snapshot absence reported as ErrNoRows is typed evidence missing", func(t *testing.T) {
		// The production media-facts reader surfaces an absent snapshot row as
		// sql.ErrNoRows. It is the same "evidence does not exist" class, not a
		// transient database read failure, so the placement stage must report the
		// stable evidence-missing reason instead of a retryable read fault.
		reader := frozenCoverageFakeReader{err: sql.ErrNoRows}
		_, _, ok, err := FrozenFileCoverage(ctx, reader, "rel-1", "ep.mkv")
		if ok || !errors.Is(err, ErrPlacementEvidenceMissing) {
			t.Fatalf("ErrNoRows snapshot = %t/%v, want typed evidence missing", ok, err)
		}
	})

	t.Run("missing file row is typed evidence missing", func(t *testing.T) {
		reader := frozenCoverageFakeReader{
			snapshot: &facts.Snapshot{ID: "snap-1", SchemaVersion: facts.SchemaVersion},
			rows:     []facts.FileRow{},
		}
		_, _, ok, err := FrozenFileCoverage(ctx, reader, "rel-1", "ep.mkv")
		if ok || !errors.Is(err, ErrPlacementEvidenceMissing) {
			t.Fatalf("missing file row = %t/%v, want typed evidence missing", ok, err)
		}
	})

	t.Run("empty file ref is typed evidence missing", func(t *testing.T) {
		reader := frozenCoverageFakeReader{
			snapshot: &facts.Snapshot{ID: "snap-1", SchemaVersion: facts.SchemaVersion},
			rows:     []facts.FileRow{},
		}
		_, _, ok, err := FrozenFileCoverage(ctx, reader, "rel-1", " ")
		if ok || !errors.Is(err, ErrPlacementEvidenceMissing) {
			t.Fatalf("empty file ref = %t/%v, want typed evidence missing", ok, err)
		}
	})

	t.Run("read failure is a raw retryable error", func(t *testing.T) {
		reader := frozenCoverageFakeReader{err: errors.New("connection reset")}
		_, _, ok, err := FrozenFileCoverage(ctx, reader, "rel-1", "ep.mkv")
		if ok || err == nil || errors.Is(err, ErrPlacementEvidenceMissing) {
			t.Fatalf("read failure = %t/%v, want raw retryable error", ok, err)
		}
	})

	t.Run("nil reader reads as unknown", func(t *testing.T) {
		if _, _, ok, err := FrozenFileCoverage(ctx, nil, "rel-1", "ep.mkv"); ok || err != nil {
			t.Fatalf("nil reader = %t/%v, want ok=false/err=nil", ok, err)
		}
	})
}
