package application

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The runtime log history is the durable copy of what the runtime logger wrote
// to stdout. It is an operator diagnostic, never product state:
//
//   - volumes live under <ConfigDir>/logs and are named YYYY-MM-DD-NNN.log in
//     UTC, where NNN is the zero-padded sequence of that day;
//   - a whole line is never split across volumes; when the next line would
//     cross the volume bound the writer opens the next volume;
//   - the retained total is bounded (~30 MiB) and old closed volumes are
//     evicted oldest first, also after 7 days;
//   - reads only project the allowlisted fields of a line. Nothing here ever
//     carries a filesystem path, a raw error, or a credential into a response.
const (
	// RuntimeLogDirName is the directory under the configuration root.
	RuntimeLogDirName = "logs"
	// RuntimeLogVolumeMaxBytes is the bound of one volume.
	RuntimeLogVolumeMaxBytes int64 = 10 << 20
	// RuntimeLogTotalMaxBytes is the bound of the whole retained history.
	RuntimeLogTotalMaxBytes int64 = 30 << 20
	// RuntimeLogRetention is the longest a closed volume is kept.
	RuntimeLogRetention = 7 * 24 * time.Hour

	// RuntimeLogSourceFile means the page came from the on-disk history.
	RuntimeLogSourceFile = "file"
	// RuntimeLogSourceMemory means the on-disk history was unavailable and the
	// bounded in-memory tail answered instead.
	RuntimeLogSourceMemory = "memory"

	runtimeLogFlushInterval = 100 * time.Millisecond
	runtimeLogPendingBytes  = 4 << 20
	runtimeLogReadChunk     = 64 << 10
	runtimeLogCursorVersion = "1"
)

// ErrRuntimeLogCursorInvalid reports an opaque cursor the server did not mint.
var ErrRuntimeLogCursorInvalid = errors.New("runtime log cursor is invalid")

