package bootstrap

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hardeningdomain "bansoko/internal/hardening/domain"
	updateapplication "bansoko/internal/update/application"
	updatefence "bansoko/internal/update/fence"
	embeddedupdate "bansoko/internal/update/journal"
)

// The marker path is the only trust root for an update that has not yet been
// acknowledged by the database: these tests drive consumePendingMarker with the
// real signed release tree, the real journal and the real filesystem, and script
// only the two PostgreSQL effects (the fence store and the verified dump).

const (
	markerPreviousRelease = "rel-marker-a"
	markerTargetRelease   = "rel-marker-b"
	// markerTestDSN is never dialed: the fence is scripted, and the production
	// sql.Open("pgx", dsn) above it is lazy.
	markerTestDSN = "postgres://owner@127.0.0.1:1/bansoko"
)

// fenceIdentity is the exact identity every marker fence call must carry.
type fenceIdentity struct {
	operationID    string
	admittedBy     string
	sequence       int64
	manifestDigest string
}

// fenceClose records one terminal-state request together with the row state it
// was requested against.
type fenceClose struct {
	fenceIdentity
	terminalState string
	released      bool
}

// recordingMarkerFence is a scripted markerFence holding one durable row. Its
// Acquire and ReconcileExactCurrentState refuse any identity that is not exactly
// the scripted row, so a production call with the wrong owner, digest, sequence
// or released state surfaces as an error instead of closing silently.
type recordingMarkerFence struct {
	row      updatefence.Row
	rowFound bool

	acquireErr error
	lookupErr  error
	listErr    error
	closeErr   error

	acquires []fenceIdentity
	closes   []fenceClose
	lookups  int
	lists    int
}

func (f *recordingMarkerFence) Lookup(_ context.Context, operationID string) (updatefence.Row, bool, error) {
	f.lookups++
	if f.lookupErr != nil {
		return updatefence.Row{}, false, f.lookupErr
	}
	if !f.rowFound || f.row.OperationID != operationID {
		return updatefence.Row{}, false, nil
	}
	return f.row, true, nil
}

func (f *recordingMarkerFence) Acquire(_ context.Context, operationID, admittedBy string, sequence int64, manifestDigest string) error {
	f.acquires = append(f.acquires, fenceIdentity{operationID, admittedBy, sequence, manifestDigest})
	if f.acquireErr != nil {
		return f.acquireErr
	}
	if f.rowFound {
		return updatefence.ErrStateConflict
	}
	f.row = updatefence.Row{OperationID: operationID, AdmittedBy: admittedBy, Sequence: sequence, ManifestDigest: manifestDigest, State: "active"}
	f.rowFound = true
	return nil
}

func (f *recordingMarkerFence) ListOpen(_ context.Context, admittedBy string) ([]updatefence.OpenFenceRow, error) {
	f.lists++
	if f.listErr != nil {
		return nil, f.listErr
	}
	if !f.rowFound || f.row.Released || f.row.AdmittedBy != admittedBy {
		return nil, nil
	}
	return []updatefence.OpenFenceRow{{
		OperationID:    f.row.OperationID,
		AdmittedBy:     f.row.AdmittedBy,
		Sequence:       f.row.Sequence,
		ManifestDigest: f.row.ManifestDigest,
		State:          f.row.State,
	}}, nil
}

func (f *recordingMarkerFence) ReconcileExactCurrentState(_ context.Context, operationID, admittedBy, manifestDigest string, expectedSequence int64, terminalState string) error {
	f.closes = append(f.closes, fenceClose{
		fenceIdentity: fenceIdentity{operationID, admittedBy, expectedSequence, manifestDigest},
		terminalState: terminalState,
		released:      f.row.Released,
	})
	if f.closeErr != nil {
		return f.closeErr
	}
	exact := f.rowFound && !f.row.Released && f.row.OperationID == operationID && f.row.AdmittedBy == admittedBy &&
		f.row.ManifestDigest == manifestDigest && f.row.Sequence == expectedSequence
	if !exact {
		return updatefence.ErrStateConflict
	}
	f.row.Released = true
	f.row.State = terminalState
	return nil
}

