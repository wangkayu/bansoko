package application

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	observabilityport "bansoko/internal/observability/port"
)

func runtimeLogTestEvent(message string) observabilityport.RuntimeEvent {
	return observabilityport.RuntimeEvent{Message: message, Component: "test", Operation: "runtime_log", Outcome: "success"}
}

func runtimeLogTestLine(at time.Time, level, message string, workItemID string) string {
	if workItemID == "" {
		return fmt.Sprintf(`{"timestamp":%q,"level":%q,"message":%q}`, at.Format(time.RFC3339Nano), level, message)
	}
	return fmt.Sprintf(`{"timestamp":%q,"level":%q,"message":%q,"kind":"work_item","work_item_id":%q}`, at.Format(time.RFC3339Nano), level, message, workItemID)
}

func writeRuntimeLogTestLine(t *testing.T, store *RuntimeLogStore, line string) {
	t.Helper()
	if _, err := store.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readRuntimeLogTestPage(t *testing.T, logger *JSONRuntimeLogger, query RuntimeLogQuery) RuntimeLogPage {
	t.Helper()
	page, err := logger.RuntimeLogs(query)
	if err != nil {
		t.Fatalf("read runtime logs: %v", err)
	}
	return page
}

func runtimeLogTestMessages(page RuntimeLogPage) []string {
	messages := make([]string, 0, len(page.Entries))
	for _, entry := range page.Entries {
		messages = append(messages, entry.Message)
	}
	return messages
}

func listRuntimeLogTestVolumes(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read logs dir: %v", err)
	}
	var volumes []os.DirEntry
	for _, entry := range entries {
		if runtimeLogVolumeName.MatchString(entry.Name()) {
			volumes = append(volumes, entry)
		}
	}
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].Name() < volumes[j].Name() })
	return volumes
}

func TestPersistentRuntimeLoggerKeepsTheWriterAndTheHistory(t *testing.T) {
	var writer strings.Builder
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	logger := NewPersistentRuntimeLogger(&writer, dir)
	t.Cleanup(func() { _ = logger.Close() })
	logger.Warn(t.Context(), runtimeLogTestEvent("stderr and history"))
	if !strings.Contains(writer.String(), "stderr and history") {
		t.Fatalf("the stderr writer lost the line: %q", writer.String())
	}
	page := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogWarn, Limit: 10})
	if page.Source != RuntimeLogSourceFile || len(page.Entries) != 1 || page.Entries[0].Message != "stderr and history" {
		t.Fatalf("history page = %+v", page)
	}
}

func TestRuntimeLogStoreVolumeBoundsAndUTCNaming(t *testing.T) {
	if RuntimeLogVolumeMaxBytes != 10<<20 {
		t.Fatalf("volume bound = %d, want 10 MiB", RuntimeLogVolumeMaxBytes)
	}
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	// 2026-11-19 02:00 at UTC+8 is 2026-11-18 18:00 UTC.
	now := time.Date(2026, 11, 19, 2, 0, 0, 0, time.FixedZone("utc8", 8*3600))
	line := runtimeLogTestLine(now, "info", "one whole line", "")
	budget := int64(len(line) + 1)
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, MaxVolumeBytes: budget, MaxTotalBytes: 1 << 20,
		Retention: 24 * time.Hour, Now: func() time.Time { return now },
	})
	t.Cleanup(func() { _ = store.Close() })
	for i := 0; i < 3; i++ {
		writeRuntimeLogTestLine(t, store, line)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	volumes := listRuntimeLogTestVolumes(t, dir)
	if len(volumes) != 3 {
		t.Fatalf("volumes = %v, want three", volumeNames(volumes))
	}
	for index, volume := range volumes {
		wantName := fmt.Sprintf("2026-11-18-%03d.log", index+1)
		if volume.Name() != wantName {
			t.Fatalf("volume %d = %q, want %q", index, volume.Name(), wantName)
		}
		body, err := os.ReadFile(filepath.Join(dir, volume.Name()))
		if err != nil {
			t.Fatalf("read volume: %v", err)
		}
		if string(body) != line+"\n" {
			t.Fatalf("volume %q split a line: %q", volume.Name(), string(body))
		}
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("logs dir mode = %o, want 700", dirInfo.Mode().Perm())
	}
	fileInfo, err := volumes[0].Info()
	if err != nil {
		t.Fatalf("stat volume: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("volume mode = %o, want 600", fileInfo.Mode().Perm())
	}
}

func TestRuntimeLogStoreRollsOnFirstRecordAfterUTCMidnight(t *testing.T) {
	dir := t.TempDir()
	var seconds atomic.Int64
	midnight := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC).Unix()
	seconds.Store(midnight - 1)
	now := func() time.Time { return time.Unix(seconds.Load(), 0).UTC() }
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{Dir: dir, Now: now})
	t.Cleanup(func() { _ = store.Close() })
	write := func(message string) {
		writeRuntimeLogTestLine(t, store, runtimeLogTestLine(now(), "info", message, ""))
		if err := store.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if len(listRuntimeLogTestVolumes(t, dir)) != 0 {
		t.Fatal("startup created an empty volume")
	}
	write("before midnight")
	seconds.Store(midnight)
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(listRuntimeLogTestVolumes(t, dir)) != 1 {
		t.Fatal("idle midnight created an empty volume")
	}
	write("after midnight")
	for name, message := range map[string]string{"2026-11-18-001.log": "before midnight", "2026-11-19-001.log": "after midnight"} {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Count(string(body), "\n") != 1 || !strings.Contains(string(body), message) {
			t.Fatalf("%s: %q", name, body)
		}
	}
}

