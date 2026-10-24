package filemanager

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// fakeCommands records what the service hands the command store; it re-checks
// nothing itself (the persistence tests cover the transactional re-check).
type fakeCommands struct {
	byKey      map[string]UserOperation
	digests    map[string]string
	admitted   []OperationAdmission
	released   []ReleaseAdmission
	releaseKey map[string][]string
}

func (f *fakeCommands) OperationByKey(_ context.Context, key string) (*UserOperation, string, error) {
	if op, ok := f.byKey[key]; ok {
		return &op, f.digests[key], nil
	}
	return nil, "", nil
}

func (f *fakeCommands) Operation(_ context.Context, id string) (*UserOperation, error) {
	for _, op := range f.byKey {
		if op.ID == id {
			return &op, nil
		}
	}
	return nil, nil
}

func (f *fakeCommands) AdmitOperation(_ context.Context, admission OperationAdmission) (UserOperation, error) {
	f.admitted = append(f.admitted, admission)
	op := UserOperation{ID: admission.ID, Operation: admission.Operation, State: UserOperationReady,
		SourcePath: admission.SourcePath, TargetPath: admission.TargetPath}
	if f.byKey == nil {
		f.byKey, f.digests = map[string]UserOperation{}, map[string]string{}
	}
	f.byKey[admission.IdempotencyKey], f.digests[admission.IdempotencyKey] = op, admission.Digest
	return op, nil
}

func (f *fakeCommands) ReleasedByKey(_ context.Context, key string) (*ReleaseOutcome, []string, error) {
	if paths, ok := f.releaseKey[key]; ok {
		return &ReleaseOutcome{ManagedAnimeID: "anime-1", Released: 1}, paths, nil
	}
	return nil, nil, nil
}

func (f *fakeCommands) Release(_ context.Context, admission ReleaseAdmission) (ReleaseOutcome, error) {
	f.released = append(f.released, admission)
	if f.releaseKey == nil {
		f.releaseKey = map[string][]string{}
	}
	f.releaseKey[admission.IdempotencyKey] = admission.Spellings
	return ReleaseOutcome{ManagedAnimeID: admission.ManagedAnimeID, Released: 1}, nil
}

type fakeAligner struct{ admitted []AlignAdmission }

func (f *fakeAligner) Align(_ context.Context, admission AlignAdmission) (AlignOutcome, error) {
	f.admitted = append(f.admitted, admission)
	return AlignOutcome{ManagedAnimeID: admission.ManagedAnimeID}, nil
}

type commandFixture struct {
	service  *Service
	store    *fakeStore
	commands *fakeCommands
	aligner  *fakeAligner
	library  string
	appdata  string
}

func newCommandFixture(t *testing.T) commandFixture {
	t.Helper()
	library := canonicalTemp(t)
	appdata := filepath.Join(library, "appdata")
	for _, rel := range []string{"Show/managed.mkv", "Show/managed.zh.ass", "Loose/a.mkv", "Loose/b.mkv", "Loose/Sub/.keep", ".bansoko-cleanup-x/object"} {
		writeFile(t, filepath.Join(library, filepath.FromSlash(rel)), 4)
	}
	managed := activePlacement("p-managed", filepath.Join(library, "Show", "managed.mkv"), "anime-1")
	managed.Version = 3
	store := &fakeStore{
		placements: []PlacementFact{managed},
		companions: []CompanionFact{{ID: "c-managed", Path: filepath.Join(library, "Show", "managed.zh.ass"), State: "active",
			MediaKind: "subtitle", Suffix: "zh", Extension: "ass", Placement: managed}},
		locked: map[string][]string{
			filepath.Join(library, "Show"): {
				filepath.Join(library, "Show", "managed.mkv"),
				filepath.Join(library, "Show", "managed.zh.ass"),
			},
		},
	}
	service := newTestService(t, store, NewDenialZone(appdata))
	commands, aligner := &fakeCommands{}, &fakeAligner{}
	service.SetCommands(commands, aligner)
	return commandFixture{service: service, store: store, commands: commands, aligner: aligner, library: library, appdata: appdata}
}

