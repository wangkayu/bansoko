package downloadcompletion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bansoko/internal/batchplan"
)

type fakeQB struct {
	torrent    *Torrent
	listed     []Torrent
	listErr    error
	listCalls  int
	others     []Torrent
	othersErr  error
	afterState string
	// lag is the number of reads that still see the old state after a pause
	// or a delete (qBittorrent applies both asynchronously).
	lag     int
	pending func()
	// stays keeps the task after a delete (verification must catch it).
	stays      bool
	infoErr    error
	actionErr  error
	changeNext bool
	calls      []string
	// webAPIVersion is what app/webapiVersion reports.
	webAPIVersion  string
	versionErr     error
	limits         []ShareLimitsRequest
	cleanupErr     error
	cleanupRefs    []string
	cleanupBatches [][]string
}

func (f *fakeQB) Torrent(_ context.Context, hash string) (Torrent, bool, error) {
	f.calls = append(f.calls, "info")
	if f.infoErr != nil {
		return Torrent{}, false, f.infoErr
	}
	if f.torrent == nil {
		return Torrent{}, false, nil
	}
	if f.pending != nil {
		if f.lag > 0 {
			f.lag--
		} else {
			f.pending()
			f.pending = nil
			if f.torrent == nil {
				return Torrent{}, false, nil
			}
		}
	}
	value := *f.torrent
	if f.changeNext {
		f.changeNext = false
		defer func() { f.torrent.ContentPath = f.torrent.ContentPath + "-moved" }()
	}
	return value, true, nil
}

func (f *fakeQB) List(context.Context) ([]Torrent, error) {
	f.calls = append(f.calls, "list")
	f.listCalls++
	return f.listed, f.listErr
}

func (f *fakeQB) Others(_ context.Context, hash string) ([]Torrent, error) {
	f.calls = append(f.calls, "others")
	return f.others, f.othersErr
}

func (f *fakeQB) Pause(_ context.Context, hash string) error {
	f.calls = append(f.calls, "pause:"+hash)
	if f.actionErr != nil {
		return f.actionErr
	}
	if f.afterState != "" {
		apply := func() { f.torrent.State = f.afterState }
		if f.lag > 0 {
			f.pending = apply
		} else {
			apply()
		}
	}
	return nil
}

func (f *fakeQB) Delete(_ context.Context, hash string, deleteFiles bool) error {
	if deleteFiles {
		f.calls = append(f.calls, "delete+data:"+hash)
	} else {
		f.calls = append(f.calls, "delete:"+hash)
	}
	if f.actionErr != nil {
		return f.actionErr
	}
	if !f.stays {
		if f.lag > 0 {
			f.pending = func() { f.torrent = nil }
		} else {
			f.torrent = nil
		}
	}
	return nil
}

func (f *fakeQB) WebAPIVersion(context.Context) (string, error) {
	f.calls = append(f.calls, "webapi")
	if f.versionErr != nil {
		return "", f.versionErr
	}
	return f.webAPIVersion, nil
}

func (f *fakeQB) SetShareLimits(_ context.Context, hash string, limits ShareLimitsRequest) error {
	f.calls = append(f.calls, "limits:"+hash)
	if f.actionErr != nil {
		return f.actionErr
	}
	f.limits = append(f.limits, limits)
	return nil
}

func (f *fakeQB) CleanupTaskTags(_ context.Context, providerRefs []string) error {
	f.calls = append(f.calls, "cleanup:"+strings.Join(providerRefs, ","))
	f.cleanupRefs = append(f.cleanupRefs, providerRefs...)
	f.cleanupBatches = append(f.cleanupBatches, append([]string(nil), providerRefs...))
	return f.cleanupErr
}

type fakeLibrary struct {
	paths []string
	err   error
}

func (f fakeLibrary) EachLibraryPath(_ context.Context, visit func(string) error) error {
	for _, path := range f.paths {
		if err := visit(path); err != nil {
			return err
		}
	}
	return nil
}

type fakeSharing struct {
	shared bool
	err    error
}

func (f fakeSharing) SharedByOther(context.Context, string, string) (bool, error) {
	return f.shared, f.err
}