func TestRuntimeLogStoreAppendsToTheSameDayVolumeAfterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	now := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	options := RuntimeLogStoreOptions{Dir: dir, MaxVolumeBytes: 1 << 20, Now: func() time.Time { return now }}

	first := NewRuntimeLogStore(options)
	writeRuntimeLogTestLine(t, first, runtimeLogTestLine(now, "info", "before restart", ""))
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	second := NewRuntimeLogStore(options)
	writeRuntimeLogTestLine(t, second, runtimeLogTestLine(now.Add(time.Second), "info", "after restart", ""))
	if err := second.Close(); err != nil {
		t.Fatalf("close second: %v", err)
	}

	volumes := listRuntimeLogTestVolumes(t, dir)
	if len(volumes) != 1 || volumes[0].Name() != "2026-11-18-001.log" {
		t.Fatalf("volumes after restart = %v, want one 2026-11-18-001.log", volumeNames(volumes))
	}
	body, err := os.ReadFile(filepath.Join(dir, volumes[0].Name()))
	if err != nil {
		t.Fatalf("read volume: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "before restart") || !strings.Contains(lines[1], "after restart") {
		t.Fatalf("same-day volume was not appended: %q", string(body))
	}
}

// TestRuntimeLogStoreIndependentProcessRestart proves a real restart reads
// back lines written by a previous process, not a shared mock. The same test
// acts as the child writer when the environment names its directory, so no
// branch is skipped.
func TestRuntimeLogStoreIndependentProcessRestart(t *testing.T) {
	const childDirEnv = "BANSOKO_RUNTIME_LOG_CHILD_DIR"
	if dir := os.Getenv(childDirEnv); dir != "" {
		// Use the real clock: the parent reads this directory right away, and a
		// fixed date would fall outside retention when the suite is run later.
		now := time.Now().UTC()
		store := NewRuntimeLogStore(RuntimeLogStoreOptions{
			Dir: dir, MaxVolumeBytes: 512, MaxTotalBytes: 1 << 20, Now: func() time.Time { return now },
		})
		for i := 0; i < 6; i++ {
			writeRuntimeLogTestLine(t, store, runtimeLogTestLine(now.Add(time.Duration(i)*time.Second), "info", fmt.Sprintf("child-line-%d", i), fmt.Sprintf("wi-%d", i)))
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close child store: %v", err)
		}
		return
	}

	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	command := exec.Command(os.Args[0], "-test.run=^TestRuntimeLogStoreIndependentProcessRestart$")
	command.Env = append(os.Environ(), childDirEnv+"="+dir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("child writer failed: %v\n%s", err, output)
	}

	logger := NewPersistentRuntimeLogger(io.Discard, dir)
	t.Cleanup(func() { _ = logger.Close() })
	page := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 100})
	if page.Source != RuntimeLogSourceFile {
		t.Fatalf("source = %q, want file", page.Source)
	}
	messages := runtimeLogTestMessages(page)
	want := []string{"child-line-0", "child-line-1", "child-line-2", "child-line-3", "child-line-4", "child-line-5"}
	if strings.Join(messages, ",") != strings.Join(want, ",") {
		t.Fatalf("messages after independent restart = %v, want %v", messages, want)
	}
	if len(listRuntimeLogTestVolumes(t, dir)) < 2 {
		t.Fatalf("expected the child to rotate across volumes")
	}
}