// calls reports every fence interaction in one number so a refusal can be
// asserted to have touched nothing.
func (f *recordingMarkerFence) calls() int {
	return len(f.acquires) + len(f.closes) + f.lookups + f.lists
}

// recordingMarkerBackup scripts the verified dump. A successful run publishes
// the real dump plus its sha256 sidecar, so the artifact the post-handoff
// consumer verifies is a genuine one, and it records whether the handoff
// journal already existed when the backup ran.
type recordingMarkerBackup struct {
	err           error
	backupID      string
	calls         int
	journalBefore bool
}

func (b *recordingMarkerBackup) write(updatesRoot string, marker pendingMarker, _ string, _ *sql.DB) (string, error) {
	b.calls++
	if _, found, err := embeddedupdate.Snapshot(filepath.Join(updatesRoot, journalFileName)); err != nil {
		return "", err
	} else if found {
		b.journalBefore = true
	}
	if b.err != nil {
		return "", b.err
	}
	backupID := b.backupID
	if backupID == "" {
		backupID = "embedded-" + marker.OperationID
	}
	if err := writeVerifiedMarkerBackupArtifact(updatesRoot, backupID); err != nil {
		return "", err
	}
	return backupID, nil
}

// installMarkerSeam scripts the marker path's PostgreSQL effects for one test.
// The production defaults are restored afterwards; nothing else writes them.
func installMarkerSeam(t *testing.T, fence *recordingMarkerFence, backup *recordingMarkerBackup) {
	t.Helper()
	previousFence, previousBackup := openMarkerFence, writeMarkerBackup
	openMarkerFence = func(*sql.DB) markerFence { return fence }
	writeMarkerBackup = backup.write
	t.Cleanup(func() { openMarkerFence, writeMarkerBackup = previousFence, previousBackup })
}

// markerRoot materializes a deployment mid-update: the release that is serving,
// the release the marker names, and the pointer that names the serving one.
func markerRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeReleaseTree(t, root, markerPreviousRelease)
	writeReleaseTree(t, root, markerTargetRelease)
	writePointer(t, root, markerPreviousRelease)
	return root
}

// markerForRelease derives the durable handoff identity from the signed tree
// itself, exactly as the application does after it staged the release.
func markerForRelease(t *testing.T, root, releaseID, previousReleaseID, operationID string) pendingMarker {
	t.Helper()
	releaseDir := writeReleaseTree(t, root, releaseID)
	raw, err := os.ReadFile(filepath.Join(releaseDir, updateapplication.SignedManifestEntryPath))
	if err != nil {
		t.Fatal(err)
	}
	var manifest hardeningdomain.ReleaseManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	digest, err := manifest.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	return pendingMarker{
		OperationID:       operationID,
		ReleaseID:         releaseID,
		ManifestDigest:    digest,
		ReleaseSequence:   manifest.ReleaseSequence,
		PreviousReleaseID: previousReleaseID,
	}
}

