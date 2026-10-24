package filemanager

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"bansoko/internal/filemaintenance"
	"bansoko/internal/worktree"
)

const (
	defaultPageLimit = 100
	maxPageLimit     = 200
)

// Service is the file manager's read side: browse and derive.
type Service struct {
	store     Store
	zone      DenialZone
	alignment AlignmentPolicy
	commands  CommandStore
	aligner   Aligner

	matchRenderer MatchRenderer
	matchAnimes   MatchAnimeReader
	seasons       SeasonCatalog
}

// AlignmentPolicy reports whether the effective file maintenance policy of
// an anime has alignment enabled.
type AlignmentPolicy interface {
	AlignmentEnabled(ctx context.Context, managedAnimeID string) (bool, error)
}

// SetAlignmentPolicy wires the policy read that gates the align operation.
// Unwired, align is refused (fail closed).
func (s *Service) SetAlignmentPolicy(policy AlignmentPolicy) {
	if s != nil {
		s.alignment = policy
	}
}

// NewService builds the file manager over a read-only owner-fact store. The
// denial zone is injected by the composition root: it names the application's
// own data directories, which are runtime configuration rather than container
// layout.
func NewService(store Store, zone DenialZone) (*Service, error) {
	if store == nil {
		return nil, errors.New("file manager store is nil")
	}
	return &Service{store: store, zone: zone}, nil
}

// Mounts lists the quick-entry mounts with their availability. A mount is a
// navigation shortcut only: it never bounds what may be read or written.
//
// The work mounts are the works' tree tops — the deepest directory common to a
// work's own active placements, which is the directory the product last put
// that work's files in. A work with no placement evidence has no tree top and
// is left out rather than given an invented path.
func (s *Service) Mounts(ctx context.Context) ([]Mount, error) {
	configs, err := s.store.Mounts(ctx)
	if err != nil {
		return nil, err
	}
	placed, err := s.store.AnimePlacementIndex(ctx)
	if err != nil {
		return nil, err
	}
	mounts := make([]Mount, 0, len(configs)+len(placed)+1)
	mounts = append(mounts, resolveMount(MountConfig{Key: RootMountKey, Kind: MountRoot, Label: RootMountPath, Path: RootMountPath}))
	for _, config := range configs {
		mounts = append(mounts, resolveMount(config))
	}
	seen := map[string]bool{}
	for _, anime := range placed {
		top, ok := worktree.Top(anime.Paths)
		if !ok || seen[top] || s.zone.Denies(top) || !directoryExists(top) {
			continue
		}
		seen[top] = true
		mounts = append(mounts, resolveMount(MountConfig{
			Key: AnimeMountPrefix + anime.ManagedAnimeID, Kind: MountAnime, Label: anime.Title,
			Path: top, ManagedAnimeID: anime.ManagedAnimeID,
		}))
	}
	return mounts, nil
}

// cursorKey is the stable sort position of an entry: directories first, then
// byte-wise name order.
type cursorKey struct {
	Directory bool   `json:"d"`
	Name      string `json:"n"`
	Scope     string `json:"s"`
}

func entryLess(left, right rawEntry) bool {
	leftDir, rightDir := left.kind == KindDirectory, right.kind == KindDirectory
	if leftDir != rightDir {
		return leftDir
	}
	return left.name < right.name
}

func afterCursor(entry rawEntry, cursor cursorKey) bool {
	directory := entry.kind == KindDirectory
	if directory != cursor.Directory {
		return !directory
	}
	return entry.name > cursor.Name
}

// cursorScope binds a cursor to the directory it was issued for.
func cursorScope(directory string) string {
	digest := sha256.Sum256([]byte(directory))
	return hex.EncodeToString(digest[:8])
}

func encodeCursor(key cursorKey) string {
	raw, _ := json.Marshal(key)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(value, scope string) (cursorKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursorKey{}, invalid(CodeCursorInvalid, "cursor is not valid")
	}
	var key cursorKey
	if err := json.Unmarshal(raw, &key); err != nil || key.Scope != scope {
		return cursorKey{}, invalid(CodeCursorInvalid, "cursor is not valid for this directory")
	}
	return key, nil
}