func TestRuntimeLogStoreFiltersBeforeTheLimitAcrossVolumes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, MaxVolumeBytes: 300, MaxTotalBytes: 1 << 20, Now: func() time.Time { return now },
	})
	logger := NewTeedRuntimeLoggerWithStore(io.Discard, store)
	t.Cleanup(func() { _ = logger.Close() })

	writeRuntimeLogTestLine(t, store, runtimeLogTestLine(now, "info", "ordinary filler", ""))
	writeRuntimeLogTestLine(t, store, runtimeLogTestLine(now.Add(time.Second), "info", "the needle transition", "wi-target"))
	writeRuntimeLogTestLine(t, store, runtimeLogTestLine(now.Add(2*time.Second), "error", "unrelated failure", "wi-other"))
	for i := 0; i < 20; i++ {
		writeRuntimeLogTestLine(t, store, runtimeLogTestLine(now.Add(time.Duration(i+3)*time.Second), "info", fmt.Sprintf("newer filler %d", i), ""))
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	page := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Query: "needle", Limit: 1})
	if len(page.Entries) != 1 || page.Entries[0].Message != "the needle transition" {
		t.Fatalf("filtered page = %+v", runtimeLogTestMessages(page))
	}
	exact := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, WorkItemID: "wi-target", Limit: 10})
	if len(exact.Entries) != 1 || exact.Entries[0].WorkItemID != "wi-target" {
		t.Fatalf("work item exact page = %+v", exact.Entries)
	}
	errorsOnly := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogError, Limit: 10})
	if len(errorsOnly.Entries) != 1 || errorsOnly.Entries[0].Message != "unrelated failure" {
		t.Fatalf("error page = %+v", runtimeLogTestMessages(errorsOnly))
	}
	// A substring in q must not satisfy the exact work item filter.
	none := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Query: "wi", WorkItemID: "wi-target", Limit: 10})
	if len(none.Entries) != 1 {
		t.Fatalf("q and exact work item must stack, got %+v", runtimeLogTestMessages(none))
	}
}

func TestRuntimeLogStorePaginationHasNoMissingOrDuplicateLines(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	now := time.Date(2026, 11, 18, 11, 0, 0, 0, time.UTC)
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, MaxVolumeBytes: 420, MaxTotalBytes: 1 << 20, Now: func() time.Time { return now },
	})
	logger := NewTeedRuntimeLoggerWithStore(io.Discard, store)
	t.Cleanup(func() { _ = logger.Close() })

	const total = 40
	for i := 0; i < total; i++ {
		writeRuntimeLogTestLine(t, store, runtimeLogTestLine(now.Add(time.Duration(i)*time.Second), "info", fmt.Sprintf("line-%02d", i), ""))
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	page := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 7})
	var collected []string
	for {
		collected = append(runtimeLogTestMessages(page), collected...)
		if page.NextBefore == "" {
			break
		}
		page = readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 7, Before: page.NextBefore})
	}
	if len(collected) != total {
		t.Fatalf("backward paging collected %d lines, want %d: %v", len(collected), total, collected)
	}
	for i, message := range collected {
		if want := fmt.Sprintf("line-%02d", i); message != want {
			t.Fatalf("line %d = %q, want %q", i, message, want)
		}
	}

	// The newest cursor is the incremental watermark. Lines written after it,
	// including a rotation, are returned exactly once.
	latest := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 3})
	if latest.NextAfter == "" {
		t.Fatal("latest page has no next_after watermark")
	}
	for i := total; i < total+9; i++ {
		writeRuntimeLogTestLine(t, store, runtimeLogTestLine(now.Add(time.Duration(i)*time.Second), "info", fmt.Sprintf("line-%02d", i), ""))
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var got []string
	seen := map[string]bool{}
	for cursor := latest.NextAfter; cursor != "" && !seen[cursor]; {
		seen[cursor] = true
		incremental := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 100, After: cursor})
		got = append(got, runtimeLogTestMessages(incremental)...)
		cursor = incremental.NextAfter
	}
	if len(got) != 9 {
		t.Fatalf("incremental lines = %v, want 9", got)
	}
	for i, message := range got {
		if want := fmt.Sprintf("line-%02d", total+i); message != want {
			t.Fatalf("incremental line %d = %q, want %q", i, message, want)
		}
	}
}