func (f commandFixture) abs(rel string) string {
	return filepath.Join(f.library, filepath.FromSlash(rel))
}

func (f commandFixture) revision(t *testing.T, rel string) string {
	t.Helper()
	detail, err := f.service.Detail(context.Background(), f.abs(rel))
	if err != nil {
		t.Fatalf("detail %s: %v", rel, err)
	}
	return detail.Revision
}

func TestCommandsRefusedUnwired(t *testing.T) {
	service := newTestService(t, &fakeStore{}, NewDenialZone(t.TempDir()))
	_, err := service.SubmitOperation(context.Background(), OperationRequest{IdempotencyKey: "k"})
	mustCode(t, err, CodeCommandsUnavailable)
	_, err = service.Release(context.Background(), ReleaseRequest{IdempotencyKey: "k"})
	mustCode(t, err, CodeCommandsUnavailable)
	_, err = service.Align(context.Background(), AlignRequest{IdempotencyKey: "k"})
	mustCode(t, err, CodeCommandsUnavailable)
}

func TestSubmitOperationChecksRevisionAndMatrix(t *testing.T) {
	f := newCommandFixture(t)
	ctx := context.Background()
	rev := f.revision(t, "Loose/a.mkv")

	_, err := f.service.SubmitOperation(ctx, OperationRequest{IdempotencyKey: "k1", Operation: UserDelete, Path: f.abs("Loose/a.mkv"), ExpectedRevision: "stale"})
	mustCode(t, err, CodeEntryChanged)

	// A managed file is never moved, renamed or deleted by the user.
	managedRev := f.revision(t, "Show/managed.mkv")
	for _, kind := range []UserOperationKind{UserMove, UserRename, UserDelete} {
		request := OperationRequest{IdempotencyKey: "km-" + string(kind), Operation: kind, Path: f.abs("Show/managed.mkv"), ExpectedRevision: managedRev}
		if kind != UserDelete {
			request.Target = &Target{Path: f.abs("Show/other.mkv")}
		}
		_, err = f.service.SubmitOperation(ctx, request)
		mustCode(t, err, CodeOperationNotAllowed)
	}
	// Copy reads a managed file; the copy is detached and never overwrites.
	op, err := f.service.SubmitOperation(ctx, OperationRequest{IdempotencyKey: "kc", Operation: UserCopy, Path: f.abs("Show/managed.mkv"), ExpectedRevision: managedRev, Target: &Target{Path: f.abs("Loose/copy.mkv")}})
	if err != nil || op.State != UserOperationReady {
		t.Fatalf("copy of a managed file: %+v, %v", op, err)
	}
	if got := f.commands.admitted[len(f.commands.admitted)-1]; got.RequireDetachedSource || got.Collision != ConflictBlock {
		t.Fatalf("copy admission = %+v", got)
	}

	op, err = f.service.SubmitOperation(ctx, OperationRequest{IdempotencyKey: "kd", Operation: UserDelete, Path: f.abs("Loose/a.mkv"), ExpectedRevision: rev})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.commands.admitted[len(f.commands.admitted)-1]; !got.RequireDetachedSource || got.TargetPath != "" || got.ExpectedSize != 4 {
		t.Fatalf("delete admission = %+v", got)
	}
	_, err = f.service.SubmitOperation(ctx, OperationRequest{IdempotencyKey: "kd2", Operation: UserDelete, Path: f.abs("Loose/a.mkv"), ExpectedRevision: rev, Target: &Target{Path: f.abs("Loose/x.mkv")}})
	mustCode(t, err, CodeTargetInvalid)
	_ = op
}

