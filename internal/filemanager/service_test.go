package filemanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeStore answers owner facts from in-memory rows, matching paths exactly
// and directories by prefix, like the PostgreSQL store.
type fakeStore struct {
	mounts     []MountConfig
	workTops   []AnimePlacements
	placements []PlacementFact
	companions []CompanionFact
	inProgress []InProgressFact
	anime      map[string][]string
	operations []UserOperation
	locked     map[string][]string
	// episodes is keyed by placement id for a placement lineage and by
	// absolute path for a file with none; the real store prefers the placement.
	episodes  map[string]*EpisodeEvidence
	pathCalls int
}

func (f *fakeStore) Mounts(context.Context) ([]MountConfig, error) { return f.mounts, nil }

func (f *fakeStore) AnimePlacementIndex(context.Context) ([]AnimePlacements, error) {
	return f.workTops, nil
}

func (f *fakeStore) PathFacts(_ context.Context, paths []string) (PathFacts, error) {
	f.pathCalls++
	wanted := map[string]bool{}
	for _, path := range paths {
		wanted[path] = true
	}
	facts := PathFacts{Placements: map[string][]PlacementFact{}, Companions: map[string][]CompanionFact{}, InProgress: map[string][]InProgressFact{}}
	for _, placement := range f.placements {
		if wanted[placement.Path] {
			facts.Placements[placement.Path] = append(facts.Placements[placement.Path], placement)
		}
	}
	for _, companion := range f.companions {
		if wanted[companion.Path] {
			facts.Companions[companion.Path] = append(facts.Companions[companion.Path], companion)
		}
	}
	for _, owner := range f.inProgress {
		if wanted[owner.Path] {
			facts.InProgress[owner.Path] = append(facts.InProgress[owner.Path], owner)
		}
	}
	return facts, nil
}

func (f *fakeStore) SubtreeFacts(_ context.Context, dirs []string) (map[string]SubtreeFact, error) {
	result := map[string]SubtreeFact{}
	for _, dir := range dirs {
		prefix := strings.TrimRight(dir, "/") + "/"
		var fact SubtreeFact
		for _, owner := range f.inProgress {
			fact.InProgress = fact.InProgress || strings.HasPrefix(owner.Path, prefix)
		}
		for _, placement := range f.placements {
			if !strings.HasPrefix(placement.Path, prefix) {
				continue
			}
			if state, _ := placementState(placement); state == StateManaged {
				fact.Managed = true
			}
		}
		result[dir] = fact
	}
	return result, nil
}

func (f *fakeStore) SubtreeLockedPaths(_ context.Context, dir string, limit int) ([]string, error) {
	prefix := strings.TrimRight(dir, "/") + "/"
	var result []string
	for _, path := range f.locked[dir] {
		if strings.HasPrefix(path, prefix) && len(result) < limit {
			result = append(result, path)
		}
	}
	return result, nil
}

func (f *fakeStore) AnimePlacementPaths(_ context.Context, id string) ([]string, error) {
	return f.anime[id], nil
}

func (f *fakeStore) LatestUserOperation(_ context.Context, paths []string) (*UserOperation, error) {
	var latest *UserOperation
	for index := range f.operations {
		op := &f.operations[index]
		for _, path := range paths {
			if op.SourcePath == path || op.TargetPath == path || op.ResultPath == path {
				if latest == nil || op.CreatedAt.After(latest.CreatedAt) {
					latest = op
				}
			}
		}
	}
	return latest, nil
}

func (f *fakeStore) PlacementCompanions(_ context.Context, placementID string) ([]CompanionFact, error) {
	var result []CompanionFact
	for _, companion := range f.companions {
		if companion.Placement.ID == placementID {
			result = append(result, companion)
		}
	}
	return result, nil
}

func (f *fakeStore) EpisodeEvidence(_ context.Context, placementID, path string) (*EpisodeEvidence, error) {
	if placementID != "" {
		return f.episodes[placementID], nil
	}
	return f.episodes[path], nil
}

func mustCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// canonicalTemp returns a symlink-resolved temporary directory: a browse never
// follows a link component, so a browsable fixture path is always the resolved
// spelling of its directory.
func canonicalTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func newTestService(t *testing.T, store *fakeStore, zone DenialZone) *Service {
	t.Helper()
	service, err := NewService(store, zone)
	if err != nil {
		t.Fatal(err)
	}
	service.SetAlignmentPolicy(alignmentPolicy(true))
	return service
}

type alignmentPolicy bool

func (p alignmentPolicy) AlignmentEnabled(context.Context, string) (bool, error) { return bool(p), nil }

func TestCleanAbsoluteRefusesEscapesAndCleans(t *testing.T) {
	for _, value := range []string{"", "relative/x", ".", "./x", "\x00/a", "/a\x00b", string([]byte{0xff}), "/" + strings.Repeat("a", 4097)} {
		if _, ok := cleanAbsolute(value); ok {
			t.Fatalf("cleanAbsolute(%q) accepted a value that is not a usable absolute path", value)
		}
	}
	for value, want := range map[string]string{"/": "/", "/a//b/./c/": "/a/b/c", "/a/../../b": "/b", "/a/../..": "/"} {
		got, ok := cleanAbsolute(value)
		if !ok || got != want {
			t.Fatalf("cleanAbsolute(%q) = %q, %v; want %q", value, got, ok, want)
		}
	}
}