// List reads one sorted, bounded page of an absolute directory and derives
// every entry's state. It never writes.
func (s *Service) List(ctx context.Context, absolutePath, cursor string, limit int) (Listing, error) {
	if err := refuseDenied(s.zone, absolutePath); err != nil {
		return Listing{}, err
	}
	directory, walked, err := walkAbsolute(absolutePath, false)
	if err != nil {
		return Listing{}, err
	}
	raw, truncated, err := readDirectory(directory, walked)
	if err != nil {
		return Listing{}, err
	}
	if limit <= 0 {
		limit = defaultPageLimit
	}
	if limit > maxPageLimit {
		limit = maxPageLimit
	}
	sort.Slice(raw, func(i, j int) bool { return entryLess(raw[i], raw[j]) })
	scope := cursorScope(directory)
	start := 0
	if cursor != "" {
		key, err := decodeCursor(cursor, scope)
		if err != nil {
			return Listing{}, err
		}
		start = sort.Search(len(raw), func(index int) bool { return afterCursor(raw[index], key) })
	}
	end := start + limit
	if end > len(raw) {
		end = len(raw)
	}
	page := raw[start:end]
	listing := Listing{Path: directory, Truncated: truncated}
	if end < len(raw) {
		last := page[len(page)-1]
		listing.NextCursor = encodeCursor(cursorKey{Directory: last.kind == KindDirectory, Name: last.name, Scope: scope})
	}
	listing.Entries, err = s.derivePage(ctx, directory, page)
	if err != nil {
		return Listing{}, err
	}
	return listing, nil
}