// TestSubmitOperationRefusesALockedSubtree proves the lock is judged by the
// subtree: a directory operation is refused while the product manages a file
// below it, and the refusal names the files that hold it.
func TestSubmitOperationRefusesALockedSubtree(t *testing.T) {
	f := newCommandFixture(t)
	ctx := context.Background()
	rev := f.revision(t, "Show")
	_, err := f.service.SubmitOperation(ctx, OperationRequest{IdempotencyKey: "d1", Operation: UserDelete, Path: f.abs("Show"), ExpectedRevision: rev})
	mustCode(t, err, CodeEntryLocked)
	var typed *Error
	if !errors.As(err, &typed) || len(typed.Paths) != 2 {
		t.Fatalf("the refusal names the managed files: %v", err)
	}
	// A directory holding nothing managed is renamed like any other.
	looseRev := f.revision(t, "Loose/Sub")
	if _, err := f.service.SubmitOperation(ctx, OperationRequest{IdempotencyKey: "d2", Operation: UserRename,
		Path: f.abs("Loose/Sub"), ExpectedRevision: looseRev, Target: &Target{Path: f.abs("Loose/Sub2")}}); err != nil {
		t.Fatal(err)
	}
}

func TestSubmitOperationTargetRules(t *testing.T) {
	f := newCommandFixture(t)
	ctx := context.Background()
	rev := f.revision(t, "Loose/a.mkv")
	submit := func(key string, kind UserOperationKind, target *Target) error {
		_, err := f.service.SubmitOperation(ctx, OperationRequest{IdempotencyKey: key, Operation: kind, Path: f.abs("Loose/a.mkv"), ExpectedRevision: rev, Target: target})
		return err
	}
	mustCode(t, submit("t1", UserMove, nil), CodeTargetInvalid)
	mustCode(t, submit("t2", UserMove, &Target{Path: "../escape.mkv"}), CodeTargetInvalid)
	mustCode(t, submit("t3", UserMove, &Target{Path: f.abs("Loose/a.mkv")}), CodeTargetInvalid)
	mustCode(t, submit("t4", UserRename, &Target{Path: f.abs("Loose/Sub/a.mkv")}), CodeTargetInvalid)
	mustCode(t, submit("t5", UserMove, &Target{Path: f.abs("Missing/a.mkv")}), CodeTargetInvalid)
	mustCode(t, submit("t6", UserMove, &Target{Path: f.abs(".bansoko-cleanup-x/a.mkv")}), CodeTargetInvalid)
	mustCode(t, submit("t7", UserMove, &Target{Path: f.abs("Loose/x.mkv"), Conflict: "overwrite"}), CodeTargetInvalid)
	// A target the browser refuses to read is refused to write as well.
	mustCode(t, submit("t8", UserMove, &Target{Path: filepath.Join(f.appdata, "x.mkv")}), CodeTargetInvalid)
	mustCode(t, submit("t8b", UserMove, &Target{Path: "/"}), CodeTargetInvalid)

	// An occupied target: on disk, or a path an owner manages, is refused
	// under block, whatever the target's own state (Q9: no overwrite at all).
	mustCode(t, submit("t9", UserMove, &Target{Path: f.abs("Loose/b.mkv")}), CodeTargetExists)
	mustCode(t, submit("t10", UserRename, &Target{Path: f.abs("Loose/b.mkv")}), CodeTargetExists)
	f.store.placements = append(f.store.placements, activePlacement("p-ghost", f.abs("Loose/ghost.mkv"), "anime-2"))
	rev = f.revision(t, "Loose/a.mkv")
	mustCode(t, submit("t11", UserMove, &Target{Path: f.abs("Loose/ghost.mkv")}), CodeTargetExists)
	f.store.inProgress = append(f.store.inProgress, InProgressFact{Path: f.abs("Loose/held.mkv"), Kind: InProgressUserOperation})
	rev = f.revision(t, "Loose/a.mkv")
	mustCode(t, submit("t12", UserMove, &Target{Path: f.abs("Loose/held.mkv")}), CodeTargetExists)

	// version admits the request; the worker picks the free suffix.
	if err := submit("t13", UserMove, &Target{Path: f.abs("Loose/b.mkv"), Conflict: ConflictVersion}); err != nil {
		t.Fatal(err)
	}
	got := f.commands.admitted[len(f.commands.admitted)-1]
	if got.Collision != ConflictVersion || got.TargetPath != f.abs("Loose/b.mkv") {
		t.Fatalf("version admission = %+v", got)
	}
	// A rename re-checks the conflict against its new target.
	if err := submit("t14", UserRename, &Target{Path: f.abs("Loose/c.mkv")}); err != nil {
		t.Fatal(err)
	}
	// Copy onto itself is never a target, with or without a suffix: the file
	// itself is refused before any conflict rule applies.
	mustCode(t, submit("t15", UserCopy, &Target{Path: f.abs("Loose/a.mkv")}), CodeTargetInvalid)
	mustCode(t, submit("t16", UserCopy, &Target{Path: f.abs("Loose/a.mkv"), Conflict: ConflictVersion}), CodeTargetInvalid)
}