func TestRuntimeLogStoreMarksAGapForAnEvictedCursor(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, MaxVolumeBytes: 300, MaxTotalBytes: 1 << 20, Now: func() time.Time { return now },
	})
	logger := NewTeedRuntimeLoggerWithStore(io.Discard, store)
	t.Cleanup(func() { _ = logger.Close() })
	writeRuntimeLogTestLine(t, store, runtimeLogTestLine(now, "info", "retained line", ""))
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	oldCursor := encodeRuntimeLogFileCursor("2000-01-01-001.log", 0)
	page := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 10, After: oldCursor})
	if !page.Gap || page.Source != RuntimeLogSourceFile {
		t.Fatalf("evicted cursor page = %+v, want gap from file", page)
	}
	if len(page.Entries) == 0 || page.Entries[len(page.Entries)-1].Message != "retained line" {
		t.Fatalf("evicted cursor did not start at the oldest retained line: %+v", page.Entries)
	}
}

func TestRuntimeLogStoreEvictsOldestClosedVolumesAtTheTotalBound(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	now := time.Date(2026, 11, 18, 13, 0, 0, 0, time.UTC)
	line := runtimeLogTestLine(now, "info", strings.Repeat("x", 60), "")
	volumeBudget := int64(len(line)+1) * 2 // two lines per volume
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, MaxVolumeBytes: volumeBudget, MaxTotalBytes: volumeBudget * 2,
		Retention: time.Hour, Now: func() time.Time { return now },
	})
	t.Cleanup(func() { _ = store.Close() })
	for i := 0; i < 12; i++ {
		writeRuntimeLogTestLine(t, store, line)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	volumes := listRuntimeLogTestVolumes(t, dir)
	if len(volumes) > 2 {
		t.Fatalf("retained %d volumes, want at most 2: %v", len(volumes), volumeNames(volumes))
	}
	if len(volumes) == 0 || volumes[0].Name() == "2026-11-18-001.log" {
		t.Fatalf("oldest volume was not evicted: %v", volumeNames(volumes))
	}
	// Reopen with the fixture clock as well: wall time would eventually age the
	// fixed-date volumes past the retention window and erase what this reads.
	restarted := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, MaxVolumeBytes: volumeBudget, MaxTotalBytes: volumeBudget * 2,
		Retention: time.Hour, Now: func() time.Time { return now },
	})
	t.Cleanup(func() { _ = restarted.Close() })
	logger := NewTeedRuntimeLoggerWithStore(io.Discard, restarted)
	t.Cleanup(func() { _ = logger.Close() })
	page := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 100})
	if len(page.Entries) == 0 {
		t.Fatal("history is empty after eviction")
	}
}

func TestRuntimeLogStoreEvictsVolumesOlderThanRetention(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	first := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, MaxVolumeBytes: 1 << 20, Retention: 7 * 24 * time.Hour, Now: func() time.Time { return first },
	})
	writeRuntimeLogTestLine(t, store, runtimeLogTestLine(first, "info", "expired", ""))
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	later := first.Add(8 * 24 * time.Hour)
	restarted := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, MaxVolumeBytes: 1 << 20, Retention: 7 * 24 * time.Hour, Now: func() time.Time { return later },
	})
	t.Cleanup(func() { _ = restarted.Close() })
	writeRuntimeLogTestLine(t, restarted, runtimeLogTestLine(later, "info", "current", ""))
	if err := restarted.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	volumes := listRuntimeLogTestVolumes(t, dir)
	for _, volume := range volumes {
		if strings.HasPrefix(volume.Name(), "2026-11-10") {
			t.Fatalf("expired volume retained: %v", volumeNames(volumes))
		}
	}
	if len(volumes) != 1 || volumes[0].Name() != "2026-11-18-001.log" {
		t.Fatalf("volumes = %v, want only the current day", volumeNames(volumes))
	}
}