var runtimeLogVolumeName = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2})-([0-9]{3})\.log$`)

// RuntimeLogQuery is one page request against either retained source.
type RuntimeLogQuery struct {
	Level      RuntimeLogLevel
	Query      string
	WorkItemID string
	Limit      int
	// Before asks for matching lines older than the cursor (backwards paging).
	Before string
	// After asks for matching lines newer than the cursor (incremental poll).
	After string
}

// RuntimeLogPage is a bounded page plus the cursors that continue it.
type RuntimeLogPage struct {
	Entries []RuntimeLogEntry
	// NextBefore continues with older lines; empty when there is no older
	// retained line to continue from.
	NextBefore string
	// NextAfter continues with newer lines and is also the incremental
	// watermark. It names the next unread byte, so it is present even when the
	// page is empty: a query with no matches still advances the watermark to
	// the end of what it scanned.
	NextAfter string
	// Gap says the supplied cursor is no longer retained (or belongs to the
	// other source), so lines between it and this page may be gone.
	Gap    bool
	Source string
}

// RuntimeLogStoreOptions configures one store. Zero values take the product
// bounds; tests use smaller bounds.
type RuntimeLogStoreOptions struct {
	Dir            string
	MaxVolumeBytes int64
	MaxTotalBytes  int64
	Retention      time.Duration
	Now            func() time.Time
}

// RuntimeLogStore writes complete JSON lines to bounded, rotating volumes and
// answers filtered pages from them. Writes are queued and flushed by one
// background goroutine, so a slow disk never stalls the caller; when the queue
// or the disk fails the bounded in-memory tail remains the read fallback.
type RuntimeLogStore struct {
	dir            string
	now            func() time.Time
	maxVolumeBytes int64
	maxTotalBytes  int64
	retention      time.Duration

	mu           sync.Mutex
	pending      [][]byte
	pendingBytes int64
	maxPending   int64
	degraded     bool
	closed       bool
	dropped      int64

	writeMu   sync.Mutex
	file      *os.File
	fileName  string
	fileSize  int64
	lastEvict time.Time

	wake chan struct{}
	done chan struct{}
	wg   sync.WaitGroup
}

// NewRuntimeLogStore builds a store and starts its flusher. A directory that
// cannot be prepared leaves the store degraded; it never fails construction, so
// startup is not held hostage by an unwritable logs directory.
func NewRuntimeLogStore(opts RuntimeLogStoreOptions) *RuntimeLogStore {
	if opts.MaxVolumeBytes <= 0 {
		opts.MaxVolumeBytes = RuntimeLogVolumeMaxBytes
	}
	if opts.MaxTotalBytes <= 0 {
		opts.MaxTotalBytes = RuntimeLogTotalMaxBytes
	}
	if opts.Retention <= 0 {
		opts.Retention = RuntimeLogRetention
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &RuntimeLogStore{
		dir:            opts.Dir,
		now:            opts.Now,
		maxVolumeBytes: opts.MaxVolumeBytes,
		maxTotalBytes:  opts.MaxTotalBytes,
		retention:      opts.Retention,
		maxPending:     runtimeLogPendingBytes,
		wake:           make(chan struct{}, 1),
		done:           make(chan struct{}),
	}
	s.writeMu.Lock()
	if err := s.prepareLocked(); err != nil {
		s.degraded = true
	}
	s.writeMu.Unlock()
	s.wg.Add(1)
	go s.run()
	return s
}

// Write queues one complete line without waiting for the disk. It never fails,
// so a logging tee cannot break the stdout writer next to it.
func (s *RuntimeLogStore) Write(p []byte) (int, error) {
	if s == nil {
		return len(p), nil
	}
	line := bytes.TrimRight(p, "\r\n")
	if len(line) == 0 {
		return len(p), nil
	}
	// A line that cannot fit a whole volume is not split; it is dropped.
	if int64(len(line))+1 > s.maxVolumeBytes {
		s.mu.Lock()
		s.dropped++
		s.mu.Unlock()
		return len(p), nil
	}
	line = append([]byte(nil), line...)
	s.mu.Lock()
	if s.closed || s.pendingBytes+int64(len(line))+1 > s.maxPending {
		s.dropped++
		s.mu.Unlock()
		return len(p), nil
	}
	s.pending = append(s.pending, line)
	s.pendingBytes += int64(len(line)) + 1
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return len(p), nil
}

// Flush writes every queued line now. It is used by reads so a page includes
// lines the caller just wrote, and by tests.
func (s *RuntimeLogStore) Flush() error {
	if s == nil {
		return nil
	}
	return s.flush()
}

// Close stops the flusher after writing what is queued.
func (s *RuntimeLogStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.done)
	s.wg.Wait()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.file != nil {
		err := s.file.Close()
		s.file = nil
		return err
	}
	return nil
}

// Dropped reports how many lines could not be queued or written. It exists for
// tests and never crosses the HTTP boundary.
func (s *RuntimeLogStore) Dropped() int64 {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

func (s *RuntimeLogStore) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(runtimeLogFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.wake:
			_ = s.flush()
		case <-ticker.C:
			_ = s.flush()
		case <-s.done:
			_ = s.flush()
			return
		}
	}
}

func (s *RuntimeLogStore) flush() error {
	// Serialize the queue swap with the write. A read-triggered flush must not
	// overtake (or return ahead of) a batch the background flusher just took.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	batch, ok := s.takePending()
	if !ok {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return nil
		}
		// A quiet process still expires old volumes; the directory is listed
		// at most once a minute, not on every flush tick.
		now := s.now()
		if !s.degraded && (s.lastEvict.IsZero() || now.Sub(s.lastEvict) >= time.Minute) {
			s.evictLocked(now)
		}
		return nil
	}
	if s.file == nil {
		if err := s.prepareLocked(); err != nil {
			s.degraded = true
			return err
		}
		if err := s.openActiveLocked(); err != nil {
			s.degraded = true
			return err
		}
		s.degraded = false
	}
	now := s.now()
	for _, line := range batch {
		if int64(len(line))+1 > s.maxVolumeBytes {
			s.mu.Lock()
			s.dropped++
			s.mu.Unlock()
			continue
		}
		if !strings.HasPrefix(s.fileName, now.UTC().Format("2006-01-02")+"-") || s.fileSize+int64(len(line))+1 > s.maxVolumeBytes {
			if err := s.rotateLocked(now); err != nil {
				s.failLocked()
				return err
			}
		}
		if err := s.writeLineLocked(line); err != nil {
			s.failLocked()
			return err
		}
	}
	s.evictLocked(now)
	return nil
}

// takePending swaps the queue; it reports false when there is nothing to write.
func (s *RuntimeLogStore) takePending() ([][]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return nil, false
	}
	batch := s.pending
	s.pending = nil
	s.pendingBytes = 0
	return batch, true
}

// prepareLocked makes the directory private and opens an appendable volume. It
// also retries after a degraded period, so an unwritable directory recovers as
// soon as the file system allows it again.
func (s *RuntimeLogStore) prepareLocked() error {
	if s.dir == "" {
		return errors.New("runtime log directory is not configured")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("runtime log directory is not a directory")
	}
	// Do not open a new volume until the first record actually arrives.
	return os.Chmod(s.dir, 0o700)
}

func (s *RuntimeLogStore) failLocked() {
	s.degraded = true
	if s.file != nil {
		_ = s.file.Close()
		s.file = nil
		s.fileName = ""
		s.fileSize = 0
	}
}

func (s *RuntimeLogStore) openActiveLocked() error {
	volumes, err := s.listVolumes()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	day := now.Format("2006-01-02")
	name := ""
	if len(volumes) == 0 {
		name = day + "-001.log"
	} else {
		last := volumes[len(volumes)-1]
		switch {
		case last.day == day && last.size < s.maxVolumeBytes:
			name = last.name
		case last.day == day:
			name = fmt.Sprintf("%s-%03d.log", day, last.seq+1)
		case last.day < day:
			name = day + "-001.log"
		default:
			// A forward clock jump leaves a future-named volume behind. The new
			// day still starts at 001; name ordering only affects which volume
			// reads first, never line integrity.
			name = day + "-001.log"
		}
	}
	return s.openVolumeLocked(name)
}

func (s *RuntimeLogStore) openVolumeLocked(name string) error {
	path := filepath.Join(s.dir, name)
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		// Symlinks, directories and devices are never log volumes, even if
		// their name looks like one.
		return errors.New("runtime log volume is not a regular file")
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	size := stat.Size()
	// A process killed mid-write can leave a line without its newline. Close
	// that line instead of appending the next record onto it.
	if size > 0 {
		last := make([]byte, 1)
		if _, err := file.ReadAt(last, size-1); err == nil && last[0] != '\n' {
			if _, err := file.Write([]byte("\n")); err != nil {
				_ = file.Close()
				return err
			}
			size++
		}
	}
	s.file = file
	s.fileName = name
	s.fileSize = size
	s.evictLocked(s.now())
	return nil
}

func (s *RuntimeLogStore) rotateLocked(now time.Time) error {
	previous := s.fileName
	if s.file != nil {
		if err := s.file.Close(); err != nil {
			s.file = nil
			return err
		}
		s.file = nil
	}
	if previous == "" {
		return s.openActiveLocked()
	}
	// Open the next sequence deliberately: a line that did not fit the current
	// volume must not be written there just because the volume is not yet at its
	// hard bound.
	today := now.UTC().Format("2006-01-02")
	next := today + "-001.log"
	if match := runtimeLogVolumeName.FindStringSubmatch(previous); match != nil && match[1] == today {
		seq, _ := strconv.Atoi(match[2])
		next = fmt.Sprintf("%s-%03d.log", today, seq+1)
	}
	return s.openVolumeLocked(next)
}

func (s *RuntimeLogStore) writeLineLocked(line []byte) error {
	data := make([]byte, 0, len(line)+1)
	data = append(data, line...)
	data = append(data, '\n')
	n, err := s.file.Write(data)
	s.fileSize += int64(n)
	if err != nil {
		return err
	}
	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func (s *RuntimeLogStore) evictLocked(now time.Time) {
	s.lastEvict = now
	volumes, err := s.listVolumes()
	if err != nil {
		return
	}
	total := int64(0)
	for _, volume := range volumes {
		total += volume.size
	}
	cutoff := now.UTC().Add(-s.retention)
	cutoffDay := cutoff.Format("2006-01-02")
	for len(volumes) > 0 {
		oldest := volumes[0]
		if oldest.name == s.fileName {
			return
		}
		expired := oldest.day < cutoffDay
		if total <= s.maxTotalBytes && !expired {
			return
		}
		// An expired volume is removed even when it is the last one: a quiet
		// process must not keep a stale log forever. A single volume that is
		// merely over the total bound is kept, because evicting it would leave
		// no history at all.
		if len(volumes) == 1 && !expired {
			return
		}
		if err := os.Remove(oldest.path); err != nil && !os.IsNotExist(err) {
			return
		}
		total -= oldest.size
		volumes = volumes[1:]
	}
}

type runtimeLogVolume struct {
	name string
	path string
	size int64
	day  string
	seq  int
}

func (s *RuntimeLogStore) listVolumes() ([]runtimeLogVolume, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	volumes := make([]runtimeLogVolume, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		match := runtimeLogVolumeName.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		seq, _ := strconv.Atoi(match[2])
		volumes = append(volumes, runtimeLogVolume{
			name: entry.Name(),
			path: filepath.Join(s.dir, entry.Name()),
			size: info.Size(),
			day:  match[1],
			seq:  seq,
		})
	}
	sort.Slice(volumes, func(i, j int) bool { return volumes[i].name < volumes[j].name })
	return volumes, nil
}

// Read answers one page from the retained volumes. The bool is false when the
// caller must fall back to the in-memory tail (degraded store or unavailable
// directory). A cursor minted for the tail is not an error: a healthy store
// answers from the files and marks the page as a gap, so recovery actually
// switches back to the disk instead of leaving the reader on memory forever.
// ErrRuntimeLogCursorInvalid is returned only for a cursor this server never
// minted.
func (s *RuntimeLogStore) Read(q RuntimeLogQuery) (RuntimeLogPage, bool, error) {
	if s == nil {
		return RuntimeLogPage{}, false, nil
	}
	if err := s.flush(); err != nil {
		return RuntimeLogPage{}, false, nil
	}
	s.writeMu.Lock()
	degraded := s.degraded
	s.writeMu.Unlock()
	if degraded {
		return RuntimeLogPage{}, false, nil
	}
	page, err := s.readFiles(q)
	if err != nil {
		if errors.Is(err, ErrRuntimeLogCursorInvalid) {
			return RuntimeLogPage{}, false, err
		}
		return RuntimeLogPage{}, false, nil
	}
	return page, true, nil
}

func (s *RuntimeLogStore) readFiles(q RuntimeLogQuery) (RuntimeLogPage, error) {
	if q.Before != "" && q.After != "" {
		return RuntimeLogPage{}, ErrRuntimeLogCursorInvalid
	}
	limit := normalizeRuntimeLogLimit(q.Limit)
	level := q.Level
	if level == "" {
		level = RuntimeLogAll
	}
	terms := runtimeLogSearchTerms(q.Query)

	page := RuntimeLogPage{Source: RuntimeLogSourceFile}
	var cursor runtimeLogCursor
	var err error
	cursorKind := ""
	if q.After != "" {
		cursor, err = decodeRuntimeLogCursor(q.After)
		if err != nil {
			return RuntimeLogPage{}, err
		}
		cursorKind = "after"
	} else if q.Before != "" {
		cursor, err = decodeRuntimeLogCursor(q.Before)
		if err != nil {
			return RuntimeLogPage{}, err
		}
		cursorKind = "before"
	}
	// A cursor minted by the memory tail cannot address the files. It is not a
	// client error: after the disk recovers, the store answers from the files
	// and the page carries a gap so the reader can reset its window.
	foreignCursor := cursorKind != "" && cursor.kind != "f"
	if foreignCursor {
		page.Gap = true
	}

	volumes, err := s.listVolumes()
	if err != nil {
		return RuntimeLogPage{}, err
	}
	if len(volumes) == 0 {
		if cursorKind != "" {
			page.Gap = true
		}
		page.NextAfter = s.emptyRuntimeLogBoundary()
		return page, nil
	}
	eof := runtimeLogBoundary{volume: volumes[len(volumes)-1].name, offset: volumes[len(volumes)-1].size}
	hits := make([]runtimeLogHit, 0, limit)

	switch cursorKind {
	case "after":
		start := 0
		startOffset := int64(0)
		scanEnd := runtimeLogBoundary{volume: volumes[0].name, offset: 0}
		if !foreignCursor {
			index := volumeIndex(volumes, cursor.file)
			if index < 0 {
				insert := sort.Search(len(volumes), func(i int) bool { return volumes[i].name >= cursor.file })
				page.Gap = true
				if insert >= len(volumes) {
					// The cursor names a volume newer than everything retained;
					// there is nothing after it to read.
					page.NextAfter = encodeRuntimeLogFileCursor(eof.volume, eof.offset)
					return page, nil
				}
				// Older than the oldest retained volume, or a hole between
				// volumes: start at the oldest retained line so nothing that is
				// still here is skipped.
				start = insert
				scanEnd = runtimeLogBoundary{volume: volumes[start].name, offset: 0}
			} else if cursor.offset > volumes[index].size {
				// The volume shrank under the cursor (external rewrite).
				page.Gap = true
				start = index
				scanEnd = runtimeLogBoundary{volume: volumes[index].name, offset: 0}
			} else {
				start = index
				startOffset = cursor.offset
				scanEnd = runtimeLogBoundary{volume: cursor.file, offset: cursor.offset}
			}
		}
		// The cursor names the next unread byte, not the last line already
		// read: the line starting exactly at the cursor is included. Scanning
		// advances the boundary line by line even when no line matches, which
		// makes a query with no matches pollable instead of re-reading the same
		// range.
		for i := start; i < len(volumes) && len(hits) < limit; i++ {
			volume := volumes[i]
			offset := int64(0)
			if i == start {
				offset = startOffset
			} else {
				scanEnd = runtimeLogBoundary{volume: volume.name, offset: 0}
			}
			err := scanRuntimeLogFileForward(volume.path, offset, func(lineOffset int64, line []byte) bool {
				scanEnd = runtimeLogBoundary{volume: volume.name, offset: lineOffset + int64(len(line)) + 1}
				entry, ok := parseRuntimeLogLine(string(line))
				if !ok || !runtimeLogEntryMatchesQuery(entry, level, terms, q.WorkItemID) {
					return true
				}
				hits = append(hits, runtimeLogHit{volume: volume.name, offset: lineOffset, entry: entry})
				return len(hits) < limit
			})
			if err != nil && !os.IsNotExist(err) {
				return RuntimeLogPage{}, err
			}
		}
		page.NextAfter = encodeRuntimeLogFileCursor(scanEnd.volume, scanEnd.offset)
	case "before":
		start := len(volumes) - 1
		endOffset := volumes[start].size
		afterBoundary := eof
		if !foreignCursor {
			index := volumeIndex(volumes, cursor.file)
			if index >= 0 && cursor.offset <= volumes[index].size {
				start = index
				endOffset = cursor.offset
				afterBoundary = runtimeLogBoundary{volume: cursor.file, offset: cursor.offset}
			} else {
				// An evicted, missing or rewritten cursor: reset to the newest
				// page instead of returning a misleading slice.
				page.Gap = true
			}
		}
		for i := start; i >= 0 && len(hits) < limit; i-- {
			volume := volumes[i]
			end := volume.size
			if i == start {
				end = endOffset
			}
			err := scanRuntimeLogFileBackward(volume.path, end, func(offset int64, line []byte) bool {
				entry, ok := parseRuntimeLogLine(string(line))
				if !ok || !runtimeLogEntryMatchesQuery(entry, level, terms, q.WorkItemID) {
					return true
				}
				hits = append(hits, runtimeLogHit{volume: volume.name, offset: offset, entry: entry})
				return len(hits) < limit
			})
			if err != nil && !os.IsNotExist(err) {
				return RuntimeLogPage{}, err
			}
		}
		reverseRuntimeLogHits(hits)
		page.NextAfter = encodeRuntimeLogFileCursor(afterBoundary.volume, afterBoundary.offset)
	default:
		for i := len(volumes) - 1; i >= 0 && len(hits) < limit; i-- {
			volume := volumes[i]
			err := scanRuntimeLogFileBackward(volume.path, volume.size, func(offset int64, line []byte) bool {
				entry, ok := parseRuntimeLogLine(string(line))
				if !ok || !runtimeLogEntryMatchesQuery(entry, level, terms, q.WorkItemID) {
					return true
				}
				hits = append(hits, runtimeLogHit{volume: volume.name, offset: offset, entry: entry})
				return len(hits) < limit
			})
			if err != nil && !os.IsNotExist(err) {
				return RuntimeLogPage{}, err
			}
		}
		reverseRuntimeLogHits(hits)
		page.NextAfter = encodeRuntimeLogFileCursor(eof.volume, eof.offset)
	}

	page.Entries = make([]RuntimeLogEntry, 0, len(hits))
	for _, hit := range hits {
		page.Entries = append(page.Entries, hit.entry)
	}
	if len(hits) > 0 {
		page.NextBefore = encodeRuntimeLogFileCursor(hits[0].volume, hits[0].offset)
	}
	return page, nil
}

// emptyRuntimeLogBoundary is the next unread byte for a store with no retained
// volume yet. It names the volume the first record of the current UTC day will
// use, so a query with no matches can still poll incrementally.
func (s *RuntimeLogStore) emptyRuntimeLogBoundary() string {
	name := s.now().UTC().Format("2006-01-02") + "-001.log"
	return encodeRuntimeLogFileCursor(name, 0)
}

type runtimeLogBoundary struct {
	volume string
	offset int64
}

type runtimeLogHit struct {
	volume string
	offset int64
	entry  RuntimeLogEntry
}

func reverseRuntimeLogHits(hits []runtimeLogHit) {
	for i, j := 0, len(hits)-1; i < j; i, j = i+1, j-1 {
		hits[i], hits[j] = hits[j], hits[i]
	}
}

func volumeIndex(volumes []runtimeLogVolume, name string) int {
	for i, volume := range volumes {
		if volume.name == name {
			return i
		}
	}
	return -1
}

func normalizeRuntimeLogLimit(limit int) int {
	if limit <= 0 || limit > RuntimeLogMaxLimit {
		return RuntimeLogMaxLimit
	}
	return limit
}

func runtimeLogEntryMatchesQuery(entry RuntimeLogEntry, level RuntimeLogLevel, terms []string, workItemID string) bool {
	if !runtimeLevelAllows(level, entry.Level) {
		return false
	}
	if workItemID != "" && entry.WorkItemID != workItemID {
		return false
	}
	return runtimeLogEntryMatches(entry, terms)
}

type runtimeLogCursor struct {
	kind   string
	file   string
	offset int64
	// seq is the stable sequence number of a memory-tail cursor. It names the
	// next unread sequence (a boundary) or, when hash is non-zero, the first
	// line of a backwards page. It never depends on a slice position. epoch
	// identifies the tail instance so a cursor from another process is a gap.
	epoch string
	seq   uint64
	hash  uint64
}

func encodeRuntimeLogFileCursor(file string, offset int64) string {
	payload := strings.Join([]string{runtimeLogCursorVersion, "f", file, strconv.FormatInt(offset, 10)}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func encodeRuntimeLogMemoryCursor(epoch string, seq, hash uint64) string {
	payload := strings.Join([]string{runtimeLogCursorVersion, "m", epoch, strconv.FormatUint(seq, 10), strconv.FormatUint(hash, 16)}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func encodeRuntimeLogMemoryLineCursor(epoch string, seq uint64, line string) string {
	return encodeRuntimeLogMemoryCursor(epoch, seq, runtimeLogLineHash(line))
}

func encodeRuntimeLogMemoryBoundary(epoch string, seq uint64) string {
	return encodeRuntimeLogMemoryCursor(epoch, seq, 0)
}

func decodeRuntimeLogCursor(value string) (runtimeLogCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return runtimeLogCursor{}, ErrRuntimeLogCursorInvalid
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) < 4 || parts[0] != runtimeLogCursorVersion {
		return runtimeLogCursor{}, ErrRuntimeLogCursorInvalid
	}
	switch parts[1] {
	case "f":
		if len(parts) != 4 {
			return runtimeLogCursor{}, ErrRuntimeLogCursorInvalid
		}
		if !runtimeLogVolumeName.MatchString(parts[2]) {
			return runtimeLogCursor{}, ErrRuntimeLogCursorInvalid
		}
		offset, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil || offset < 0 {
			return runtimeLogCursor{}, ErrRuntimeLogCursorInvalid
		}
		return runtimeLogCursor{kind: "f", file: parts[2], offset: offset}, nil
	case "m":
		if len(parts) != 5 || parts[2] == "" {
			return runtimeLogCursor{}, ErrRuntimeLogCursorInvalid
		}
		seq, err := strconv.ParseUint(parts[3], 10, 64)
		if err != nil || seq == 0 {
			return runtimeLogCursor{}, ErrRuntimeLogCursorInvalid
		}
		hash, err := strconv.ParseUint(parts[4], 16, 64)
		if err != nil {
			return runtimeLogCursor{}, ErrRuntimeLogCursorInvalid
		}
		return runtimeLogCursor{kind: "m", epoch: parts[2], seq: seq, hash: hash}, nil
	default:
		return runtimeLogCursor{}, ErrRuntimeLogCursorInvalid
	}
}

func runtimeLogLineHash(line string) uint64 {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(line))
	return hasher.Sum64()
}

// scanRuntimeLogFileBackward visits complete lines in reverse order. It reads
// fixed-size chunks from the end and stops as soon as visit returns false, so a
// normal newest-page read never walks the whole volume.
func scanRuntimeLogFileBackward(path string, end int64, visit func(offset int64, line []byte) bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("runtime log volume is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if end <= 0 {
		return nil
	}
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if end > stat.Size() {
		end = stat.Size()
	}
	var carry []byte
	pos := end
	for pos > 0 {
		start := pos - runtimeLogReadChunk
		if start < 0 {
			start = 0
		}
		buf := make([]byte, pos-start)
		if _, err := file.ReadAt(buf, start); err != nil && err != io.EOF {
			return err
		}
		data := buf
		if len(carry) > 0 {
			data = append(append(make([]byte, 0, len(buf)+len(carry)), buf...), carry...)
		}
		last := bytes.LastIndexByte(data, '\n')
		if last < 0 {
			carry = data
			pos = start
			continue
		}
		// Bytes after the last newline are an unterminated line; ignore them
		// until a later flush completes it.
		lineEnd := last
		for lineEnd >= 0 {
			prev := bytes.LastIndexByte(data[:lineEnd], '\n')
			if prev < 0 {
				if start == 0 {
					if line := data[:lineEnd]; len(line) > 0 {
						if !visit(0, line) {
							return nil
						}
					}
					return nil
				}
				// The line's start is further left; carry the suffix and its
				// terminating newline so the combined line ends where it should.
				carry = append([]byte(nil), data[:lineEnd+1]...)
				break
			}
			lineStart := prev + 1
			if lineEnd > lineStart {
				if !visit(start+int64(lineStart), data[lineStart:lineEnd]) {
					return nil
				}
			}
			lineEnd = prev
		}
		pos = start
	}
	return nil
}

// scanRuntimeLogFileForward visits complete lines in order from start. It stops
// when visit returns false.
func scanRuntimeLogFileForward(path string, start int64, visit func(offset int64, line []byte) bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("runtime log volume is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if start > 0 {
		if _, err := file.Seek(start, io.SeekStart); err != nil {
			return err
		}
	}
	reader := bufio.NewReaderSize(file, runtimeLogReadChunk)
	offset := start
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			complete := line[len(line)-1] == '\n'
			if complete {
				line = line[:len(line)-1]
			}
			if complete {
				if !visit(offset, line) {
					return nil
				}
				offset += int64(len(line)) + 1
			} else if err != nil {
				// A trailing partial line is not a record.
				return nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