func writePendingMarker(t *testing.T, root string, marker pendingMarker) string {
	t.Helper()
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, pendingMarkerName)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeVerifiedMarkerBackupArtifact publishes backups/<id>.dump with a matching
// sha256 sidecar, i.e. the artifact shape the consume family re-verifies.
func writeVerifiedMarkerBackupArtifact(updatesRoot, backupID string) error {
	dir := filepath.Join(updatesRoot, backupsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	dumpPath := backupsDumpPath(updatesRoot, backupID)
	if err := os.WriteFile(dumpPath, []byte("PGDMP marker fixture\n"), 0o600); err != nil {
		return err
	}
	digest, err := hashFileSHA256(dumpPath)
	if err != nil {
		return err
	}
	return os.WriteFile(dumpPath+".sha256", []byte(digest+"\n"), 0o600)
}

// assertServingUntouched states the "no migration, the old release serves" half
// of every refusal: the pointer, the serving tree and the journal are exactly as
// the test left them.
func assertServingUntouched(t *testing.T, root string) {
	t.Helper()
	if got := pointerTokenForTest(t, root); got != markerPreviousRelease {
		t.Fatalf("current pointer = %q, want the previous release %q still serving", got, markerPreviousRelease)
	}
	if _, err := os.Stat(relDirOf(root, markerPreviousRelease)); err != nil {
		t.Fatalf("previous release tree = %v, want it untouched", err)
	}
	if record, found, err := embeddedupdate.Snapshot(filepath.Join(root, journalFileName)); err != nil || found {
		t.Fatalf("journal record = %+v found=%v err=%v, want no handoff at all", record, found, err)
	}
}

// assertTargetTreeStaged is the other half of a refusal that owns no discard:
// the tree the marker staged stays where the handoff left it.
func assertTargetTreeStaged(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(relDirOf(root, markerTargetRelease)); err != nil {
		t.Fatalf("target release tree = %v, want it still staged", err)
	}
}

// TestConsumePendingMarkerDiscardsMarkerRefusedByFenceSequenceFloor covers the
// replayed/lower-sequence marker: the fence's monotonic floor refuses this exact
// operation, so the marker and the tree it staged are discarded and the previous
// release keeps serving without any handoff.
func TestConsumePendingMarkerDiscardsMarkerRefusedByFenceSequenceFloor(t *testing.T) {
	root := markerRoot(t)
	marker := markerForRelease(t, root, markerTargetRelease, markerPreviousRelease, "marker-replay-op")
	markerPath := writePendingMarker(t, root, marker)

	fence := &recordingMarkerFence{acquireErr: updatefence.ErrTargetStale}
	backup := &recordingMarkerBackup{}
	installMarkerSeam(t, fence, backup)
	t.Setenv("BANSOKO_DATABASE_URL", markerTestDSN)

	if err := consumePendingMarker(root); err != nil {
		t.Fatalf("refused marker consumption = %v, want the current release to keep serving", err)
	}
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused marker still present: err=%v", err)
	}
	if _, err := os.Stat(relDirOf(root, markerTargetRelease)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused target tree still present: err=%v", err)
	}
	assertServingUntouched(t, root)
	if fence.rowFound {
		t.Fatalf("sequence refusal still recorded a fence row: %+v", fence.row)
	}
	want := fenceIdentity{marker.OperationID, updatefence.AdmittedByEmbedded, marker.ReleaseSequence, marker.ManifestDigest}
	if len(fence.acquires) != 1 || fence.acquires[0] != want {
		t.Fatalf("fence acquires = %+v, want exactly the marker identity %+v", fence.acquires, want)
	}
	if len(fence.closes) != 0 || fence.lists != 0 || backup.calls != 0 {
		t.Fatalf("sequence refusal touched later stages: closes=%+v lists=%d backup=%d", fence.closes, fence.lists, backup.calls)
	}
}

// TestConsumePendingMarkerDiscardsMarkerAfterClosedBackupFailure covers the other
// replay shape: the fence already carries this exact operation closed as failed,
// so the marker is a leftover of a backup that never became a handoff.
func TestConsumePendingMarkerDiscardsMarkerAfterClosedBackupFailure(t *testing.T) {
	root := markerRoot(t)
	marker := markerForRelease(t, root, markerTargetRelease, markerPreviousRelease, "marker-closed-op")
	markerPath := writePendingMarker(t, root, marker)

	fence := &recordingMarkerFence{rowFound: true, row: updatefence.Row{
		OperationID: marker.OperationID, AdmittedBy: updatefence.AdmittedByEmbedded,
		Sequence: marker.ReleaseSequence, ManifestDigest: marker.ManifestDigest,
		State: "failed", Released: true,
	}}
	backup := &recordingMarkerBackup{}
	installMarkerSeam(t, fence, backup)
	t.Setenv("BANSOKO_DATABASE_URL", markerTestDSN)

	if err := consumePendingMarker(root); err != nil {
		t.Fatalf("replayed marker after a closed backup = %v, want it discarded", err)
	}
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replayed marker still present: err=%v", err)
	}
	if _, err := os.Stat(relDirOf(root, markerTargetRelease)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replayed target tree still present: err=%v", err)
	}
	assertServingUntouched(t, root)
	if fence.calls() != 1 || fence.lookups != 1 {
		t.Fatalf("closed-failure replay re-entered the fence: acquires=%+v closes=%+v lists=%d lookups=%d",
			fence.acquires, fence.closes, fence.lists, fence.lookups)
	}
	if backup.calls != 0 {
		t.Fatalf("closed-failure replay ran the backup %d time(s)", backup.calls)
	}
}