func TestRuntimeLogStoreSilentlyRemovesTheLastExpiredVolume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	expired := filepath.Join(dir, "2026-10-01-001.log")
	if err := os.WriteFile(expired, []byte(`{"timestamp":"2026-10-01T08:00:00Z","level":"info","message":"old"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write expired volume: %v", err)
	}
	later := time.Date(2026, 10, 10, 8, 0, 0, 0, time.UTC)
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, Retention: 7 * 24 * time.Hour, Now: func() time.Time { return later },
	})
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if _, err := os.Stat(expired); !os.IsNotExist(err) {
		t.Fatalf("the last expired volume was retained: %v", err)
	}
}

func TestRuntimeLogStoreNeverTreatsANonRegularNameAsAVolume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	if err := os.MkdirAll(filepath.Join(dir, "2026-11-18-002.log"), 0o700); err != nil {
		t.Fatalf("mkdir volume-named directory: %v", err)
	}
	target := filepath.Join(t.TempDir(), "target.log")
	if err := os.WriteFile(target, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, "2026-11-18-001.log")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	now := time.Date(2026, 11, 18, 18, 0, 0, 0, time.UTC)
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{Dir: dir, Now: func() time.Time { return now }})
	t.Cleanup(func() { _ = store.Close() })
	page, ok, err := store.Read(RuntimeLogQuery{Level: RuntimeLogAll, Limit: 10})
	if err != nil || !ok {
		t.Fatalf("read = ok:%v err:%v, want the file source to answer", ok, err)
	}
	if len(page.Entries) != 0 || page.NextAfter == "" {
		t.Fatalf("non-regular names became history: %+v", page)
	}

	// Both scan directions refuse a path that is not a regular file, even if a
	// later caller bypasses the listing.
	visit := func(int64, []byte) bool { return true }
	if err := scanRuntimeLogFileForward(link, 0, visit); err == nil {
		t.Fatal("forward scan followed a symlink")
	}
	if err := scanRuntimeLogFileBackward(filepath.Join(dir, "2026-11-18-002.log"), 0, visit); err == nil {
		t.Fatal("backward scan read a directory")
	}

	// The writer also refuses to open the volume-named symlink or directory.
	logger := NewTeedRuntimeLoggerWithStore(io.Discard, store)
	t.Cleanup(func() { _ = logger.Close() })
	logger.Info(t.Context(), runtimeLogTestEvent("must not follow the link"))
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(body) != "outside\n" {
		t.Fatalf("writer followed the volume-named symlink: %q", body)
	}
}

func TestRuntimeLogStoreDegradesToMemoryAndRecovers(t *testing.T) {
	blocking := filepath.Join(t.TempDir(), RuntimeLogDirName)
	if err := os.WriteFile(blocking, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{Dir: blocking})
	logger := NewTeedRuntimeLoggerWithStore(io.Discard, store)
	t.Cleanup(func() { _ = logger.Close() })

	logger.Info(t.Context(), runtimeLogTestEvent("while unwritable"))
	page := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 10})
	if page.Source != RuntimeLogSourceMemory {
		t.Fatalf("source = %q, want memory while the directory is blocked", page.Source)
	}
	if len(page.Entries) != 1 || page.Entries[0].Message != "while unwritable" {
		t.Fatalf("memory fallback = %+v", runtimeLogTestMessages(page))
	}
	if strings.Contains(fmt.Sprintf("%+v", page), blocking) {
		t.Fatalf("response leaked the filesystem path: %+v", page)
	}

	if err := os.Remove(blocking); err != nil {
		t.Fatalf("remove blocker: %v", err)
	}
	logger.Info(t.Context(), runtimeLogTestEvent("after recovery"))
	page = readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 10})
	if page.Source != RuntimeLogSourceFile {
		t.Fatalf("source = %q, want file after recovery", page.Source)
	}
	found := false
	for _, entry := range page.Entries {
		if entry.Message == "after recovery" {
			found = true
		}
	}
	if !found {
		t.Fatalf("recovered store did not persist the new line: %+v", runtimeLogTestMessages(page))
	}
}

func TestRuntimeLogStoreBackwardScanAcrossReadChunks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	now := time.Date(2026, 11, 18, 17, 0, 0, 0, time.UTC)
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, MaxVolumeBytes: 1 << 20, Now: func() time.Time { return now },
	})
	logger := NewTeedRuntimeLoggerWithStore(io.Discard, store)
	t.Cleanup(func() { _ = logger.Close() })
	long := runtimeLogTestLine(now, "info", strings.Repeat("y", 70<<10), "")
	writeRuntimeLogTestLine(t, store, long)
	writeRuntimeLogTestLine(t, store, runtimeLogTestLine(now.Add(time.Second), "info", "after the long line", ""))
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	page := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 10})
	if len(page.Entries) != 2 || page.Entries[1].Message != "after the long line" || !strings.HasPrefix(page.Entries[0].Message, "yyy") {
		t.Fatalf("chunked backward scan = %+v", runtimeLogTestMessages(page))
	}
	older := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 1})
	if len(older.Entries) != 1 || older.Entries[0].Message != "after the long line" {
		t.Fatalf("limit 1 page = %d entries", len(older.Entries))
	}
	older = readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 1, Before: older.NextBefore})
	if len(older.Entries) != 1 || !strings.HasPrefix(older.Entries[0].Message, "yyy") {
		t.Fatalf("before page across chunks = %+v", runtimeLogTestMessages(older))
	}
}

func TestRuntimeLogStoreDropsAnOversizedWholeLine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	now := time.Date(2026, 11, 18, 14, 0, 0, 0, time.UTC)
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{
		Dir: dir, MaxVolumeBytes: 128, Now: func() time.Time { return now },
	})
	logger := NewTeedRuntimeLoggerWithStore(io.Discard, store)
	t.Cleanup(func() { _ = logger.Close() })
	if _, err := store.Write([]byte(strings.Repeat("x", 200) + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got := store.Dropped(); got == 0 {
		t.Fatal("oversized line was not reported as dropped")
	}
	page := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 10})
	if len(page.Entries) != 0 {
		t.Fatalf("oversized line reached the file: %+v", page.Entries)
	}
}

func TestRuntimeLogStoreClosesAPartialLineLeftByACrash(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	now := time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "2026-11-18-001.log")
	if err := os.WriteFile(path, []byte(`{"timestamp":"partial"`), 0o600); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{Dir: dir, Now: func() time.Time { return now }})
	logger := NewTeedRuntimeLoggerWithStore(io.Discard, store)
	t.Cleanup(func() { _ = logger.Close() })
	logger.Info(t.Context(), runtimeLogTestEvent("complete line"))
	page := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 10})
	if len(page.Entries) != 1 || page.Entries[0].Message != "complete line" {
		t.Fatalf("partial line leaked into the projection: %+v", page.Entries)
	}
}

func TestRuntimeLogStoreRejectsMalformedCursors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), RuntimeLogDirName)
	store := NewRuntimeLogStore(RuntimeLogStoreOptions{Dir: dir})
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	for _, cursor := range []string{"not-a-cursor", encodeRuntimeLogFileCursor("../../etc/passwd", 1), encodeRuntimeLogFileCursor("2026-11-18-001.log", -1)} {
		if _, ok, err := store.Read(RuntimeLogQuery{After: cursor, Limit: 1}); ok || !errors.Is(err, ErrRuntimeLogCursorInvalid) {
			t.Fatalf("cursor %q = ok:%v err:%v, want invalid", cursor, ok, err)
		}
	}
	if _, ok, err := store.Read(RuntimeLogQuery{Before: "x", After: "y"}); ok || !errors.Is(err, ErrRuntimeLogCursorInvalid) {
		t.Fatalf("before+after = ok:%v err:%v, want invalid", ok, err)
	}
}

func TestRuntimeLogTailQueryPagesTheMemoryFallback(t *testing.T) {
	logger := NewTeedRuntimeLogger(io.Discard)
	for i := 0; i < 5; i++ {
		_, _ = logger.Tail().Write([]byte(`{"timestamp":"2026-11-18T16:00:0` + fmt.Sprint(i) + `Z","level":"info","message":"mem-` + fmt.Sprint(i) + `"}` + "\n"))
	}
	first := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 2})
	if first.Source != RuntimeLogSourceMemory || len(first.Entries) != 2 || first.Entries[0].Message != "mem-3" {
		t.Fatalf("first memory page = %+v", first)
	}
	older := readRuntimeLogTestPage(t, logger, RuntimeLogQuery{Level: RuntimeLogAll, Limit: 2, Before: first.NextBefore})
	if len(older.Entries) != 2 || older.Entries[0].Message != "mem-1" {
		t.Fatalf("older memory page = %+v", runtimeLogTestMessages(older))
	}
	if _, err := logger.RuntimeLogs(RuntimeLogQuery{Before: "bad", Limit: 1}); !errors.Is(err, ErrRuntimeLogCursorInvalid) {
		t.Fatalf("bad memory cursor error = %v", err)
	}
}

func volumeNames(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