func TestSubmitOperationIdempotency(t *testing.T) {
	f := newCommandFixture(t)
	ctx := context.Background()
	rev := f.revision(t, "Loose/a.mkv")
	request := OperationRequest{IdempotencyKey: "same", Operation: UserMove, Path: f.abs("Loose/a.mkv"), ExpectedRevision: rev, Target: &Target{Path: f.abs("Loose/moved.mkv")}}
	first, err := f.service.SubmitOperation(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	// A replay returns the admitted operation even after the entry changed.
	replay := request
	writeFile(t, f.abs("Loose/a.mkv"), 9)
	again, err := f.service.SubmitOperation(ctx, replay)
	if err != nil || again.ID != first.ID || len(f.commands.admitted) != 1 {
		t.Fatalf("replay = %+v, %v (admitted %d)", again, err, len(f.commands.admitted))
	}
	other := request
	other.Target = &Target{Path: f.abs("Loose/elsewhere.mkv")}
	_, err = f.service.SubmitOperation(ctx, other)
	mustCode(t, err, CodeIdempotencyConflict)
	_, err = f.service.SubmitOperation(ctx, OperationRequest{Operation: UserDelete, Path: f.abs("Loose/a.mkv")})
	mustCode(t, err, CodeIdempotencyKeyInvalid)
	got, err := f.service.Operation(ctx, first.ID)
	if err != nil || got.ID != first.ID {
		t.Fatalf("operation read = %+v, %v", got, err)
	}
	_, err = f.service.Operation(ctx, "missing")
	mustCode(t, err, CodeOperationNotFound)
}

func TestReleaseFreezesPlacementVersionAndMove(t *testing.T) {
	f := newCommandFixture(t)
	ctx := context.Background()
	rev := f.revision(t, "Show/managed.mkv")

	// A subtitle is released with its video, never on its own.
	subRev := f.revision(t, "Show/managed.zh.ass")
	_, err := f.service.Release(ctx, ReleaseRequest{IdempotencyKey: "r0", Path: f.abs("Show/managed.zh.ass"), ExpectedRevision: subRev})
	mustCode(t, err, CodeOperationNotAllowed)
	// A detached file has nothing to release.
	looseRev := f.revision(t, "Loose/a.mkv")
	_, err = f.service.Release(ctx, ReleaseRequest{IdempotencyKey: "r1", Path: f.abs("Loose/a.mkv"), ExpectedRevision: looseRev})
	mustCode(t, err, CodeOperationNotAllowed)
	// "Release and move" refuses an occupied target before anything is written.
	_, err = f.service.Release(ctx, ReleaseRequest{IdempotencyKey: "r2", Path: f.abs("Show/managed.mkv"), ExpectedRevision: rev, MoveTo: &Target{Path: f.abs("Loose/a.mkv")}})
	mustCode(t, err, CodeTargetExists)
	if len(f.commands.released) != 0 {
		t.Fatalf("a refused release wrote: %+v", f.commands.released)
	}

	outcome, err := f.service.Release(ctx, ReleaseRequest{IdempotencyKey: "r3", Path: f.abs("Show/managed.mkv"), ExpectedRevision: rev, MoveTo: &Target{Path: f.abs("Loose/managed.mkv")}})
	if err != nil || outcome.Released != 1 {
		t.Fatalf("release = %+v, %v", outcome, err)
	}
	got := f.commands.released[0]
	if got.Placement != (VersionRef{ID: "p-managed", Version: 3}) || got.ManagedAnimeID != "anime-1" || got.Move == nil {
		t.Fatalf("release admission = %+v", got)
	}
	if got.Spellings[0] != f.abs("Show/managed.mkv") {
		t.Fatalf("release freezes the spelled path: %+v", got.Spellings)
	}
	if got.Move.Operation != UserMove || got.Move.ReleasePlacementID != "p-managed" || got.Move.Digest == "" ||
		got.Move.TargetPath != f.abs("Loose/managed.mkv") {
		t.Fatalf("release move admission = %+v", got.Move)
	}
	// A replay of the same key and path returns the outcome; another path
	// under the same key conflicts.
	if _, err := f.service.Release(ctx, ReleaseRequest{IdempotencyKey: "r3", Path: f.abs("Show/managed.mkv"), ExpectedRevision: "whatever"}); err != nil {
		t.Fatalf("release replay: %v", err)
	}
	_, err = f.service.Release(ctx, ReleaseRequest{IdempotencyKey: "r3", Path: f.abs("Loose/a.mkv"), ExpectedRevision: looseRev})
	mustCode(t, err, CodeIdempotencyConflict)
}

func TestAlignOnlyManagedVideoAndPolicyGated(t *testing.T) {
	f := newCommandFixture(t)
	ctx := context.Background()
	rev := f.revision(t, "Show/managed.mkv")
	outcome, err := f.service.Align(ctx, AlignRequest{IdempotencyKey: "a1", Path: f.abs("Show/managed.mkv"), ExpectedRevision: rev})
	if err != nil || outcome.ManagedAnimeID != "anime-1" || len(f.aligner.admitted) != 1 {
		t.Fatalf("align = %+v, %v", outcome, err)
	}
	if got := f.aligner.admitted[0]; got.PlacementID != "p-managed" || got.IdempotencyKey != "a1" {
		t.Fatalf("align admission = %+v", got)
	}
	looseRev := f.revision(t, "Loose/a.mkv")
	_, err = f.service.Align(ctx, AlignRequest{IdempotencyKey: "a2", Path: f.abs("Loose/a.mkv"), ExpectedRevision: looseRev})
	mustCode(t, err, CodeOperationNotAllowed)

	f.service.SetAlignmentPolicy(alignmentPolicy(false))
	rev = f.revision(t, "Show/managed.mkv")
	_, err = f.service.Align(ctx, AlignRequest{IdempotencyKey: "a3", Path: f.abs("Show/managed.mkv"), ExpectedRevision: rev})
	mustCode(t, err, CodeAlignmentDisabled)
}

func TestRevisionChangesWithOwnerFacts(t *testing.T) {
	f := newCommandFixture(t)
	before := f.revision(t, "Loose/a.mkv")
	if again := f.revision(t, "Loose/a.mkv"); again != before {
		t.Fatalf("revision is not stable: %s vs %s", before, again)
	}
	f.store.placements = append(f.store.placements, activePlacement("p-new", f.abs("Loose/a.mkv"), "anime-3"))
	if after := f.revision(t, "Loose/a.mkv"); after == before {
		t.Fatal("a new owner fact must change the revision")
	}
}