func executorFixture(t *testing.T) (Executor, *fakeQB, layout, Completion) {
	t.Helper()
	l := newLayout(t)
	qb := &fakeQB{torrent: &Torrent{
		Hash: testHash, State: "uploading", SavePath: l.save, ContentPath: l.content,
		FileNames: []string{"Show/ep1.mkv", "Show/ep2.mkv", "Show/extra/x.nfo"},
	}}
	executor := Executor{Sleep: func(context.Context, time.Duration) {}, QB: qb, Library: fakeLibrary{paths: []string{filepath.Join(l.library, "Show", "S01E01.mkv")}}, Sharing: fakeSharing{}}
	completion := Completion{ID: "c1", AcquisitionID: "a1", Action: ActionRemoveWithData, InfoHash: testHash, ProviderRef: "tag:bansoko-operation-1", RemoteRoot: l.save, ApplicationRoot: l.save}
	return executor, qb, l, completion
}

func TestExecutorSeedDoesNothing(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	c.Action = ActionSeed
	got := executor.Run(context.Background(), c)
	if !got.Success || got.Code != ResultSeedKept || len(qb.calls) != 0 {
		t.Fatalf("seed = %+v calls=%v", got, qb.calls)
	}
}

func TestExecutorPause(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	c.Action = ActionPause
	qb.afterState = "stoppedUP"
	got := executor.Run(context.Background(), c)
	if !got.Success || got.Code != ResultPaused {
		t.Fatalf("pause = %+v", got)
	}
	if want := []string{"info", "pause:" + testHash, "info"}; len(qb.calls) != 3 || qb.calls[1] != want[1] {
		t.Fatalf("calls = %v", qb.calls)
	}
}

func TestExecutorPauseVerifiesTheTaskStopped(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	c.Action = ActionPause
	qb.afterState = "uploading"
	got := executor.Run(context.Background(), c)
	if got.Success || !got.Retryable || got.Code != FailureVerifyFailed {
		t.Fatalf("a task still running after pause = %+v", got)
	}
}

func TestExecutorRemoveKeepsFilesAndNeverChecksPaths(t *testing.T) {
	executor, qb, l, c := executorFixture(t)
	c.Action = ActionRemove
	// Even a library path inside the content does not matter: no file is deleted.
	executor.Library = fakeLibrary{paths: []string{filepath.Join(l.content, "ep1.mkv")}}
	got := executor.Run(context.Background(), c)
	if !got.Success || got.Code != ResultRemoved || qb.torrent != nil {
		t.Fatalf("remove = %+v", got)
	}
	for _, call := range qb.calls {
		if call == "delete+data:"+testHash {
			t.Fatalf("remove asked qBittorrent to delete data: %v", qb.calls)
		}
	}
}

func TestExecutorRemoveWithDataDeletesOnlyAfterTheCheck(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	got := executor.Run(context.Background(), c)
	if !got.Success || got.Code != ResultRemovedData || qb.torrent != nil {
		t.Fatalf("remove_with_data = %+v", got)
	}
	found := false
	for _, call := range qb.calls {
		found = found || call == "delete+data:"+testHash
	}
	if !found {
		t.Fatalf("no delete with data was issued: %v", qb.calls)
	}
}

func TestExecutorRemoveWithDataRefusalNeverCallsDelete(t *testing.T) {
	cases := map[string]func(*Executor, layout){
		"library path inside the content": func(e *Executor, l layout) {
			e.Library = fakeLibrary{paths: []string{filepath.Join(l.content, "ep2.mkv")}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			executor, qb, l, c := executorFixture(t)
			mutate(&executor, l)
			got := executor.Run(context.Background(), c)
			if got.Success || got.Retryable {
				t.Fatalf("refusal = %+v", got)
			}
			for _, call := range qb.calls {
				if call == "delete+data:"+testHash || call == "delete:"+testHash {
					t.Fatalf("a refused action still deleted: %v", qb.calls)
				}
			}
			if qb.torrent == nil {
				t.Fatal("task was removed")
			}
		})
	}
}

func TestExecutorRemoveWithDataRefusesAnUnrecordedFrozenRoot(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	c.RemoteRoot = ""
	got := executor.Run(context.Background(), c)
	if got.Success || got.Code != CodeContentUnresolved {
		t.Fatalf("no frozen root = %+v", got)
	}
	for _, call := range qb.calls {
		if call == "delete+data:"+testHash {
			t.Fatal("deleted without a frozen root")
		}
	}
}