func (s *Service) derivePage(ctx context.Context, directory string, page []rawEntry) ([]Entry, error) {
	var filePaths, dirPaths []string
	entryPaths := make([][]string, len(page))
	for index, entry := range page {
		spellings := absoluteSpellings(joinAbsolute(directory, entry.name))
		entryPaths[index] = spellings
		if entry.kind == KindDirectory {
			dirPaths = append(dirPaths, spellings...)
		} else {
			filePaths = append(filePaths, spellings...)
		}
	}
	facts, videoInProgress, err := s.fileFacts(ctx, filePaths)
	if err != nil {
		return nil, err
	}
	subtrees := map[string]SubtreeFact{}
	if len(dirPaths) > 0 {
		subtrees, err = s.store.SubtreeFacts(ctx, dirPaths)
		if err != nil {
			return nil, err
		}
	}
	entries := make([]Entry, 0, len(page))
	for index, raw := range page {
		spellings := entryPaths[index]
		entry := Entry{Name: raw.name, Path: spellings[0], Kind: raw.kind, SizeBytes: raw.size, ModifiedAt: raw.modTime}
		if raw.kind == KindDirectory {
			var merged SubtreeFact
			for _, spelling := range spellings {
				fact := subtrees[spelling]
				merged.InProgress = merged.InProgress || fact.InProgress
				merged.Managed = merged.Managed || fact.Managed
			}
			entry.State = deriveDirectory(merged)
			// A directory is locked like the file that holds it: an
			// operation on the directory would touch that file.
			applyLock(&entry)
		} else {
			derived := deriveFile(facts, spellings, videoInProgress)
			applyDerivation(&entry, derived)
			applyLock(&entry)
			if entry.Locked && derived.placement != nil {
				entry.ManagedAnimeID = derived.placement.ManagedAnimeID
			}
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func applyDerivation(entry *Entry, derived derivation) {
	entry.State = derived.state
	entry.Role = derived.role
	entry.NotAligned = derived.notAligned
	if derived.companion != nil && derived.placement != nil {
		entry.CompanionOf = derived.placement.Path
	}
}

// applyLock marks an entry the product manages. A locked file cannot be moved,
// renamed or deleted until a user unlocks it; a directory whose subtree holds
// one is locked the same way, because an operation on it would touch the file.
func applyLock(entry *Entry) {
	entry.Locked = entry.State == StateManaged || entry.State == StateInProgress
}

// fileFacts reads the owner facts of the file paths plus the in-progress
// owners of every video a listed companion follows.
func (s *Service) fileFacts(ctx context.Context, paths []string) (PathFacts, func(string) []InProgressFact, error) {
	if len(paths) == 0 {
		return PathFacts{}, nil, nil
	}
	facts, err := s.store.PathFacts(ctx, paths)
	if err != nil {
		return PathFacts{}, nil, err
	}
	queried := make(map[string]bool, len(paths))
	for _, path := range paths {
		queried[path] = true
	}
	var videos []string
	for _, companions := range facts.Companions {
		for _, companion := range companions {
			if video := companion.Placement.Path; video != "" && !queried[video] {
				queried[video] = true
				videos = append(videos, video)
			}
		}
	}
	videoFacts := facts
	if len(videos) > 0 {
		extra, err := s.store.PathFacts(ctx, videos)
		if err != nil {
			return PathFacts{}, nil, err
		}
		videoFacts = mergeFacts(facts, extra)
	}
	return facts, func(path string) []InProgressFact { return videoFacts.InProgress[path] }, nil
}

func mergeFacts(left, right PathFacts) PathFacts {
	merged := PathFacts{InProgress: map[string][]InProgressFact{}}
	for path, values := range left.InProgress {
		merged.InProgress[path] = append(merged.InProgress[path], values...)
	}
	for path, values := range right.InProgress {
		merged.InProgress[path] = append(merged.InProgress[path], values...)
	}
	return merged
}

// Detail derives the read-only detail of one absolute entry path.
func (s *Service) Detail(ctx context.Context, absolutePath string) (Detail, error) {
	inspected, err := s.inspect(ctx, absolutePath)
	if err != nil {
		return Detail{}, err
	}
	return inspected.detail, nil
}

// inspection is one entry's detail together with the facts it was derived
// from; commands re-derive with it on every write.
type inspection struct {
	absolute  string
	spellings []string
	info      fs.FileInfo
	detail    Detail
	derived   derivation
	facts     PathFacts
}

func (s *Service) inspect(ctx context.Context, absolutePath string) (inspection, error) {
	if err := refuseDenied(s.zone, absolutePath); err != nil {
		return inspection{}, err
	}
	cleaned, ok := cleanAbsolute(absolutePath)
	if !ok {
		return inspection{}, invalid(CodePathInvalid, "path must be an absolute path")
	}
	if cleaned == string(filepath.Separator) {
		return inspection{}, invalid(CodePathInvalid, "path must name an entry below the filesystem root")
	}
	absolute, info, err := walkAbsolute(cleaned, true)
	if err != nil {
		return inspection{}, err
	}
	spellings := absoluteSpellings(absolute)
	raw := describe(filepath.Base(absolute), info)
	entry := Entry{Name: raw.name, Path: absolute, Kind: raw.kind, SizeBytes: raw.size, ModifiedAt: raw.modTime}
	result := inspection{absolute: absolute, spellings: spellings, info: info}
	detail := Detail{}
	if raw.kind == KindDirectory {
		subtrees, err := s.store.SubtreeFacts(ctx, spellings)
		if err != nil {
			return inspection{}, err
		}
		var merged SubtreeFact
		for _, fact := range subtrees {
			merged.InProgress = merged.InProgress || fact.InProgress
			merged.Managed = merged.Managed || fact.Managed
		}
		entry.State = deriveDirectory(merged)
		applyLock(&entry)
		detail.Entry = entry
		detail.Revision = revisionOf(entry, derivation{state: entry.State}, PathFacts{}, spellings, nil)
		result.detail = detail
		return result, nil
	}
	facts, videoInProgress, err := s.fileFacts(ctx, spellings)
	if err != nil {
		return inspection{}, err
	}
	derived := deriveFile(facts, spellings, videoInProgress)
	applyDerivation(&entry, derived)
	applyLock(&entry)
	if entry.Locked && derived.placement != nil {
		entry.ManagedAnimeID = derived.placement.ManagedAnimeID
	}
	detail.Entry = entry
	detail.DetachedReason = derived.reason
	seen := map[InProgressRef]bool{}
	for _, owner := range derived.inProgress {
		ref := InProgressRef{Kind: owner.Kind, AcquisitionID: owner.AcquisitionID}
		if !seen[ref] {
			seen[ref] = true
			detail.InProgress = append(detail.InProgress, ref)
		}
	}
	if placement := derived.placement; placement != nil {
		if placement.AnimeExists {
			detail.Anime = &AnimeRef{ID: placement.ManagedAnimeID, Title: placement.AnimeTitle, SubscriptionState: placement.SubscriptionState, Ended: placement.Retired}
		}
		detail.AcquisitionID = placement.AcquisitionID
	}
	if derived.companion != nil && derived.placement != nil {
		videoPath := derived.placement.Path
		detail.Video = &LinkedFile{Path: videoPath, Present: regularFileExists(videoPath), Role: RoleVideo}
	}
	var companions []CompanionFact
	if derived.role == RoleVideo && derived.placement != nil {
		companions, err = s.store.PlacementCompanions(ctx, derived.placement.ID)
		if err != nil {
			return inspection{}, err
		}
		detail.Companions = linkedCompanions(companions)
	}
	if entry.Kind == KindFile {
		alignmentEnabled := false
		if entry.State == StateManaged && derived.role == RoleVideo && derived.placement != nil {
			alignmentEnabled, err = s.alignmentEnabled(ctx, derived.placement.ManagedAnimeID)
			if err != nil {
				return inspection{}, err
			}
		}
		detail.Operations = operationsFor(entry.State, entry.Role, alignmentEnabled, matchRuleFor(entry, internalNamespace(absolute)))
		if entry.State == StateDetached {
			detail.MatchDefaultAnime = matchDefaultAnime(derived)
		}
		// The frozen episode identity explains why a file is unrecognized. A
		// video's placement is its authoritative lineage; any other file (or a
		// video with no placement) is read at its own path.
		placementID := ""
		if derived.role == RoleVideo && derived.placement != nil {
			placementID = derived.placement.ID
		}
		detail.Episode, err = s.store.EpisodeEvidence(ctx, placementID, absolute)
		if err != nil {
			return inspection{}, err
		}
	}
	last, err := s.store.LatestUserOperation(ctx, spellings)
	if err != nil {
		return inspection{}, err
	}
	detail.LastOperation = last
	detail.Revision = revisionOf(entry, derived, facts, spellings, companions)
	result.detail = detail
	result.derived = derived
	result.facts = facts
	return result, nil
}

func (s *Service) alignmentEnabled(ctx context.Context, managedAnimeID string) (bool, error) {
	if s.alignment == nil || managedAnimeID == "" {
		return false, nil
	}
	return s.alignment.AlignmentEnabled(ctx, managedAnimeID)
}

// linkedCompanions lists the active companions of a video placement. Their
// paths are absolute; none is ever followed.
func linkedCompanions(companions []CompanionFact) []LinkedFile {
	result := make([]LinkedFile, 0, len(companions))
	for _, companion := range companions {
		if companion.State != companionActive {
			continue
		}
		linked := LinkedFile{Role: companionRole(companion.MediaKind), NotAligned: companion.AlignmentFailed}
		if companion.Placement.Path != "" &&
			filemaintenance.CompanionPath(companion.Placement.Path, companion.Suffix, companion.Extension) != companion.Path {
			linked.NotAligned = true
		}
		linked.Path = companion.Path
		linked.Present = regularFileExists(companion.Path)
		result = append(result, linked)
	}
	return result
}

// Locate finds the directory holding one anime's active placements: the work's
// tree top, walked up to the closest directory that exists.
func (s *Service) Locate(ctx context.Context, managedAnimeID string) (Location, error) {
	paths, err := s.store.AnimePlacementPaths(ctx, managedAnimeID)
	if err != nil {
		return Location{}, err
	}
	candidate, ok := worktree.Top(paths)
	if !ok {
		return Location{}, notFound(CodeLocationUnknown, "no browsable directory holds this anime's files")
	}
	for {
		if !s.zone.Denies(candidate) && directoryExists(candidate) {
			return Location{Path: candidate}, nil
		}
		if candidate == string(filepath.Separator) || candidate == "" {
			break
		}
		candidate = filepath.Dir(candidate)
	}
	return Location{}, notFound(CodeLocationUnknown, "no browsable directory holds this anime's files")
}

func absoluteFromSegments(segments []string) string {
	if len(segments) == 0 {
		return ""
	}
	return string(filepath.Separator) + strings.Join(segments, string(filepath.Separator))
}

// PathSpellings returns the browsed absolute path and every spelling owner
// rows may use for it. A writer re-reads the entry through this so its
// revision is computed over the same facts the read used.
func (s *Service) PathSpellings(absolute string) (string, []string, error) {
	cleaned, ok := cleanAbsolute(absolute)
	if !ok {
		return "", nil, invalid(CodePathInvalid, "path must be an absolute path")
	}
	if err := refuseDenied(s.zone, cleaned); err != nil {
		return "", nil, err
	}
	return cleaned, absoluteSpellings(cleaned), nil
}
