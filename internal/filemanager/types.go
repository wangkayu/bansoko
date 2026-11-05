// Package filemanager is the file manager: a container file browser over the
// library directories the owner added, plus the lock that keeps the product's
// own managed files from being changed behind its back.
//
// It browses absolute paths inside the authorized roots (the owner's library
// directories) and nowhere else, except the denial zone (the application's own
// data directories and the kernel's pseudo filesystems), never follows a
// symlink component, reads metadata only (no hashing), and derives one
// ownership state per entry from existing owner facts: placements, companion
// relations, and non-terminal owners. Browsing never writes and observes
// nothing for scans.
//
// P1 commands (commands.go) re-derive that state on every write: release one
// managed video (placement state `released`), admit a user file operation on a
// detached file (owner file_manager_operations, executed by the
// file_manager_execute WorkItem), or align a managed file through the
// existing reconciliation chain.
package filemanager

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

// Root is one library directory the owner added: the file manager reads and
// writes only inside these, and a path outside all of them is neither listed
// nor accepted as a target.
type Root struct {
	ID    string
	Label string
	Path  string
	// Available reports that the directory is still a real directory reached
	// without passing a symbolic link.
	Available   bool
	Unavailable RootUnavailable
}

// RootUnavailable explains why a root cannot be browsed right now.
type RootUnavailable string

const (
	RootMissing      RootUnavailable = "missing"
	RootNotDirectory RootUnavailable = "not_directory"
)

// RootRecord is one root as stored: an absolute container path and the label
// the owner sees.
type RootRecord struct {
	ID    string
	Label string
	Path  string
}

// DenialZone is the set of absolute directory prefixes the browser refuses to
// read or write. The application's own data directories are injected by the
// composition root (they are runtime configuration, not container layout);
// the kernel's pseudo filesystems are process-independent.
type DenialZone struct {
	prefixes []string
}

// NewDenialZone builds the refusal set from the composition root's injected
// application-data directories plus the kernel pseudo filesystems.
func NewDenialZone(applicationDataDirs ...string) DenialZone {
	zone := DenialZone{}
	for _, dir := range applicationDataDirs {
		zone.Add(dir)
	}
	for _, dir := range []string{"/proc", "/sys", "/dev"} {
		zone.Add(dir)
	}
	return zone
}

// Add refuses one prefix and everything below it. Empty values are ignored.
func (z *DenialZone) Add(prefix string) {
	cleaned, ok := cleanAbsolute(prefix)
	if !ok {
		return
	}
	for _, existing := range z.prefixes {
		if existing == cleaned {
			return
		}
	}
	z.prefixes = append(z.prefixes, cleaned)
}