func TestExecutorRemoveWithDataRefusesWhenAnotherTaskSharesTheContent(t *testing.T) {
	executor, qb, l, c := executorFixture(t)
	qb.others = []Torrent{{Hash: "9999999999999999999999999999999999999999", SavePath: l.save, ContentPath: l.content}}
	got := executor.Run(context.Background(), c)
	if got.Success || got.Retryable || got.Code != CodeContentSharedWithOther {
		t.Fatalf("shared content = %+v", got)
	}
	for _, call := range qb.calls {
		if call == "delete+data:"+testHash {
			t.Fatalf("deleted a task whose content another task shares: %v", qb.calls)
		}
	}
	// A task elsewhere does not block the delete; remove (no data) never asks.
	executor, qb, l, c = executorFixture(t)
	qb.others = []Torrent{{Hash: "9999999999999999999999999999999999999999", SavePath: filepath.Join(l.root, "elsewhere"), ContentPath: filepath.Join(l.root, "elsewhere", "Show")}}
	if got := executor.Run(context.Background(), c); !got.Success || got.Code != ResultRemovedData {
		t.Fatalf("a task elsewhere blocked the delete: %+v", got)
	}
	executor, qb, l, c = executorFixture(t)
	qb.others = []Torrent{{Hash: "9999999999999999999999999999999999999999", SavePath: l.save, ContentPath: l.content}}
	c.Action = ActionRemove
	if got := executor.Run(context.Background(), c); !got.Success {
		t.Fatalf("remove without data must not care about other tasks: %+v", got)
	}
	for _, call := range qb.calls {
		if call == "others" {
			t.Fatal("remove without data listed the other tasks")
		}
	}
}

func TestExecutorRemoveWithDataFailsClosedWhenTheTaskListCannotBeRead(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	qb.othersErr = errors.New("connection reset")
	got := executor.Run(context.Background(), c)
	if got.Success || !got.Retryable {
		t.Fatalf("an unreadable task list = %+v", got)
	}
	for _, call := range qb.calls {
		if call == "delete+data:"+testHash {
			t.Fatal("deleted without knowing the other tasks")
		}
	}
}

func TestExecutorRemoveWithDataRechecksTheTaskBeforeDeleting(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	// The third read (the re-check) sees a different content path.
	qb.changeNext = false
	reads := 0
	wrapped := &changingQB{fakeQB: qb, changeAt: 2, reads: &reads}
	executor.QB = wrapped
	got := executor.Run(context.Background(), c)
	if got.Success || !got.Retryable || got.Code != FailureTaskChanged {
		t.Fatalf("changed task = %+v", got)
	}
	for _, call := range qb.calls {
		if call == "delete+data:"+testHash {
			t.Fatal("deleted a task that changed after the check")
		}
	}
}

type changingQB struct {
	*fakeQB
	changeAt int
	reads    *int
}

func (c *changingQB) Torrent(ctx context.Context, hash string) (Torrent, bool, error) {
	*c.reads++
	value, found, err := c.fakeQB.Torrent(ctx, hash)
	if *c.reads == c.changeAt && found {
		value.FileNames = append(append([]string(nil), value.FileNames...), "Show/extra/new.mkv")
	}
	return value, found, err
}

func TestExecutorAbsentTaskIsDone(t *testing.T) {
	for _, action := range []string{ActionPause, ActionRemove, ActionRemoveWithData} {
		executor, qb, _, c := executorFixture(t)
		qb.torrent = nil
		c.Action = action
		got := executor.Run(context.Background(), c)
		if !got.Success || got.Code != ResultTaskAbsent {
			t.Fatalf("%s on an absent task = %+v", action, got)
		}
		wantCleanups := 0
		if action == ActionRemove || action == ActionRemoveWithData {
			wantCleanups = 1
		}
		if len(qb.cleanupRefs) != wantCleanups {
			t.Fatalf("%s absent cleanups = %v, want %d", action, qb.cleanupRefs, wantCleanups)
		}
	}
}

func TestExecutorRemoveWithDataRecheckAbsentCleansFrozenTag(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	vanishing := &vanishAfterFirstReadQB{fakeQB: qb}
	executor.QB = vanishing
	got := executor.Run(context.Background(), c)
	if !got.Success || got.Code != ResultTaskAbsent || len(qb.cleanupRefs) != 1 || qb.cleanupRefs[0] != c.ProviderRef {
		t.Fatalf("remove_with_data absent replay = %+v cleanups=%v", got, qb.cleanupRefs)
	}
	for _, call := range qb.calls {
		if call == "delete+data:"+testHash {
			t.Fatalf("an already absent recheck must not delete again: %v", qb.calls)
		}
	}
}

