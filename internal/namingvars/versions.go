package namingvars

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Where a version came from.
const (
	// OriginFile is a program seeded from the deployment file or the bundled
	// defaults. Programs published before the editor existed carry it too.
	OriginFile   = "file"
	OriginEditor = "editor"
	OriginImport = "import"
)

// MaxNoteLength bounds the free-text label a version may carry.
const MaxNoteLength = 200

// VersionListLimit bounds the version history one read returns.
const VersionListLimit = 200

// ErrVersionNotFound reports a revision that was never published.
var ErrVersionNotFound = errors.New("naming vars version not found")

// ErrVersioningUnavailable reports a persistence that cannot version programs.
var ErrVersioningUnavailable = errors.New("naming vars persistence does not support versions")

// InvalidSourceError is a program that failed validation before any write.
type InvalidSourceError struct{ Diagnostics []Diagnostic }

func (e *InvalidSourceError) Error() string {
	if len(e.Diagnostics) == 0 {
		return "naming vars source is invalid"
	}
	return e.Diagnostics[0].Message
}

// ConflictError reports a compare-and-swap on the active version that found
// another version active. Current is the version that is active now.
type ConflictError struct{ Current VersionInfo }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("naming vars active version changed (current revision %d)", e.Current.Revision)
}

// VersionInfo identifies one immutable program version.
type VersionInfo struct {
	// Revision numbers the versions in publication order, from 1. It is the
	// compare-and-swap token of the active pointer.
	Revision int64
	// Digest is the canonical source fingerprint, the program identity every
	// frozen evaluation records.
	Digest      string
	Origin      string
	Note        string
	SourceBytes int
	CreatedAt   time.Time
	ActivatedAt *time.Time
	Active      bool
}

// Ref is the program reference an accepted evaluation freezes.
func (v VersionInfo) Ref() ProgramRef {
	return ProgramRef{Version: ProfileVersion(v.Digest), Digest: v.Digest}
}

// Version is one program version with its source.
type Version struct {
	VersionInfo
	Source string
}

// SaveParams is one publication of a new active version.
type SaveParams struct {
	Source      string
	Fingerprint string
	Origin      string
	Note        string
	// ExpectedRevision is the revision the caller saw active (0: none).
	ExpectedRevision int64
}

// VersionedPersistence is the persistence a Store needs to keep program
// versions and the active pointer. Every write is serialized and moves the
// pointer only when ExpectedRevision is still active.
type VersionedPersistence interface {
	Persistence
	// ActiveVersion returns the active version, or ErrVersionNotFound.
	ActiveVersion(ctx context.Context) (Version, error)
	VersionByRevision(ctx context.Context, revision int64) (Version, error)
	// ListVersions returns the newest versions first, at most limit.
	ListVersions(ctx context.Context, limit int) ([]VersionInfo, error)
	// SaveVersion publishes Source and makes it active when ExpectedRevision
	// is active; a source that is already a version is activated again
	// instead of duplicated. A mismatch returns *ConflictError.
	SaveVersion(ctx context.Context, params SaveParams) (VersionInfo, error)
	// ActivateRevision moves the pointer to a published revision when
	// expected is active; a mismatch returns *ConflictError.
	ActivateRevision(ctx context.Context, expected, target int64) (VersionInfo, error)
	// Seed publishes Source as the first active version, only when none is
	// active. It reports whether it did.
	Seed(ctx context.Context, params SaveParams) (bool, error)
}

func (s Store) versioned() (VersionedPersistence, error) {
	if err := s.unavailable(); err != nil {
		return nil, err
	}
	versioned, ok := s.persistence.(VersionedPersistence)
	if !ok {
		return nil, ErrVersioningUnavailable
	}
	return versioned, nil
}