func TestListNeverFollowsASymlinkComponent(t *testing.T) {
	base := canonicalTemp(t)
	library := filepath.Join(base, "library")
	outside := filepath.Join(base, "outside")
	writeFile(t, filepath.Join(library, "Show", "ep01.mkv"), 3)
	writeFile(t, filepath.Join(outside, "secret.txt"), 1)
	if err := os.Symlink(outside, filepath.Join(library, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(library, "Show"), filepath.Join(library, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(library, "Show", "link.txt")); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, &fakeStore{}, NewDenialZone(filepath.Join(base, "appdata")))
	ctx := context.Background()

	listing, err := service.List(ctx, library, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]EntryKind{}
	for _, entry := range listing.Entries {
		kinds[entry.Name] = entry.Kind
	}
	if kinds["escape"] != KindSymlink || kinds["alias"] != KindSymlink || kinds["Show"] != KindDirectory {
		t.Fatalf("symlinks must be listed as symlinks, never followed: %v", kinds)
	}
	for _, path := range []string{"escape", "alias", "escape/x", "alias/ep01.mkv"} {
		_, err := service.List(ctx, filepath.Join(library, path), "", 0)
		mustCode(t, err, CodeSymlinkNotFollowed)
	}
	for _, path := range []string{"relative", "/a\x00b", strings.Repeat("a", 4097)} {
		_, err := service.List(ctx, path, "", 0)
		mustCode(t, err, CodePathInvalid)
		_, err = service.Detail(ctx, path)
		mustCode(t, err, CodePathInvalid)
	}
	_, err = service.List(ctx, filepath.Join(library, "missing"), "", 0)
	mustCode(t, err, CodePathNotFound)
	_, err = service.List(ctx, filepath.Join(library, "Show", "ep01.mkv"), "", 0)
	mustCode(t, err, CodeNotADirectory)
	_, err = service.List(ctx, "/", "", 0)
	if err != nil {
		t.Fatalf("the filesystem root is browsable: %v", err)
	}

	// A symlink entry is described, never followed: its detail is the link.
	detail, err := service.Detail(ctx, filepath.Join(library, "Show", "link.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if detail.Entry.Kind != KindSymlink || detail.Entry.SizeBytes != nil {
		t.Fatalf("symlink detail must not follow the link: %+v", detail.Entry)
	}
	_, err = service.Detail(ctx, filepath.Join(library, "escape", "secret.txt"))
	mustCode(t, err, CodeSymlinkNotFollowed)
	_, err = service.Detail(ctx, "/")
	mustCode(t, err, CodePathInvalid)
}

func TestBrowsingRefusesTheDenialZone(t *testing.T) {
	base := canonicalTemp(t)
	appdata := filepath.Join(base, "appdata")
	library := filepath.Join(base, "library")
	writeFile(t, filepath.Join(appdata, "bansoko.db"), 1)
	writeFile(t, filepath.Join(library, "a.mkv"), 1)
	// A second name for the same directory must not open a way in.
	if err := os.Symlink(appdata, filepath.Join(library, "shadow")); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, &fakeStore{}, NewDenialZone(appdata))
	ctx := context.Background()

	for _, path := range []string{appdata, filepath.Join(appdata, "bansoko.db"), appdata + "/nested/deeper", "/proc", "/sys/kernel", "/dev/null"} {
		_, err := service.List(ctx, path, "", 0)
		mustCode(t, err, CodePathDenied)
		_, err = service.Detail(ctx, path)
		mustCode(t, err, CodePathDenied)
	}
	// The denied directory is listed (it is a name in a browsable directory)
	// but its content is not readable.
	listing, err := service.List(ctx, library, "", 0)
	if err != nil || len(listing.Entries) != 2 {
		t.Fatalf("listing = %+v, %v", listing.Entries, err)
	}
	if _, err := service.Detail(ctx, filepath.Join(library, "shadow", "bansoko.db")); err == nil {
		t.Fatal("a denied path reached through a second name must be refused")
	}
}

func TestMountsResolveAvailabilityAndWorkTops(t *testing.T) {
	base := canonicalTemp(t)
	library := filepath.Join(base, "library")
	plain := filepath.Join(base, "plain.txt")
	writeFile(t, filepath.Join(library, "Show", "a.mkv"), 1)
	writeFile(t, plain, 1)
	linked := filepath.Join(base, "linked")
	if err := os.Symlink(library, linked); err != nil {
		t.Fatal(err)
	}
	denied := filepath.Join(base, "appdata")
	writeFile(t, filepath.Join(denied, "Show", "b.mkv"), 1)
	store := &fakeStore{
		mounts: []MountConfig{
			{Key: DownloadMountPrefix + "qb-missing", Kind: MountDownload, ConnectionID: "qb-missing", Path: filepath.Join(base, "nope")},
			{Key: DownloadMountPrefix + "qb-file", Kind: MountDownload, ConnectionID: "qb-file", Path: plain},
			{Key: DownloadMountPrefix + "qb-linked", Kind: MountDownload, ConnectionID: "qb-linked", Path: linked},
			{Key: DownloadMountPrefix + "qb-broken", Kind: MountDownload, ConnectionID: "qb-broken", Path: "relative"},
		},
		workTops: []AnimePlacements{
			{ManagedAnimeID: "anime-1", Title: "Show 1", Paths: []string{filepath.Join(library, "Show", "a.mkv")}},
			{ManagedAnimeID: "anime-2", Title: "Gone 2", Paths: []string{filepath.Join(base, "gone", "b.mkv")}},
			{ManagedAnimeID: "anime-3", Title: "Denied 3", Paths: []string{filepath.Join(denied, "Show", "b.mkv")}},
			{ManagedAnimeID: "anime-4", Title: "Nowhere 4", Paths: []string{"relative.mkv"}},
		},
	}
	service := newTestService(t, store, NewDenialZone(denied))
	ctx := context.Background()

	mounts, err := service.Mounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 6 {
		t.Fatalf("mounts = %+v", mounts)
	}
	root := mounts[0]
	if root.Key != RootMountKey || root.Kind != MountRoot || root.Path != RootMountPath || !root.Available || root.Unavailable != "" {
		t.Fatalf("the filesystem root is the browser's fixed entry: %+v", root)
	}
	want := []MountUnavailable{MountMissing, MountNotDirectory, "", MountNotConfigured}
	for index, mount := range mounts[1:5] {
		if mount.Unavailable != want[index] || mount.Available != (want[index] == "") {
			t.Fatalf("mount %s: available=%v reason=%q, want %q", mount.Key, mount.Available, mount.Unavailable, want[index])
		}
	}
	// A mount configured through a symlink is browsed at its target; a work
	// mount is the tree top of its placements, and a work whose tree top is
	// missing, denied or unevidenced has no mount at all.
	work := mounts[5]
	if work.Key != AnimeMountPrefix+"anime-1" || work.Kind != MountAnime || work.Path != filepath.Join(library, "Show") ||
		work.Label != "Show 1" || work.ManagedAnimeID != "anime-1" || !work.Available {
		t.Fatalf("work mount = %+v", work)
	}
	listing, err := service.List(ctx, mounts[3].canonical, "", 0)
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "Show" {
		t.Fatalf("linked mount listing = %+v, %v", listing.Entries, err)
	}
}

func TestListPagesInStableOrderWithBoundCursor(t *testing.T) {
	library := canonicalTemp(t)
	for _, dir := range []string{"b-dir", "a-dir"} {
		if err := os.MkdirAll(filepath.Join(library, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"e.mkv", "c.mkv", "d.mkv", "B.mkv", "a.mkv"} {
		writeFile(t, filepath.Join(library, name), 2)
	}
	service := newTestService(t, &fakeStore{}, NewDenialZone(filepath.Join(library, "appdata")))
	ctx := context.Background()
	var names []string
	cursor := ""
	pages := 0
	for {
		listing, err := service.List(ctx, library, cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if len(listing.Entries) > 3 {
			t.Fatalf("page exceeded limit: %d", len(listing.Entries))
		}
		for _, entry := range listing.Entries {
			names = append(names, entry.Name)
		}
		if listing.NextCursor == "" {
			break
		}
		cursor = listing.NextCursor
	}
	want := "a-dir,b-dir,B.mkv,a.mkv,c.mkv,d.mkv,e.mkv"
	if got := strings.Join(names, ","); got != want || pages != 3 {
		t.Fatalf("paged order = %s over %d pages, want %s over 3", got, pages, want)
	}
	first, err := service.List(ctx, library, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.List(ctx, filepath.Join(library, "a-dir"), first.NextCursor, 3)
	mustCode(t, err, CodeCursorInvalid)
	_, err = service.List(ctx, library, "not-a-cursor!", 3)
	mustCode(t, err, CodeCursorInvalid)
	listing, err := service.List(ctx, library, "", 1000)
	if err != nil || len(listing.Entries) != 7 {
		t.Fatalf("limit must clamp, not fail: %d, %v", len(listing.Entries), err)
	}
}

// stateFixture lays out one tree with a file per ownership case.
type stateFixture struct {
	service *Service
	store   *fakeStore
	library string
}

func activePlacement(id, path, anime string) PlacementFact {
	return PlacementFact{ID: id, Path: path, State: "active", ManagedAnimeID: anime, AnimeExists: true,
		AnimeTitle: "Show " + anime, SubscriptionState: "active", AcquisitionID: "acq-" + id}
}

func newStateFixture(t *testing.T) stateFixture {
	t.Helper()
	library := canonicalTemp(t)
	abs := func(rel string) string { return filepath.Join(library, filepath.FromSlash(rel)) }
	for _, rel := range []string{
		"Show/managed.mkv", "Show/managed.zh.ass", "Show/stopped.mkv", "Show/ended.mkv", "Show/replaced.mkv",
		"Show/extras/unknown.mkv", "Show/busy.mkv", "Show/busy.zh.ass", "Show/moved.zh.ass",
		"OneShot/movie.mkv", "OneShot/movie.zh.ass", "Loose/external.mkv", "Loose/old.zh.ass",
		"Busy/inside/x.mkv",
	} {
		writeFile(t, abs(rel), 4)
	}
	managed := activePlacement("p-managed", abs("Show/managed.mkv"), "anime-1")
	stopped := activePlacement("p-stopped", abs("Show/stopped.mkv"), "anime-1")
	stopped.SubscriptionState = "stopped"
	ended := activePlacement("p-ended", abs("Show/ended.mkv"), "anime-2")
	ended.Retired = true
	replaced := activePlacement("p-replaced", abs("Show/replaced.mkv"), "anime-1")
	replaced.State = "superseded"
	// An unknown file is released at admission.
	unknown := activePlacement("p-unknown", abs("Show/extras/unknown.mkv"), "anime-1")
	unknown.State = "released"
	busy := activePlacement("p-busy", abs("Show/busy.mkv"), "anime-1")
	oneShot := activePlacement("p-oneshot", abs("OneShot/movie.mkv"), "anime-temp")
	oneShot.OneShotID = "os-1"
	oneShot.SubscriptionState = "stopped"
	store := &fakeStore{
		placements: []PlacementFact{managed, stopped, ended, replaced, unknown, busy, oneShot},
		companions: []CompanionFact{
			{ID: "c-managed", Path: abs("Show/managed.zh.ass"), State: "active", MediaKind: "subtitle", Suffix: "zh", Extension: "ass", Placement: managed},
			{ID: "c-busy", Path: abs("Show/busy.zh.ass"), State: "active", MediaKind: "subtitle", Suffix: "zh", Extension: "ass", Placement: busy},
			// Registered beside the old video name: not aligned with it.
			{ID: "c-moved", Path: abs("Show/moved.zh.ass"), State: "active", MediaKind: "subtitle", Suffix: "zh", Extension: "ass", Placement: stopped},
			{ID: "c-oneshot", Path: abs("OneShot/movie.zh.ass"), State: "active", MediaKind: "subtitle", Suffix: "zh", Extension: "ass", Placement: oneShot},
			{ID: "c-old", Path: abs("Loose/old.zh.ass"), State: "ended", MediaKind: "subtitle", Suffix: "zh", Extension: "ass", Placement: managed},
		},
		inProgress: []InProgressFact{
			{Path: abs("Show/busy.mkv"), Kind: InProgressAlignment},
			{Path: abs("Busy/inside/x.mkv"), Kind: InProgressFileOperation, AcquisitionID: "acq-busy"},
		},
		anime: map[string][]string{"anime-1": {abs("Show/managed.mkv"), abs("Show/extras/unknown.mkv")}},
		locked: map[string][]string{
			"Show":  {abs("Show/managed.mkv")},
			"Busy":  {abs("Busy/inside/x.mkv")},
			"Loose": {},
		},
	}
	return stateFixture{service: newTestService(t, store, NewDenialZone(filepath.Join(library, "appdata"))), store: store, library: library}
}

func (f stateFixture) abs(rel string) string {
	return filepath.Join(f.library, filepath.FromSlash(rel))
}

func (f stateFixture) entries(t *testing.T, directory string) map[string]Entry {
	t.Helper()
	listing, err := f.service.List(context.Background(), f.abs(directory), "", 200)
	if err != nil {
		t.Fatalf("list %s: %v", directory, err)
	}
	result := map[string]Entry{}
	for _, entry := range listing.Entries {
		result[entry.Name] = entry
	}
	return result
}

func TestListDerivesOwnershipStatesAndLocks(t *testing.T) {
	fixture := newStateFixture(t)
	show := fixture.entries(t, "Show")
	for name, want := range map[string]State{
		"managed.mkv":  StateManaged,
		"stopped.mkv":  StateManaged, // a stopped subscription still manages its files
		"ended.mkv":    StateDetached,
		"replaced.mkv": StateDetached,
		"busy.mkv":     StateInProgress, // in progress wins over managed
		"extras":       StateDetached,   // holds only a released unknown file
	} {
		if got := show[name].State; got != want {
			t.Fatalf("%s: state %s, want %s", name, got, want)
		}
	}
	if show["managed.mkv"].Role != RoleVideo {
		t.Fatalf("a placement is a video: %+v", show["managed.mkv"])
	}
	// The lock marks a file the product manages, or whose owner is unfinished,
	// and names the work that manages it; a detached file is not locked.
	for name, want := range map[string]bool{
		"managed.mkv": true, "stopped.mkv": true, "busy.mkv": true, "ended.mkv": false, "replaced.mkv": false,
	} {
		if got := show[name].Locked; got != want {
			t.Fatalf("%s: locked=%v, want %v", name, got, want)
		}
	}
	if show["managed.mkv"].ManagedAnimeID != "anime-1" || show["ended.mkv"].ManagedAnimeID != "" {
		t.Fatalf("the lock names the managing work: %+v", show["managed.mkv"])
	}
	if unknown := fixture.entries(t, "Show/extras")["unknown.mkv"]; unknown.State != StateDetached || unknown.Locked {
		t.Fatalf("a released unknown file is detached and unlocked: %+v", unknown)
	}
	loose := fixture.entries(t, "Loose")
	if loose["external.mkv"].State != StateDetached || loose["external.mkv"].Role != RoleNone || loose["external.mkv"].Locked {
		t.Fatalf("a file without records is detached: %+v", loose["external.mkv"])
	}
	// A directory is judged by its subtree, so one holding a managed file is
	// locked exactly like the file itself.
	root := fixture.entries(t, "")
	for name, want := range map[string]State{"Show": StateInProgress, "OneShot": StateDetached, "Loose": StateDetached, "Busy": StateInProgress} {
		if got := root[name].State; got != want {
			t.Fatalf("directory %s: state %s, want %s", name, got, want)
		}
	}
	for name, want := range map[string]bool{"Show": true, "Busy": true, "Loose": false, "OneShot": false} {
		if got := root[name].Locked; got != want {
			t.Fatalf("directory %s: locked=%v, want %v", name, got, want)
		}
	}
}

func TestOneShotFilesAreDetachedEvenWithActivePlacement(t *testing.T) {
	fixture := newStateFixture(t)
	entries := fixture.entries(t, "OneShot")
	if entries["movie.mkv"].State != StateDetached || entries["movie.zh.ass"].State != StateDetached {
		t.Fatalf("one-shot video and its subtitle must be detached: %+v", entries)
	}
	detail, err := fixture.service.Detail(context.Background(), fixture.abs("OneShot/movie.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if detail.DetachedReason != DetachedOneShot || detail.AcquisitionID != "acq-p-oneshot" {
		t.Fatalf("one-shot detail must link its execution record: %+v", detail)
	}
	for _, rule := range detail.Operations {
		if rule.Operation == OperationRelease {
			t.Fatalf("a detached file offers no release: %+v", detail.Operations)
		}
	}
}

func TestSubtitleStateFollowsItsVideo(t *testing.T) {
	fixture := newStateFixture(t)
	show := fixture.entries(t, "Show")
	subtitle := show["managed.zh.ass"]
	if subtitle.State != StateManaged || subtitle.Role != RoleSubtitle || subtitle.CompanionOf != fixture.abs("Show/managed.mkv") || subtitle.NotAligned {
		t.Fatalf("a bound subtitle follows its managed video: %+v", subtitle)
	}
	if busy := show["busy.zh.ass"]; busy.State != StateInProgress {
		t.Fatalf("a subtitle inherits its video's unfinished owner: %+v", busy)
	}
	if moved := show["moved.zh.ass"]; moved.State != StateManaged || !moved.NotAligned {
		t.Fatalf("a subtitle under an old basename is managed but not aligned: %+v", moved)
	}
	if old := fixture.entries(t, "Loose")["old.zh.ass"]; old.State != StateDetached {
		t.Fatalf("an ended companion relation is detached: %+v", old)
	}
	detail, err := fixture.service.Detail(context.Background(), fixture.abs("Show/managed.zh.ass"))
	if err != nil {
		t.Fatal(err)
	}
	if detail.Video == nil || detail.Video.Path != fixture.abs("Show/managed.mkv") || !detail.Video.Present || detail.Anime == nil || detail.Anime.ID != "anime-1" {
		t.Fatalf("subtitle detail names its video and anime: %+v", detail)
	}
	rules := map[Operation]OperationRule{}
	for _, rule := range detail.Operations {
		rules[rule.Operation] = rule
	}
	if rules[OperationRelease].Block != BlockFollowsVideo || rules[OperationMove].Block != BlockManaged {
		t.Fatalf("a subtitle is never released alone and a managed file never moves: %+v", detail.Operations)
	}
	video, err := fixture.service.Detail(context.Background(), fixture.abs("Show/managed.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if len(video.Companions) != 1 || video.Companions[0].Path != fixture.abs("Show/managed.zh.ass") || !video.Companions[0].Present {
		t.Fatalf("video detail lists its active companions: %+v", video.Companions)
	}
}

func TestDetailReportsOwnersAndOperationMatrix(t *testing.T) {
	fixture := newStateFixture(t)
	ctx := context.Background()
	managed, err := fixture.service.Detail(ctx, fixture.abs("Show/managed.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if managed.Anime == nil || managed.Anime.Title != "Show anime-1" || managed.AcquisitionID != "acq-p-managed" {
		t.Fatalf("managed detail names its anime and record: %+v", managed)
	}
	allowed := func(detail Detail) map[Operation]bool {
		result := map[Operation]bool{}
		for _, rule := range detail.Operations {
			result[rule.Operation] = rule.Allowed
		}
		return result
	}
	got := allowed(managed)
	if got[OperationMove] || got[OperationRename] || got[OperationDelete] || !got[OperationAlign] || !got[OperationCopy] || !got[OperationRelease] {
		t.Fatalf("D1: a managed file is aligned, copied or released, never moved/renamed/deleted: %+v", managed.Operations)
	}
	external, err := fixture.service.Detail(ctx, fixture.abs("Loose/external.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	got = allowed(external)
	if !got[OperationMove] || !got[OperationDelete] || external.DetachedReason != DetachedNoRecord || external.Anime != nil {
		t.Fatalf("a detached file allows user file operations: %+v", external)
	}
	ended, err := fixture.service.Detail(ctx, fixture.abs("Show/ended.mkv"))
	if err != nil || ended.DetachedReason != DetachedSubscriptionEnded || ended.Anime == nil || !ended.Anime.Ended {
		t.Fatalf("an unsubscribed anime's file is detached: %+v, %v", ended, err)
	}
	busy, err := fixture.service.Detail(ctx, fixture.abs("Show/busy.mkv"))
	if err != nil || len(busy.InProgress) != 1 || busy.InProgress[0].Kind != InProgressAlignment {
		t.Fatalf("in-progress detail lists the owner: %+v, %v", busy, err)
	}
	for _, rule := range busy.Operations {
		if rule.Allowed || rule.Block != BlockInProgress {
			t.Fatalf("nothing is allowed while in progress: %+v", busy.Operations)
		}
	}
	// A directory carries its subtree's state and no operation matrix: an
	// operation on it is judged when it is submitted.
	directory, err := fixture.service.Detail(ctx, fixture.abs("Busy"))
	if err != nil || directory.Entry.State != StateInProgress || !directory.Entry.Locked || directory.Operations != nil {
		t.Fatalf("directory detail: %+v, %v", directory, err)
	}
	// The filesystem root is a browse root, never an entry of its own.
	_, err = fixture.service.Detail(ctx, "/")
	mustCode(t, err, CodePathInvalid)
}

func TestDetailReportsTheFrozenEpisodeEvidence(t *testing.T) {
	fixture := newStateFixture(t)
	ctx := context.Background()
	fixture.store.episodes = map[string]*EpisodeEvidence{
		// The placement lineage of a managed video is authoritative.
		"p-managed": {State: "resolved", RawValue: "3", Shape: "single"},
		// The same file's own path names a different answer: it must lose.
		fixture.abs("Show/managed.mkv"): {State: "unsupported", FailureCode: "episode_shape_unsupported", RawValue: "03-04", Shape: "range"},
		// A file with no owner record is read at its own path.
		fixture.abs("Loose/external.mkv"): {State: "unsupported", FailureCode: "episode_shape_unsupported", RawValue: "01-02", Shape: "range"},
	}
	managed, err := fixture.service.Detail(ctx, fixture.abs("Show/managed.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if managed.Episode == nil || managed.Episode.State != "resolved" || managed.Episode.RawValue != "3" || managed.Episode.Shape != "single" || managed.Episode.FailureCode != "" {
		t.Fatalf("a managed video reads its placement lineage: %+v", managed.Episode)
	}
	external, err := fixture.service.Detail(ctx, fixture.abs("Loose/external.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if external.Episode == nil || external.Episode.State != "unsupported" || external.Episode.FailureCode != "episode_shape_unsupported" ||
		external.Episode.RawValue != "01-02" || external.Episode.Shape != "range" {
		t.Fatalf("a file without owner facts reads its path lineage: %+v", external.Episode)
	}
	// A file with no frozen identity reports none.
	if untouched, err := fixture.service.Detail(ctx, fixture.abs("Show/ended.mkv")); err != nil || untouched.Episode != nil {
		t.Fatalf("no frozen identity = %+v, %v", untouched.Episode, err)
	}
}

func TestLocateFindsTheWorkTreeTop(t *testing.T) {
	fixture := newStateFixture(t)
	location, err := fixture.service.Locate(context.Background(), "anime-1")
	if err != nil || location.Path != fixture.abs("Show") {
		t.Fatalf("locate = %+v, %v", location, err)
	}
	_, err = fixture.service.Locate(context.Background(), "anime-none")
	mustCode(t, err, CodeLocationUnknown)
	// A work whose recorded tree top no longer exists is located at the
	// closest directory above it that does exist.
	fixture.store.anime["anime-gone"] = []string{filepath.Join(fixture.library, "Gone", "a.mkv")}
	location, err = fixture.service.Locate(context.Background(), "anime-gone")
	if err != nil || location.Path != fixture.library {
		t.Fatalf("locate of a vanished tree top = %+v, %v", location, err)
	}
}

// TestOwnerFactsMatchTheResolvedSpelling pins the comparison rule: the browsed
// spelling of a path and the spelling its resolving yields are both looked up,
// so the rows a deployment recorded for the file are found either way. The
// browse itself never follows the link: the entry stays a symlink and only its
// ownership is derived from the file it names.
func TestOwnerFactsMatchTheResolvedSpelling(t *testing.T) {
	library := canonicalTemp(t)
	writeFile(t, filepath.Join(library, "al.mkv"), 4)
	if err := os.Symlink(filepath.Join(library, "al.mkv"), filepath.Join(library, "link.mkv")); err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{placements: []PlacementFact{activePlacement("p-alias", filepath.Join(library, "al.mkv"), "anime-1")}}
	service := newTestService(t, store, NewDenialZone(filepath.Join(library, "appdata")))

	detail, err := service.Detail(context.Background(), filepath.Join(library, "link.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if detail.Entry.Kind != KindSymlink || detail.Entry.Path != filepath.Join(library, "link.mkv") {
		t.Fatalf("the link is browsed as a link: %+v", detail.Entry)
	}
	if detail.Entry.State != StateManaged || !detail.Entry.Locked {
		t.Fatalf("the placement recorded for the file the link names must be found: %+v", detail.Entry)
	}
	if detail.Entry.CompanionOf != "" || detail.Revision == "" {
		t.Fatalf("a matched entry carries a revision and no companion: %+v", detail.Entry)
	}
	entries := map[string]Entry{}
	listing, err := service.List(context.Background(), library, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range listing.Entries {
		entries[entry.Name] = entry
	}
	if !entries["link.mkv"].Locked || !entries["al.mkv"].Locked {
		t.Fatalf("both spellings of a managed file are locked: %+v", entries)
	}
}