type vanishAfterFirstReadQB struct {
	*fakeQB
	reads int
}

func (v *vanishAfterFirstReadQB) Torrent(ctx context.Context, hash string) (Torrent, bool, error) {
	v.reads++
	if v.reads > 1 {
		v.fakeQB.torrent = nil
	}
	return v.fakeQB.Torrent(ctx, hash)
}

func TestExecutorTagCleanupFailureRetriesAndAbsentReplayConverges(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	c.Action = ActionRemove
	qb.cleanupErr = errors.New("connection reset")
	if got := executor.Run(context.Background(), c); got.Success || !got.Retryable || got.Code != FailureQBUnavailable {
		t.Fatalf("cleanup failure after delete = %+v", got)
	}
	if qb.torrent != nil {
		t.Fatal("the fixture torrent should remain deleted while cleanup retries")
	}
	qb.cleanupErr = nil
	if got := executor.Run(context.Background(), c); !got.Success || got.Code != ResultTaskAbsent {
		t.Fatalf("absent cleanup replay = %+v", got)
	}
	if len(qb.cleanupRefs) != 2 || qb.cleanupRefs[0] != c.ProviderRef || qb.cleanupRefs[1] != c.ProviderRef {
		t.Fatalf("cleanup refs = %v", qb.cleanupRefs)
	}
}

func TestExecutorSharedTaskIsSkipped(t *testing.T) {
	for _, action := range []string{ActionPause, ActionRemove, ActionRemoveWithData} {
		executor, qb, _, c := executorFixture(t)
		executor.Sharing = fakeSharing{shared: true}
		c.Action = action
		got := executor.Run(context.Background(), c)
		if !got.Success || got.Code != ResultTorrentShared || len(qb.calls) != 0 {
			t.Fatalf("%s on a shared task = %+v calls=%v", action, got, qb.calls)
		}
	}
}

func TestExecutorTransientFailuresRetry(t *testing.T) {
	boom := errors.New("connection reset")
	executor, qb, _, c := executorFixture(t)
	qb.infoErr = boom
	if got := executor.Run(context.Background(), c); !got.Retryable || got.Code != FailureQBUnavailable {
		t.Fatalf("info error = %+v", got)
	}
	executor, qb, _, c = executorFixture(t)
	qb.actionErr = boom
	if got := executor.Run(context.Background(), c); !got.Retryable || got.Code != FailureQBUnavailable {
		t.Fatalf("action error = %+v", got)
	}
	executor, _, _, c = executorFixture(t)
	executor.Sharing = fakeSharing{err: boom}
	if got := executor.Run(context.Background(), c); !got.Retryable || got.Code != FailureStoreUnavailable {
		t.Fatalf("store error = %+v", got)
	}
	executor, qb, _, c = executorFixture(t)
	qb.stays = true
	if got := executor.Run(context.Background(), c); !got.Retryable || got.Code != FailureVerifyFailed {
		t.Fatalf("task survived the delete = %+v", got)
	}
}

func TestExecutorMismatchAndUnknown(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	qb.torrent.Hash = "ffffffffffffffffffffffffffffffffffffffff"
	if got := executor.Run(context.Background(), c); got.Success || got.Retryable || got.Code != CodeQBTaskMismatch {
		t.Fatalf("other task = %+v", got)
	}
	executor, _, _, c = executorFixture(t)
	c.InfoHash = ""
	if got := executor.Run(context.Background(), c); got.Success || got.Retryable || got.Code != FailureTaskUnknown {
		t.Fatalf("no hash = %+v", got)
	}
	executor, _, _, c = executorFixture(t)
	c.Action = "wipe"
	if got := executor.Run(context.Background(), c); got.Success || got.Retryable || got.Code != FailureInvalidAction {
		t.Fatalf("unknown action = %+v", got)
	}
}