// TestConsumePendingMarkerReplayWithPendingJournalRemovesOnlyTheMarker pins the
// idempotent replay: once the handoff journal proves this marker, consumption
// only drops the marker and never re-enters the fence.
func TestConsumePendingMarkerReplayWithPendingJournalRemovesOnlyTheMarker(t *testing.T) {
	root := markerRoot(t)
	marker := markerForRelease(t, root, markerTargetRelease, markerPreviousRelease, "marker-journal-op")
	markerPath := writePendingMarker(t, root, marker)
	backupID := "embedded-" + marker.OperationID
	if err := writeVerifiedMarkerBackupArtifact(root, backupID); err != nil {
		t.Fatal(err)
	}
	seedConsumeJournal(t, root, marker.OperationID, func(record *embeddedupdate.Record) {
		record.TargetReleaseID = marker.ReleaseID
		record.TargetReleaseSequence = marker.ReleaseSequence
		record.ManifestDigest = marker.ManifestDigest
		record.BackupID = backupID
		record.PreviousReleaseID = marker.PreviousReleaseID
		record.RollbackSourceCaptured = true
	}, embeddedupdate.StepAccepted, embeddedupdate.StepFetched, embeddedupdate.StepStaged,
		embeddedupdate.StepMaintenanceEntered, embeddedupdate.StepBackupVerified,
		embeddedupdate.StepPendingMigration)
	journalPath := filepath.Join(root, journalFileName)
	before := mustReadFile(t, journalPath)

	fence := &recordingMarkerFence{}
	backup := &recordingMarkerBackup{}
	installMarkerSeam(t, fence, backup)
	t.Setenv("BANSOKO_DATABASE_URL", markerTestDSN)

	if err := consumePendingMarker(root); err != nil {
		t.Fatalf("replayed marker with a pending journal = %v, want the marker dropped", err)
	}
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replayed marker still present: err=%v", err)
	}
	if after := mustReadFile(t, journalPath); string(after) != string(before) {
		t.Fatalf("replay rewrote the handoff journal:\n before=%s\n after=%s", before, after)
	}
	if fence.calls() != 0 || backup.calls != 0 {
		t.Fatalf("journal-proven replay touched an effect: acquires=%+v closes=%+v lists=%d lookups=%d backup=%d",
			fence.acquires, fence.closes, fence.lists, fence.lookups, backup.calls)
	}
	if got := pointerTokenForTest(t, root); got != markerPreviousRelease {
		t.Fatalf("current pointer = %q, want %q", got, markerPreviousRelease)
	}
	if _, err := os.Stat(relDirOf(root, markerTargetRelease)); err != nil {
		t.Fatalf("target release tree = %v, want it kept for the consume family", err)
	}
}

