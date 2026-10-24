package application

import (
	"context"
	"errors"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
)

// fakeMetadataRefreshSource is the sweep's narrow source: a toggle, a target
// list and a per-work re-pull that records what it was asked to refresh.
type fakeMetadataRefreshSource struct {
	enabled    bool
	enabledErr error
	targets    []string
	targetsErr error
	listCalls  int
	refreshed  []string
	fail       map[string]error
	onRefresh  func()
}

func (f *fakeMetadataRefreshSource) MetadataRefreshEnabled(context.Context) (bool, error) {
	return f.enabled, f.enabledErr
}

func (f *fakeMetadataRefreshSource) MetadataRefreshTargets(context.Context) ([]string, error) {
	f.listCalls++
	return f.targets, f.targetsErr
}

func (f *fakeMetadataRefreshSource) RefreshAnimeMetadataApplication(_ context.Context, managedAnimeID string) (AnimeMetadata, error) {
	f.refreshed = append(f.refreshed, managedAnimeID)
	if f.onRefresh != nil {
		f.onRefresh()
	}
	return AnimeMetadata{}, f.fail[managedAnimeID]
}

// recordingSweepLogger keeps the sweep's events so the one-event-per-failure
// rule is asserted without a sink.
type recordingSweepLogger struct {
	infos  []observabilityport.RuntimeEvent
	warns  []observabilityport.RuntimeEvent
	errors []observabilityport.RuntimeEvent
}

func (l *recordingSweepLogger) Debug(context.Context, observabilityport.RuntimeEvent) {}
func (l *recordingSweepLogger) Info(_ context.Context, event observabilityport.RuntimeEvent) {
	l.infos = append(l.infos, event)
}
func (l *recordingSweepLogger) Warn(_ context.Context, event observabilityport.RuntimeEvent) {
	l.warns = append(l.warns, event)
}
func (l *recordingSweepLogger) Error(_ context.Context, event observabilityport.RuntimeEvent) {
	l.errors = append(l.errors, event)
}

func TestMetadataRefreshSweepDisabledIsANoOp(t *testing.T) {
	source := &fakeMetadataRefreshSource{enabled: false, targets: []string{"anime-1"}}
	logger := &recordingSweepLogger{}
	sweep := MetadataRefreshSweep{Source: source, Logger: logger, WorkTimeout: time.Second}

	result, err := sweep.RunPass(context.Background())
	if err != nil {
		t.Fatalf("RunPass: %v", err)
	}
	if result.Enabled || result.Refreshed != 0 || result.Failed != 0 {
		t.Fatalf("disabled pass = %+v", result)
	}
	if source.listCalls != 0 || len(source.refreshed) != 0 {
		t.Fatalf("disabled toggle must not list or refresh, listed=%d refreshed=%v", source.listCalls, source.refreshed)
	}
	if len(logger.warns) != 0 || len(logger.infos) != 0 {
		t.Fatalf("a disabled pass is silent, warns=%v infos=%v", logger.warns, logger.infos)
	}
}

func TestMetadataRefreshSweepRefreshesEveryTargetAndContinuesAfterFailure(t *testing.T) {
	source := &fakeMetadataRefreshSource{
		enabled: true,
		targets: []string{"anime-a", "anime-b", "anime-c"},
		fail:    map[string]error{"anime-b": errors.New("provider unavailable")},
	}
	logger := &recordingSweepLogger{}
	sweep := MetadataRefreshSweep{Source: source, Logger: logger, WorkTimeout: time.Second}

	result, err := sweep.RunPass(context.Background())
	if err != nil {
		t.Fatalf("a failed work must not fail the pass: %v", err)
	}
	if !result.Enabled || result.Refreshed != 2 || result.Failed != 1 {
		t.Fatalf("pass result = %+v", result)
	}
	if want := []string{"anime-a", "anime-b", "anime-c"}; len(source.refreshed) != len(want) {
		t.Fatalf("every target is refreshed once, got %v", source.refreshed)
	} else {
		for index, id := range want {
			if source.refreshed[index] != id {
				t.Fatalf("refresh order = %v, want %v", source.refreshed, want)
			}
		}
	}
	if len(logger.warns) != 1 {
		t.Fatalf("a failed work logs exactly one warning, got %v", logger.warns)
	}
	warning := logger.warns[0]
	if warning.Component != "metadata_refresh" || warning.Operation != "sweep" || warning.OwnerID != "anime-b" || warning.OwnerType != "managed_anime" {
		t.Fatalf("bounded warning = %+v", warning)
	}
	if warning.Message == "" || warning.ErrorClass != "metadata" {
		t.Fatalf("warning carries the fixed diagnostic envelope: %+v", warning)
	}
	if len(logger.errors) != 0 {
		t.Fatalf("a per-work failure is never an error event: %v", logger.errors)
	}
}

func TestMetadataRefreshSweepReadsTheToggleOnEveryPass(t *testing.T) {
	source := &fakeMetadataRefreshSource{enabled: true, targets: []string{"anime-a"}}
	sweep := MetadataRefreshSweep{Source: source, Logger: &recordingSweepLogger{}, WorkTimeout: time.Second}

	if _, err := sweep.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	source.enabled = false
	source.refreshed = nil
	result, err := sweep.RunPass(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Enabled || len(source.refreshed) != 0 {
		t.Fatalf("a toggle turned off between passes stops the sweep, result=%+v refreshed=%v", result, source.refreshed)
	}
}

func TestMetadataRefreshSweepStopsMidPassWithoutBlamingTheWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	source := &fakeMetadataRefreshSource{
		enabled: true,
		targets: []string{"anime-a", "anime-b"},
		fail:    map[string]error{"anime-a": context.Canceled},
		onRefresh: func() {
			cancel()
		},
	}
	logger := &recordingSweepLogger{}
	sweep := MetadataRefreshSweep{Source: source, Logger: logger, WorkTimeout: time.Second}

	if _, err := sweep.RunPass(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("a pass stopped mid-work reports the cancellation, got %v", err)
	}
	if len(source.refreshed) != 1 {
		t.Fatalf("the remaining targets are skipped, got %v", source.refreshed)
	}
	if len(logger.warns) != 0 {
		t.Fatalf("a shutdown is not a provider failure, warns=%v", logger.warns)
	}
}

func TestMetadataRefreshSweepHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &fakeMetadataRefreshSource{enabled: true, targets: []string{"anime-a"}}
	sweep := MetadataRefreshSweep{Source: source, Logger: &recordingSweepLogger{}, WorkTimeout: time.Second}

	result, err := sweep.RunPass(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled pass reports the cancellation, got %v", err)
	}
	if len(source.refreshed) != 0 {
		t.Fatalf("a cancelled pass starts no work, got %v", source.refreshed)
	}
	if !result.Enabled {
		t.Fatalf("the toggle was read before the cancellation: %+v", result)
	}
}