func TestExecutorDoesNotTouchTheFilesystem(t *testing.T) {
	// The executor's fakes delete nothing; prove the files are all still there
	// after a full remove_with_data run (the product itself never deletes).
	executor, _, l, c := executorFixture(t)
	if got := executor.Run(context.Background(), c); !got.Success {
		t.Fatalf("run = %+v", got)
	}
	for _, name := range []string{"Show/ep1.mkv", "Show/ep2.mkv", "Show/extra/x.nfo"} {
		if _, err := os.Stat(filepath.Join(l.save, filepath.FromSlash(name))); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// qBittorrent applies stop and delete asynchronously: a read right after the
// call can still show the old state, so the result is re-read for a short while.
func TestExecutorWaitsForQBittorrentToApplyPauseAndDelete(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	c.Action = ActionPause
	qb.afterState = "stoppedUP"
	qb.lag = 3
	if got := executor.Run(context.Background(), c); !got.Success || got.Code != ResultPaused {
		t.Fatalf("a pause applied after a few reads = %+v", got)
	}

	executor, qb, _, c = executorFixture(t)
	qb.lag = 3
	if got := executor.Run(context.Background(), c); !got.Success || got.Code != ResultRemovedData {
		t.Fatalf("a delete applied after a few reads = %+v", got)
	}

	// A change that never shows is still reported, after a bounded wait.
	executor, qb, _, c = executorFixture(t)
	c.Action = ActionPause
	qb.afterState = "uploading"
	slept := time.Duration(0)
	executor.Sleep = func(_ context.Context, d time.Duration) { slept += d }
	if got := executor.Run(context.Background(), c); got.Success || !got.Retryable || got.Code != FailureVerifyFailed {
		t.Fatalf("a pause that never applies = %+v", got)
	}
	if slept < 4*time.Second || slept > 6*time.Second {
		t.Fatalf("waited %v, want about five seconds", slept)
	}
}

func TestNotRunningStates(t *testing.T) {
	for state, want := range map[string]bool{
		"stoppedUP": true, "stoppedDL": true, "pausedUP": true, "pausedDL": true,
		"missingFiles": true, "error": true, "STOPPEDUP": true,
		"uploading": false, "stalledUP": false, "downloading": false, "forcedUP": false, "queuedUP": false, "": false,
	} {
		if got := notRunning(state); got != want {
			t.Fatalf("notRunning(%q) = %v, want %v", state, got, want)
		}
	}
}

func seedLimitsCompletion(c Completion, atLimit string) Completion {
	minutes, ratio := int64(2880), 2.5
	c.Action = ActionSeed
	c.SeedLimits = &batchplan.SeedLimits{SeedingTimeMinutes: &minutes, Ratio: &ratio, AtLimit: atLimit}
	return c
}

func TestExecutorSeedLimitsSendTheActionWhenTheWebAPIHasIt(t *testing.T) {
	for _, tc := range []struct {
		version, wantAction, wantMode string
	}{
		{"2.12.0", "Stop", ""},
		{"2.15.2", "Stop", ""},
		{"2.15.3", "Stop", "MatchAny"},
		{"2.17.0", "Stop", "MatchAny"},
	} {
		executor, qb, _, c := executorFixture(t)
		qb.webAPIVersion = tc.version
		got := executor.Run(context.Background(), seedLimitsCompletion(c, batchplan.SeedLimitStop))
		if !got.Success || got.Code != ResultSeedLimitsSet || len(qb.limits) != 1 {
			t.Fatalf("%s: %+v calls=%v", tc.version, got, qb.calls)
		}
		sent := qb.limits[0]
		if sent.RatioLimit != 2.5 || sent.SeedingTimeLimit != 2880 || sent.InactiveSeedingTimeLimit != -2 || sent.Action != tc.wantAction || sent.Mode != tc.wantMode {
			t.Fatalf("%s: request = %+v", tc.version, sent)
		}
		for _, call := range qb.calls {
			if call == "pause:"+testHash || call == "delete:"+testHash || call == "delete+data:"+testHash {
				t.Fatalf("%s: seed limits acted on the task: %v", tc.version, qb.calls)
			}
		}
	}
}

func TestExecutorSeedLimitsOnOldQBittorrentLeaveTheGlobalAction(t *testing.T) {
	for _, version := range []string{"2.9.3", "2.11.10", "garbage", ""} {
		executor, qb, _, c := executorFixture(t)
		qb.webAPIVersion = version
		c = seedLimitsCompletion(c, batchplan.SeedLimitRemove)
		c.SeedLimits.Ratio = nil
		got := executor.Run(context.Background(), c)
		if !got.Success || got.Code != ResultSeedLimitsSetGlobalAction || len(qb.limits) != 1 {
			t.Fatalf("%q: %+v calls=%v", version, got, qb.calls)
		}
		sent := qb.limits[0]
		if sent.Action != "" || sent.Mode != "" || sent.RatioLimit != -1 || sent.SeedingTimeLimit != 2880 || sent.InactiveSeedingTimeLimit != -2 {
			t.Fatalf("%q: request = %+v", version, sent)
		}
	}
}

func TestExecutorSeedLimitsRemoveWithDataChecksTheLibraryFirst(t *testing.T) {
	executor, qb, l, c := executorFixture(t)
	qb.webAPIVersion = "2.12.0"
	got := executor.Run(context.Background(), seedLimitsCompletion(c, batchplan.SeedLimitRemoveWithData))
	if !got.Success || got.Code != ResultSeedLimitsSet || qb.limits[0].Action != "RemoveWithContent" {
		t.Fatalf("safe hand-off = %+v %+v", got, qb.limits)
	}

	executor, qb, l, c = executorFixture(t)
	qb.webAPIVersion = "2.12.0"
	executor.Library = fakeLibrary{paths: []string{filepath.Join(l.content, "ep2.mkv")}}
	got = executor.Run(context.Background(), seedLimitsCompletion(c, batchplan.SeedLimitRemoveWithData))
	if got.Success || got.Retryable || got.Code != CodePlacementInsideContent {
		t.Fatalf("refusal = %+v", got)
	}
	if len(qb.limits) != 0 {
		t.Fatalf("a refused hand-off still set limits: %+v", qb.limits)
	}
	// Without a library the data-deleting hand-off is not wired and retries.
	executor, qb, _, c = executorFixture(t)
	executor.Library = nil
	if got := executor.Run(context.Background(), seedLimitsCompletion(c, batchplan.SeedLimitRemoveWithData)); !got.Retryable {
		t.Fatalf("unwired library = %+v", got)
	}
	_ = qb
}

func TestExecutorSeedLimitsFollowTheTaskRules(t *testing.T) {
	executor, qb, _, c := executorFixture(t)
	qb.torrent = nil
	if got := executor.Run(context.Background(), seedLimitsCompletion(c, batchplan.SeedLimitStop)); !got.Success || got.Code != ResultTaskAbsent {
		t.Fatalf("absent = %+v", got)
	}
	executor, _, _, c = executorFixture(t)
	executor.Sharing = fakeSharing{shared: true}
	if got := executor.Run(context.Background(), seedLimitsCompletion(c, batchplan.SeedLimitStop)); !got.Success || got.Code != ResultTorrentShared {
		t.Fatalf("shared = %+v", got)
	}
	executor, _, _, c = executorFixture(t)
	c = seedLimitsCompletion(c, batchplan.SeedLimitStop)
	c.InfoHash = ""
	if got := executor.Run(context.Background(), c); got.Success || got.Code != FailureTaskUnknown {
		t.Fatalf("unknown = %+v", got)
	}
	executor, qb, _, c = executorFixture(t)
	qb.versionErr = errors.New("connection refused")
	if got := executor.Run(context.Background(), seedLimitsCompletion(c, batchplan.SeedLimitStop)); !got.Retryable || got.Code != FailureQBUnavailable {
		t.Fatalf("version error = %+v", got)
	}
	executor, qb, _, c = executorFixture(t)
	qb.webAPIVersion = "2.9.3"
	qb.actionErr = errors.New("HTTP 400")
	if got := executor.Run(context.Background(), seedLimitsCompletion(c, batchplan.SeedLimitStop)); !got.Retryable || got.Code != FailureQBUnavailable {
		t.Fatalf("set error = %+v", got)
	}
	executor, _, _, c = executorFixture(t)
	c = seedLimitsCompletion(c, "wipe")
	if got := executor.Run(context.Background(), c); got.Success || got.Retryable || got.Code != FailureInvalidAction {
		t.Fatalf("invalid limits = %+v", got)
	}
}

func TestShareLimitSupportThresholds(t *testing.T) {
	for version, want := range map[string][2]bool{
		"2.11.10": {false, false}, "2.12.0": {true, false}, "2.12": {true, false}, "2.15.2": {true, false},
		"2.15.3": {true, true}, "3.0.0": {true, true}, "v2.12.1": {true, false}, "2": {false, false}, "2.x.0": {false, false},
	} {
		action, mode := shareLimitSupport(version)
		if [2]bool{action, mode} != want {
			t.Fatalf("%q = %v %v, want %v", version, action, mode, want)
		}
	}
}