// TestConsumePendingMarkerBackupFailureClosesExactFenceWithoutMigration is the
// backup-failure boundary: the verified backup is the only irreversible
// prerequisite of the handoff, so its failure must close this operation's fence
// row in the terminal failed state and leave the deployment exactly as it was.
func TestConsumePendingMarkerBackupFailureClosesExactFenceWithoutMigration(t *testing.T) {
	root := markerRoot(t)
	marker := markerForRelease(t, root, markerTargetRelease, markerPreviousRelease, "marker-backup-failed-op")
	markerPath := writePendingMarker(t, root, marker)

	backupErr := errors.New("pg_dump failed: injected")
	fence := &recordingMarkerFence{rowFound: true, row: updatefence.Row{
		OperationID: marker.OperationID, AdmittedBy: updatefence.AdmittedByEmbedded,
		Sequence: marker.ReleaseSequence, ManifestDigest: marker.ManifestDigest, State: "active",
	}}
	backup := &recordingMarkerBackup{err: backupErr}
	installMarkerSeam(t, fence, backup)
	t.Setenv("BANSOKO_DATABASE_URL", markerTestDSN)

	err := consumePendingMarker(root)
	if !errors.Is(err, backupErr) {
		t.Fatalf("consumption error = %v, want the backup failure", err)
	}

	// The fence is closed exactly: one close, for this operation's owner
	// identity, sequence and digest, requested against the open row.
	if len(fence.closes) != 1 {
		t.Fatalf("fence closes = %+v, want exactly one", fence.closes)
	}
	wantClose := fenceClose{
		fenceIdentity: fenceIdentity{marker.OperationID, updatefence.AdmittedByEmbedded, marker.ReleaseSequence, marker.ManifestDigest},
		terminalState: "failed",
		released:      false,
	}
	if fence.closes[0] != wantClose {
		t.Fatalf("fence close = %+v, want %+v", fence.closes[0], wantClose)
	}
	if !fence.row.Released || fence.row.State != "failed" {
		t.Fatalf("fence row = %+v, want a released terminal failed row", fence.row)
	}
	if len(fence.acquires) != 0 {
		t.Fatalf("an already-open marker fence was re-acquired: %+v", fence.acquires)
	}

	// No migration: the journal never holds a handoff record, the pointer still
	// names the serving release and both trees are untouched...
	if record, found, err := embeddedupdate.Snapshot(filepath.Join(root, journalFileName)); err != nil || found {
		t.Fatalf("journal record = %+v found=%v err=%v, want nothing past the backup stage", record, found, err)
	}
	if _, err := os.Stat(relDirOf(root, markerTargetRelease)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed-backup target tree still present: err=%v", err)
	}
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed-backup marker still present: err=%v", err)
	}
	assertServingUntouched(t, root)
	// ...and the failure stopped at the backup stage exactly once.
	if backup.calls != 1 {
		t.Fatalf("backup attempts = %d, want exactly 1", backup.calls)
	}
	if fence.lists != 1 {
		t.Fatalf("open-fence list calls = %d, want exactly 1", fence.lists)
	}
}

// TestConsumePendingMarkerHappyPathHandsOffPendingMigrationWithVerifiedBackup
// pins the hand-off boundary: the exact marker identity acquires the fence, the
// verified backup lands with its sidecar, and the journal is written at
// pending_migration while the pointer still names the serving release.
func TestConsumePendingMarkerHappyPathHandsOffPendingMigrationWithVerifiedBackup(t *testing.T) {
	root := markerRoot(t)
	marker := markerForRelease(t, root, markerTargetRelease, markerPreviousRelease, "marker-handoff-op")
	markerPath := writePendingMarker(t, root, marker)

	fence := &recordingMarkerFence{}
	backup := &recordingMarkerBackup{}
	installMarkerSeam(t, fence, backup)
	t.Setenv("BANSOKO_DATABASE_URL", markerTestDSN)

	if err := consumePendingMarker(root); err != nil {
		t.Fatalf("handoff consumption = %v", err)
	}

	wantIdentity := fenceIdentity{marker.OperationID, updatefence.AdmittedByEmbedded, marker.ReleaseSequence, marker.ManifestDigest}
	if len(fence.acquires) != 1 || fence.acquires[0] != wantIdentity {
		t.Fatalf("fence acquires = %+v, want exactly %+v", fence.acquires, wantIdentity)
	}
	if len(fence.closes) != 0 {
		t.Fatalf("handoff closed its own fence: %+v", fence.closes)
	}
	if !fence.rowFound || fence.row.Released || fence.row.State != "active" {
		t.Fatalf("fence row = %+v, want an open row handed to the consume family", fence.row)
	}

	// The backup ran before the handoff existed and left a verifiable artifact.
	expectedBackupID := "embedded-" + marker.OperationID
	if backup.calls != 1 || backup.journalBefore {
		t.Fatalf("backup ran %d time(s) with journalBefore=%v, want one backup before the handoff", backup.calls, backup.journalBefore)
	}
	if _, err := verifyBackupSidecar(root, expectedBackupID); err != nil {
		t.Fatalf("handoff backup is not verifiable: %v", err)
	}
	info, err := os.Stat(backupsDumpPath(root, expectedBackupID))
	if err != nil || info.Size() == 0 || !info.Mode().IsRegular() {
		t.Fatalf("backup dump = %+v err=%v, want a non-empty regular file", info, err)
	}

	record := reloadRecordForTest(t, root)
	if record.Step != embeddedupdate.StepPendingMigration {
		t.Fatalf("handoff step = %q, want pending_migration", record.Step)
	}
	if record.OperationID != marker.OperationID || record.Channel != updatefence.AdmittedByEmbedded ||
		record.TargetReleaseID != marker.ReleaseID || record.TargetVersion != "0.9.0-rc.10" ||
		record.TargetSchemaVersion != "schema-test" || record.TargetReleaseSequence != marker.ReleaseSequence ||
		record.ManifestDigest != marker.ManifestDigest || record.BackupID != expectedBackupID ||
		record.PreviousReleaseID != marker.PreviousReleaseID || !record.RollbackSourceCaptured {
		t.Fatalf("handoff record = %+v, want exact marker and signed release identity", record)
	}
	if !record.Active() || !consumeFamilySteps[record.Step] {
		t.Fatalf("handoff record is not consumable: step=%q active=%v", record.Step, record.Active())
	}

	// The boundary itself: the marker is consumed, the staged tree stays for the
	// consume family, and nothing has migrated or promoted yet.
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumed marker still present: err=%v", err)
	}
	if _, err := os.Stat(relDirOf(root, markerTargetRelease)); err != nil {
		t.Fatalf("handoff target tree = %v, want it kept for the migration", err)
	}
	if got := pointerTokenForTest(t, root); got != markerPreviousRelease {
		t.Fatalf("current pointer = %q, want %q until the consume family promotes", got, markerPreviousRelease)
	}
}