// Validate checks a source the way every publication does and returns the
// program output keys on the design input. A source that fails here is
// never written, and a source that passes can be started again later.
func Validate(source string) (Vars, []Diagnostic) {
	if diagnostics := Diagnose(source); len(diagnostics) > 0 {
		return nil, diagnostics
	}
	fingerprint, err := SourceFingerprint(source)
	if err != nil {
		return nil, []Diagnostic{{Code: DiagSourceEmpty, Message: err.Error(), Start: -1, End: -1}}
	}
	// The strict run is the semantics every frozen evaluation uses: a missing
	// value without an explicit fallback is refused here, not at accept time.
	program, err := NewProgram(ProgramRef{Version: ProfileVersion(fingerprint), Digest: fingerprint}, source)
	if err != nil {
		return nil, []Diagnostic{EvaluationDiagnostic(source, err)}
	}
	if _, err := program.Evaluate(DesignInput(), 0); err != nil {
		return nil, []Diagnostic{EvaluationDiagnostic(source, err)}
	}
	// The output keys the process knows come from the same run Store.Current
	// repeats after a restart.
	vars, err := Run(source, DesignInput(), 0)
	if err != nil {
		return nil, []Diagnostic{EvaluationDiagnostic(source, err)}
	}
	return vars, nil
}

func validateNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > MaxNoteLength {
		return "", fmt.Errorf("naming vars note exceeds %d characters", MaxNoteLength)
	}
	return note, nil
}

// ActiveVersion reads the active version from persistence.
func (s Store) ActiveVersion(ctx context.Context) (Version, error) {
	versioned, err := s.versioned()
	if err != nil {
		return Version{}, err
	}
	return versioned.ActiveVersion(ctx)
}

// VersionByRevision reads one published version.
func (s Store) VersionByRevision(ctx context.Context, revision int64) (Version, error) {
	versioned, err := s.versioned()
	if err != nil {
		return Version{}, err
	}
	return versioned.VersionByRevision(ctx, revision)
}

// Versions lists the published versions, newest first.
func (s Store) Versions(ctx context.Context) ([]VersionInfo, error) {
	versioned, err := s.versioned()
	if err != nil {
		return nil, err
	}
	return versioned.ListVersions(ctx, VersionListLimit)
}

// SaveVersion validates source, publishes it as a new immutable version and
// makes it active in this process, without a restart. Validation happens
// before any write: an invalid program changes nothing. expected is the
// revision the caller saw active.
func (s Store) SaveVersion(ctx context.Context, source, note, origin string, expected int64) (VersionInfo, error) {
	versioned, err := s.versioned()
	if err != nil {
		return VersionInfo{}, err
	}
	note, err = validateNote(note)
	if err != nil {
		return VersionInfo{}, &InvalidSourceError{Diagnostics: []Diagnostic{{Code: DiagSourceEmpty, Message: err.Error(), Start: -1, End: -1}}}
	}
	vars, diagnostics := Validate(source)
	if len(diagnostics) > 0 {
		return VersionInfo{}, &InvalidSourceError{Diagnostics: diagnostics}
	}
	canonical, err := CanonicalSource(source)
	if err != nil {
		return VersionInfo{}, err
	}
	fingerprint, err := SourceFingerprint(source)
	if err != nil {
		return VersionInfo{}, err
	}
	info, err := versioned.SaveVersion(ctx, SaveParams{Source: string(canonical), Fingerprint: fingerprint, Origin: origin, Note: note, ExpectedRevision: expected})
	if err != nil {
		return VersionInfo{}, err
	}
	if err := SetActive(vars, info.Digest); err != nil {
		return VersionInfo{}, err
	}
	return info, nil
}

// ActivateRevision moves the active pointer to an already published version
// (a rollback is an activation of an older one). The target is checked
// again before the pointer moves, because the rules a program runs under
// may be stricter than when it was saved.
func (s Store) ActivateRevision(ctx context.Context, expected, target int64) (VersionInfo, error) {
	versioned, err := s.versioned()
	if err != nil {
		return VersionInfo{}, err
	}
	version, err := versioned.VersionByRevision(ctx, target)
	if err != nil {
		return VersionInfo{}, err
	}
	vars, diagnostics := Validate(version.Source)
	if len(diagnostics) > 0 {
		return VersionInfo{}, &InvalidSourceError{Diagnostics: diagnostics}
	}
	info, err := versioned.ActivateRevision(ctx, expected, target)
	if err != nil {
		return VersionInfo{}, err
	}
	if err := SetActive(vars, info.Digest); err != nil {
		return VersionInfo{}, err
	}
	return info, nil
}