// Denies reports whether an absolute path is inside the refusal set. The path
// itself and every directory above it are checked, so a denial prefix can
// never be reached by descending into it.
func (z DenialZone) Denies(absolute string) bool {
	cleaned, ok := cleanAbsolute(absolute)
	if !ok {
		return true
	}
	for _, prefix := range z.prefixes {
		if cleaned == prefix || strings.HasPrefix(cleaned, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Contains reports whether a directory holds (or is) a refusal prefix. A root
// that contains one would hand out a directory whose own reads are refused, so
// it is not a usable root either.
func (z DenialZone) Contains(absolute string) bool {
	cleaned, ok := cleanAbsolute(absolute)
	if !ok {
		return true
	}
	for _, prefix := range z.prefixes {
		if containsPath(cleaned, prefix) {
			return true
		}
	}
	return false
}

// Prefixes returns the configured refusal prefixes, for diagnostics and tests.
func (z DenialZone) Prefixes() []string {
	return append([]string(nil), z.prefixes...)
}

// EntryKind is the Lstat type of an entry; a symlink is never followed.
type EntryKind string

const (
	KindDirectory EntryKind = "directory"
	KindFile      EntryKind = "file"
	KindSymlink   EntryKind = "symlink"
	KindOther     EntryKind = "other"
)

// State is the single ownership state of an entry.
type State string

const (
	StateInProgress State = "in_progress"
	StateManaged    State = "managed"
	StateDetached   State = "detached"
)

// Role is what owner facts say a file is; empty when no owner row names it.
type Role string

const (
	RoleNone     Role = ""
	RoleVideo    Role = "video"
	RoleSubtitle Role = "subtitle"
	RoleSidecar  Role = "sidecar"
)

// DetachedReason explains a detached state for the detail panel.
type DetachedReason string

const (
	DetachedNoRecord          DetachedReason = "no_record"
	DetachedSubscriptionEnded DetachedReason = "subscription_ended"
	DetachedAnimeRemoved      DetachedReason = "anime_removed"
	DetachedOneShot           DetachedReason = "one_shot"
	DetachedReplaced          DetachedReason = "replaced"
	DetachedCompanionEnded    DetachedReason = "companion_ended"
	// DetachedReleased is a placement whose management a user ended, and the
	// sidecars released with it.
	DetachedReleased DetachedReason = "released"
	// DetachedDisplaced is a placement another file replaced at its path
	// (overwrite setting); DetachedRedownloaded one a user downloaded again,
	// whose new file is elsewhere.
	DetachedDisplaced    DetachedReason = "displaced"
	DetachedRedownloaded DetachedReason = "redownloaded"
)

// InProgressKind names a non-terminal owner that holds a path.
type InProgressKind string

const (
	InProgressFileOperation    InProgressKind = "file_operation"
	InProgressPlacementHandoff InProgressKind = "placement_handoff"
	InProgressAlignment        InProgressKind = "alignment"
	InProgressCleanup          InProgressKind = "cleanup"
	InProgressReplacement      InProgressKind = "replacement"
	InProgressUserOperation    InProgressKind = "user_operation"
)

// Entry is one listed directory entry with its derived state. Path is the
// absolute path the entry is browsed at.
type Entry struct {
	Name       string
	Path       string
	Kind       EntryKind
	SizeBytes  *int64
	ModifiedAt *time.Time
	State      State
	Role       Role
	// FileKind is what the entry's extension says it is (video, subtitle or
	// other). Unlike Role it does not come from owner records, so a plain file
	// is classified the same way as a managed one; empty for directories.
	FileKind string
	// Locked marks a file the product manages (managed or in-progress): its
	// move, rename and delete are refused until a user unlocks it, and a
	// directory whose subtree holds one is refused the same way.
	Locked bool
	// ManagedAnimeID is the work that manages a locked file, when owner facts
	// name one.
	ManagedAnimeID string
	// CompanionOf is the absolute path of the video a bound sidecar follows.
	CompanionOf string
	// NotAligned marks a companion whose path differs from the one derived
	// from its video, or whose last alignment failed.
	NotAligned bool
	// ChildCount is the number of entries directly inside a directory, for the
	// delete confirmation; nil for files and for a directory that cannot be
	// read.
	ChildCount *int64
	// ManagedOnly marks a directory whose every file below, at any depth, is
	// managed or in progress: a browse that hides managed entries has nothing
	// to show inside it, so the folder itself can be hidden too. Set only on
	// directory entries of a listing that asked for it; nil means the answer
	// could not be decided (no file below, unreadable, or over the scan cap).
	ManagedOnly *bool
}

// Listing is one page of a directory.
type Listing struct {
	Path       string
	Entries    []Entry
	NextCursor string
	// Truncated reports that the directory holds more entries than one
	// listing reads; only the first MaxScanEntries are sorted and paged.
	Truncated bool
}

// RootPickerEntry is one subdirectory offered while choosing a library
// directory to add. Only its name and path are read: the picker never derives
// ownership state and never looks inside it.
type RootPickerEntry struct {
	Name string
	Path string
}

// RootPickerListing is one page of the subdirectories of one directory, read
// outside the library roots the owner added (the picker walks from the
// filesystem root to the folder to authorize).
type RootPickerListing struct {
	Path       string
	Entries    []RootPickerEntry
	NextCursor string
	// Truncated reports that the directory holds more entries than one read
	// observes; only its first subdirectories are listed.
	Truncated bool
}

// AnimeRef is the anime that owns (or owned) an entry.
type AnimeRef struct {
	ID                string
	Title             string
	SubscriptionState string
	Ended             bool
}

// InProgressRef is one non-terminal owner holding an entry.
type InProgressRef struct {
	Kind          InProgressKind
	AcquisitionID string
}

// LinkedFile is a file related to an entry: the video a sidecar follows, or a
// sidecar of a video. Path is absolute and always present: a related file is
// named by where it is, and how it is doing is Present.
type LinkedFile struct {
	Path       string
	Present    bool
	Role       Role
	NotAligned bool
}

// Operation names an operation the admission matrix decides for a state. Scan and
// align use the existing reconciliation chain; copy, move, rename and delete
// are user file operations; release ends a placement's management.
type Operation string

const (
	OperationScan    Operation = "scan"
	OperationAlign   Operation = "align"
	OperationCopy    Operation = "copy"
	OperationMove    Operation = "move"
	OperationRename  Operation = "rename"
	OperationDelete  Operation = "delete"
	OperationRelease Operation = "release"
	// OperationMatch matches a detached video to a season and episode of an
	// anime; the result stays detached.
	OperationMatch Operation = "match"
)

// OperationBlock explains why an applicable operation is not allowed.
type OperationBlock string

const (
	BlockNone         OperationBlock = ""
	BlockInProgress   OperationBlock = "in_progress"
	BlockManaged      OperationBlock = "managed"
	BlockFollowsVideo OperationBlock = "follows_video"
	// BlockAlignmentDisabled: the effective file maintenance policy has
	// alignment off, so the alignment consumer would admit nothing.
	BlockAlignmentDisabled OperationBlock = "alignment_disabled"
)

// OperationRule is one applicable operation and whether the state allows it.
type OperationRule struct {
	Operation Operation
	Allowed   bool
	Block     OperationBlock
}

// EpisodeEvidence is the episode identity frozen for one file when its
// acquisition recorded it. It is read as frozen: the file manager never
// re-parses a filename to explain why a file is unrecognized.
type EpisodeEvidence struct {
	// State is the frozen identity state: resolved, unresolved, invalid,
	// unsupported or conflict.
	State string
	// FailureCode is the stable code frozen with an identity that did not
	// resolve.
	FailureCode string
	// RawValue is the raw episode text the frozen identity selected or
	// refused.
	RawValue string
	// Shape is the shape of the captured evidence: single, range, list,
	// season_only, special or unknown.
	Shape string
}

// Detail is the read-only detail of one entry.
type Detail struct {
	Entry          Entry
	Anime          *AnimeRef
	AcquisitionID  string
	DetachedReason DetachedReason
	InProgress     []InProgressRef
	Video          *LinkedFile
	Companions     []LinkedFile
	Operations     []OperationRule
	// Revision is the opaque token of the derived state and file metadata a
	// write must name (CAS).
	Revision string
	// LastOperation is the newest user file operation whose source, target
	// or result is this path.
	LastOperation *UserOperation
	// MatchDefaultAnime is the anime a match of this file starts from: the
	// anime of the placement the file came from, when it still takes new
	// files (a subscription that is active or stopped, not a one-shot).
	MatchDefaultAnime *AnimeRef
	// Episode is the episode identity frozen for this file at acquisition,
	// absent when no frozen identity exists.
	Episode *EpisodeEvidence
}

// Location is where one anime's files are, as an absolute browsable path.
type Location struct {
	Path string
}

// PlacementFact is one placement row with the owner facts its state needs.
type PlacementFact struct {
	ID                string
	Path              string
	State             string
	ManagedAnimeID    string
	AnimeExists       bool
	AnimeTitle        string
	Retired           bool
	SubscriptionState string
	OneShotID         string
	AcquisitionID     string
	Version           int64
	// DisplacedCause is overwrite or redownload for a displaced placement.
	DisplacedCause string
}

// CompanionFact is one companion relation and its owning video placement.
type CompanionFact struct {
	ID              string
	Path            string
	State           string
	MediaKind       string
	Suffix          string
	Extension       string
	AlignmentFailed bool
	Version         int64
	Placement       PlacementFact
}

// InProgressFact is one non-terminal owner path.
type InProgressFact struct {
	Path          string
	Kind          InProgressKind
	AcquisitionID string
}

// PathFacts are the owner facts of a set of exact paths, keyed by path.
type PathFacts struct {
	Placements map[string][]PlacementFact
	Companions map[string][]CompanionFact
	InProgress map[string][]InProgressFact
}

// SubtreeFact summarizes the owner facts strictly below one directory.
type SubtreeFact struct {
	InProgress bool
	Managed    bool
}

// Store reads and maintains the owner facts the file manager is built on.
type Store interface {
	// Roots lists the owner-added library directories, oldest first.
	Roots(ctx context.Context) ([]RootRecord, error)
	// InsertRoot stores one root. ErrRootExists is returned when the path is
	// already a root.
	InsertRoot(ctx context.Context, record RootRecord) error
	// RemoveRoot deletes one root. ErrRootNotFound is returned when no root
	// has that id.
	RemoveRoot(ctx context.Context, id string) error
	PathFacts(ctx context.Context, paths []string) (PathFacts, error)
	SubtreeFacts(ctx context.Context, dirs []string) (map[string]SubtreeFact, error)
	// SubtreeLockedPaths lists the managed or in-progress paths strictly below
	// one directory, up to limit, in byte order.
	SubtreeLockedPaths(ctx context.Context, dir string, limit int) ([]string, error)
	AnimePlacementPaths(ctx context.Context, managedAnimeID string) ([]string, error)
	// PlacementCompanions lists a video placement's companion relations.
	PlacementCompanions(ctx context.Context, placementID string) ([]CompanionFact, error)
	// LatestUserOperation returns the newest user file operation whose
	// source, requested target or result is one of the paths (nil when none).
	LatestUserOperation(ctx context.Context, paths []string) (*UserOperation, error)
	// EpisodeEvidence returns the episode identity frozen for one file. The
	// placement lineage is authoritative when placementID is set; otherwise
	// the file's own artifact lineage at path is read. It returns nil when no
	// frozen identity exists.
	EpisodeEvidence(ctx context.Context, placementID, path string) (*EpisodeEvidence, error)
}