// TestConsumePendingMarkerRefusesMalformedMarkersWithoutAcquiring proves the
// marker is validated as a bounded regular file with exactly five unambiguous
// fields before any effect exists to refuse: nothing is discarded, no fence call
// is made and no journal is opened.
func TestConsumePendingMarkerRefusesMalformedMarkersWithoutAcquiring(t *testing.T) {
	valid := `{"operation_id":"marker-op","release_id":"` + markerTargetRelease + `","manifest_digest":"` +
		strings.Repeat("a", 64) + `","release_sequence":10,"previous_release_id":"` + markerPreviousRelease + `"}`
	cases := []struct {
		name string
		raw  string
	}{
		{name: "missing field", raw: `{"operation_id":"marker-op","release_id":"` + markerTargetRelease + `"}`},
		{name: "extra field", raw: strings.TrimSuffix(valid, "}") + `,"extra":true}`},
		{name: "repeated member", raw: strings.Replace(valid,
			`"release_id":"`+markerTargetRelease+`"`,
			`"release_id":"`+markerTargetRelease+`","release_id":"`+markerTargetRelease+`"`, 1)},
		{name: "sequence is not a number", raw: strings.Replace(valid, `"release_sequence":10`, `"release_sequence":"10"`, 1)},
		{name: "not an object", raw: `["marker-op"]`},
		{name: "empty document", raw: ""},
		{name: "oversized document", raw: `"` + strings.Repeat("x", maxPendingMarkerBytes) + `"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := markerRoot(t)
			markerPath := filepath.Join(root, pendingMarkerName)
			if err := os.WriteFile(markerPath, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			fence := &recordingMarkerFence{}
			backup := &recordingMarkerBackup{}
			installMarkerSeam(t, fence, backup)
			// No owner DSN: reaching the database stage would surface as a
			// different error, so the refusal is proven to come first.
			t.Setenv("BANSOKO_DATABASE_URL", "")

			err := consumePendingMarker(root)
			if err == nil || !strings.Contains(err.Error(), "invalid pending marker") {
				t.Fatalf("error = %v, want the malformed marker refused", err)
			}
			if fence.calls() != 0 || backup.calls != 0 {
				t.Fatalf("malformed marker reached the fence or backup: acquires=%+v closes=%+v lists=%d lookups=%d backup=%d",
					fence.acquires, fence.closes, fence.lists, fence.lookups, backup.calls)
			}
			if _, err := os.Lstat(markerPath); err != nil {
				t.Fatalf("refused marker was removed: %v", err)
			}
			assertServingUntouched(t, root)
			assertTargetTreeStaged(t, root)
		})
	}

	t.Run("symlinked marker", func(t *testing.T) {
		root := markerRoot(t)
		outside := filepath.Join(t.TempDir(), "marker.json")
		if err := os.WriteFile(outside, []byte(valid), 0o600); err != nil {
			t.Fatal(err)
		}
		markerPath := filepath.Join(root, pendingMarkerName)
		if err := os.Symlink(outside, markerPath); err != nil {
			t.Fatalf("create symlinked marker: %v", err)
		}
		fence := &recordingMarkerFence{}
		backup := &recordingMarkerBackup{}
		installMarkerSeam(t, fence, backup)
		t.Setenv("BANSOKO_DATABASE_URL", "")

		err := consumePendingMarker(root)
		if err == nil || !strings.Contains(err.Error(), "invalid pending marker") {
			t.Fatalf("error = %v, want the symlinked marker refused", err)
		}
		if fence.calls() != 0 || backup.calls != 0 {
			t.Fatalf("symlinked marker reached the fence or backup: acquires=%+v closes=%+v lists=%d lookups=%d backup=%d",
				fence.acquires, fence.closes, fence.lists, fence.lookups, backup.calls)
		}
		if info, err := os.Lstat(markerPath); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlinked marker = %+v err=%v, want the link refused in place", info, err)
		}
		if raw, err := os.ReadFile(outside); err != nil || string(raw) != valid {
			t.Fatalf("symlink target = %q err=%v, want it untouched", raw, err)
		}
		assertServingUntouched(t, root)
		assertTargetTreeStaged(t, root)
	})
}

// TestConsumePendingMarkerRefusesUngovernedMarkersBeforeAnyFenceEffect covers
// the well-formed markers the deployment must not act on: the signed tree is the
// trust root for every field except the operation id, and without the owner DSN
// there is no way to prove the fence. Each refusal must precede every effect.
func TestConsumePendingMarkerRefusesUngovernedMarkersBeforeAnyFenceEffect(t *testing.T) {
	cases := []struct {
		name   string
		dsn    string
		mutate func(*pendingMarker)
		want   string
	}{
		{
			name:   "previous release is not the one serving",
			dsn:    markerTestDSN,
			mutate: func(marker *pendingMarker) { marker.PreviousReleaseID = "rel-marker-c" },
			want:   "differs from current release",
		},
		{
			name:   "digest differs from the signed manifest",
			dsn:    markerTestDSN,
			mutate: func(marker *pendingMarker) { marker.ManifestDigest = strings.Repeat("b", 64) },
			want:   "digest differs from signed manifest",
		},
		{
			name:   "sequence differs from the signed manifest",
			dsn:    markerTestDSN,
			mutate: func(marker *pendingMarker) { marker.ReleaseSequence++ },
			want:   "release identity differs from signed manifest",
		},
		{
			name: "owner database DSN is absent",
			dsn:  "",
			want: "requires the owner database DSN",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := markerRoot(t)
			marker := markerForRelease(t, root, markerTargetRelease, markerPreviousRelease, "marker-ungoverned-op")
			if tc.mutate != nil {
				tc.mutate(&marker)
			}
			markerPath := writePendingMarker(t, root, marker)
			fence := &recordingMarkerFence{}
			backup := &recordingMarkerBackup{}
			installMarkerSeam(t, fence, backup)
			t.Setenv("BANSOKO_DATABASE_URL", tc.dsn)

			err := consumePendingMarker(root)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if fence.calls() != 0 || backup.calls != 0 {
				t.Fatalf("refused marker reached the fence or backup: acquires=%+v closes=%+v lists=%d lookups=%d backup=%d",
					fence.acquires, fence.closes, fence.lists, fence.lookups, backup.calls)
			}
			if _, err := os.Lstat(markerPath); err != nil {
				t.Fatalf("refused marker was removed: %v", err)
			}
			assertServingUntouched(t, root)
			assertTargetTreeStaged(t, root)
		})
	}
}
