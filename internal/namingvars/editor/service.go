// Package editor is the application boundary of the in-app naming Vars
// program editor: the active program and its versions, dry runs against
// sample and real releases, saving, rollback, and import/export. It owns no
// SQL of its own and no copy of the evaluator; namingvars owns validation,
// the version store and evaluation, so a dry run, a save and an acceptance
// read one set of rules.
package editor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"bansoko/internal/apperrors"
	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/templatefacts"
)

// Bounds on one dry run.
const (
	MaxTargets       = 8
	MaxCustomFiles   = 50
	MaxTitleBytes    = 1024
	MaxPathBytes     = 1024
	MaxReleaseOption = 20
)

// Target kinds.
const (
	TargetSample  = "sample"
	TargetRelease = "release"
	TargetCustom  = "custom"
)

// Target statuses.
const (
	StatusOK          = "ok"
	StatusError       = "error"
	StatusInterrupted = "interrupted"
	StatusUnavailable = "unavailable"
)

// Reasons a release target cannot be evaluated.
const (
	ReasonReleaseNotFound    = "release_not_found"
	ReasonInventoryMissing   = "inventory_missing"
	ReasonParserUnavailable  = "parser_unavailable"
	ReasonEvidenceUnreadable = "evidence_unreadable"
)

// Service is the editor application service.
type Service struct {
	db       *sql.DB
	store    namingvars.Store
	releases ReleaseEvidenceLoader
}

// ReleaseEvidenceLoader builds the Vars evidence of one persisted release the
// way the admission paths do. It reports ReasonReleaseNotFound,
// ReasonInventoryMissing or ReasonParserUnavailable through *UnavailableError.
type ReleaseEvidenceLoader interface {
	LoadReleaseEvidence(ctx context.Context, releaseID string) (namingvars.ReleaseEvidence, string, error)
	ListReleases(ctx context.Context, limit int) ([]ReleaseOption, error)
}

// UnavailableError is a release whose evidence cannot be assembled.
type UnavailableError struct{ Reason string }

func (e *UnavailableError) Error() string { return "release evidence unavailable: " + e.Reason }

// NewService wires the editor over the database.
func NewService(db *sql.DB) *Service {
	return &Service{db: db, store: namingvars.NewStore(varspersistence.NewRepository(db)), releases: newReleaseLoader(db)}
}

// NewServiceWith wires the editor over explicit parts (tests).
func NewServiceWith(store namingvars.Store, releases ReleaseEvidenceLoader) *Service {
	return &Service{store: store, releases: releases}
}

// Limits are the bounds a program and its dry run are held to.
type Limits struct {
	MaxSourceBytes int
	MaxOutputBytes int
	MaxCollection  int
	TimeoutMillis  int
	MaxNoteLength  int
	MaxTargets     int
}

// InputFact is one fact a program reads under input.<path>.
type InputFact struct {
	Path           string
	Type           string
	Nullable       bool
	Sample         *string
	LabelKey       string
	DescriptionKey string
}

// SampleInfo describes one built-in sample release.
type SampleInfo struct {
	Ref          string
	ReleaseTitle string
	FileCount    int
}

// ReleaseOption is one persisted release a dry run may use.
type ReleaseOption struct {
	ID         string
	Title      string
	AnimeTitle string
}

// State is everything the editor opens with.
type State struct {
	Active   namingvars.Version
	Versions []namingvars.VersionInfo
	Facts    []InputFact
	Samples  []SampleInfo
	Limits   Limits
}

func currentLimits() Limits {
	return Limits{
		MaxSourceBytes: namingvars.MaxSourceBytes, MaxOutputBytes: namingvars.MaxOutputBytes,
		MaxCollection: namingvars.MaxCollectionLen, TimeoutMillis: int(namingvars.DefaultTimeout / time.Millisecond),
		MaxNoteLength: namingvars.MaxNoteLength, MaxTargets: MaxTargets,
	}
}

// InputFacts lists the registered facts of the Vars input in catalog order.
func InputFacts() []InputFact {
	facts := templatefacts.VarsInputFacts()
	out := make([]InputFact, 0, len(facts))
	for _, fact := range facts {
		item := InputFact{
			Path: fact.Path, Type: string(fact.Type), Nullable: fact.NullableIn(templatefacts.ScenarioNaming),
			LabelKey: fact.LabelKey(), DescriptionKey: fact.DescriptionKey(),
		}
		if sample := templatefacts.FormatSample(fact.SampleFor(templatefacts.ScenarioNaming)); sample != "" {
			item.Sample = &sample
		}
		out = append(out, item)
	}
	return out
}

func sampleInfos() []SampleInfo {
	out := []SampleInfo{}
	for _, sample := range namingvars.Samples() {
		out = append(out, SampleInfo{Ref: sample.Ref, ReleaseTitle: sample.Evidence.ReleaseTitle, FileCount: len(sample.Evidence.Files)})
	}
	return out
}

// GetState returns the active program, the version history and the editor
// reference data.
func (s *Service) GetState(ctx context.Context) (State, error) {
	active, err := s.store.ActiveVersion(ctx)
	if err != nil {
		return State{}, mapStoreError(err)
	}
	versions, err := s.store.Versions(ctx)
	if err != nil {
		return State{}, mapStoreError(err)
	}
	return State{Active: active, Versions: versions, Facts: InputFacts(), Samples: sampleInfos(), Limits: currentLimits()}, nil
}

// GetVersion returns one published version with its source.
func (s *Service) GetVersion(ctx context.Context, revision int64) (namingvars.Version, error) {
	version, err := s.store.VersionByRevision(ctx, revision)
	if err != nil {
		return namingvars.Version{}, mapStoreError(err)
	}
	return version, nil
}

// ListReleases lists recent persisted releases a dry run can use.
func (s *Service) ListReleases(ctx context.Context) ([]ReleaseOption, error) {
	if s.releases == nil {
		return []ReleaseOption{}, nil
	}
	return s.releases.ListReleases(ctx, MaxReleaseOption)
}

// Save publishes source as a new version and activates it without a restart.
func (s *Service) Save(ctx context.Context, source, note string, expected int64) (namingvars.Version, error) {
	return s.publish(ctx, source, note, namingvars.OriginEditor, expected)
}

func (s *Service) publish(ctx context.Context, source, note, origin string, expected int64) (namingvars.Version, error) {
	info, err := s.store.SaveVersion(ctx, source, note, origin, expected)
	if err != nil {
		return namingvars.Version{}, mapStoreError(err)
	}
	version, err := s.store.VersionByRevision(ctx, info.Revision)
	if err != nil {
		return namingvars.Version{}, mapStoreError(err)
	}
	return version, nil
}

// Activate moves the active pointer to a published revision (a rollback is an
// activation of an older revision).
func (s *Service) Activate(ctx context.Context, revision, expected int64) (namingvars.Version, error) {
	info, err := s.store.ActivateRevision(ctx, expected, revision)
	if err != nil {
		return namingvars.Version{}, mapStoreError(err)
	}
	version, err := s.store.VersionByRevision(ctx, info.Revision)
	if err != nil {
		return namingvars.Version{}, mapStoreError(err)
	}
	return version, nil
}

// DryRunTarget is one release a program is evaluated over.
type DryRunTarget struct {
	Kind      string
	Sample    string
	ReleaseID string
	Title     string
	Files     []CustomFile
}

// CustomFile is one file of a typed-in release.
type CustomFile struct {
	Path string
	Size int64
}

// TargetResult is the outcome of one target.
type TargetResult struct {
	Kind       string
	Ref        string
	Title      string
	FileCount  int
	Status     string
	Output     namingvars.Vars
	Diagnostic *namingvars.Diagnostic
	Reason     string
}

// DryRunResult is a dry run of a draft source. A source that fails static
// validation reports Diagnostics and evaluates no target.
type DryRunResult struct {
	Digest      string
	Diagnostics []namingvars.Diagnostic
	Targets     []TargetResult
}

// DryRun evaluates a draft source over sample, real or typed-in releases. It
// writes nothing and is not evidence: acceptance evaluates again.
func (s *Service) DryRun(ctx context.Context, source string, targets []DryRunTarget) (DryRunResult, error) {
	if len(targets) > MaxTargets {
		return DryRunResult{}, invalidInput("targets", fmt.Sprintf("a dry run takes at most %d releases", MaxTargets))
	}
	result := DryRunResult{Diagnostics: []namingvars.Diagnostic{}, Targets: []TargetResult{}}
	if diagnostics := namingvars.Diagnose(source); len(diagnostics) > 0 {
		result.Diagnostics = diagnostics
		return result, nil
	}
	fingerprint, err := namingvars.SourceFingerprint(source)
	if err != nil {
		return DryRunResult{}, err
	}
	result.Digest = fingerprint
	program, err := namingvars.NewProgram(namingvars.ProgramRef{Version: namingvars.ProfileVersion(fingerprint), Digest: fingerprint}, source)
	if err != nil {
		result.Diagnostics = []namingvars.Diagnostic{namingvars.EvaluationDiagnostic(source, err)}
		return result, nil
	}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return DryRunResult{}, err
		}
		item, err := s.evaluateTarget(ctx, program, source, target)
		if err != nil {
			return DryRunResult{}, err
		}
		result.Targets = append(result.Targets, item)
	}
	return result, nil
}

func (s *Service) evaluateTarget(ctx context.Context, program namingvars.Program, source string, target DryRunTarget) (TargetResult, error) {
	result := TargetResult{Kind: target.Kind}
	var evidence namingvars.ReleaseEvidence
	switch target.Kind {
	case TargetSample:
		sample, ok := namingvars.SampleByRef(target.Sample)
		if !ok {
			return result, invalidInput("targets", "unknown sample release")
		}
		evidence, result.Ref = sample.Evidence, sample.Ref
	case TargetCustom:
		title := strings.TrimSpace(target.Title)
		if title == "" || len(title) > MaxTitleBytes {
			return result, invalidInput("targets", "a typed-in release needs a title of up to 1024 bytes")
		}
		if len(target.Files) > MaxCustomFiles {
			return result, invalidInput("targets", fmt.Sprintf("a typed-in release takes at most %d files", MaxCustomFiles))
		}
		files := make([]namingvars.File, 0, len(target.Files))
		for _, file := range target.Files {
			filePath := strings.TrimSpace(file.Path)
			if filePath == "" || len(filePath) > MaxPathBytes || file.Size < 0 {
				return result, invalidInput("targets", "a file needs a path and a non-negative size")
			}
			files = append(files, namingvars.File{Name: path.Base(filePath), Path: filePath, Size: file.Size})
		}
		evidence, result.Ref = namingvars.ReleaseEvidence{ReleaseTitle: title, Files: files}, "custom"
	case TargetRelease:
		id := strings.TrimSpace(target.ReleaseID)
		if id == "" || len(id) > 128 {
			return result, invalidInput("targets", "a release id is required")
		}
		result.Ref = id
		if s.releases == nil {
			result.Status, result.Reason = StatusUnavailable, ReasonEvidenceUnreadable
			return result, nil
		}
		loaded, _, err := s.releases.LoadReleaseEvidence(ctx, id)
		var unavailable *UnavailableError
		if errors.As(err, &unavailable) {
			result.Status, result.Reason = StatusUnavailable, unavailable.Reason
			return result, nil
		}
		if err != nil {
			return result, err
		}
		evidence = loaded
	default:
		return result, invalidInput("targets", "unknown release kind")
	}
	result.Title, result.FileCount = evidence.ReleaseTitle, len(evidence.Files)
	// The same builder and the same evaluation as acceptance.
	frozen, err := program.Dry(namingvars.BuildInput(evidence))
	switch {
	case err == nil:
		result.Status, result.Output = StatusOK, frozen.Output
	case namingvars.IsInterrupted(err):
		diagnostic := namingvars.EvaluationDiagnostic(source, err)
		result.Status, result.Diagnostic = StatusInterrupted, &diagnostic
	default:
		diagnostic := namingvars.EvaluationDiagnostic(source, err)
		result.Status, result.Diagnostic = StatusError, &diagnostic
	}
	return result, nil
}

// Export formats.
const (
	DocumentFormat        = "bansoko-naming-vars"
	DocumentFormatVersion = 1
)

// Document is the portable form of one program: its text plus the metadata
// that identifies it. It carries nothing else: no configuration, secret or
// frozen evidence.
type Document struct {
	Format        string
	FormatVersion int
	Runtime       string
	Digest        string
	Revision      int64
	Note          string
	ExportedAt    time.Time
	Source        string
}

// Export renders the active program as a document. It reads only.
func (s *Service) Export(ctx context.Context) (Document, error) {
	active, err := s.store.ActiveVersion(ctx)
	if err != nil {
		return Document{}, mapStoreError(err)
	}
	return Document{
		Format: DocumentFormat, FormatVersion: DocumentFormatVersion, Runtime: namingvars.ProfileBase,
		Digest: active.Digest, Revision: active.Revision, Note: active.Note, ExportedAt: time.Now().UTC(), Source: active.Source,
	}, nil
}

// Import validates a document and publishes its program as a new version. An
// unsupported or inconsistent document changes nothing.
func (s *Service) Import(ctx context.Context, document Document, expected int64) (namingvars.Version, error) {
	if document.Format != DocumentFormat {
		return namingvars.Version{}, importError("NAMING_VARS_IMPORT_UNSUPPORTED", "format", "this is not a naming Vars export")
	}
	if document.FormatVersion != DocumentFormatVersion {
		return namingvars.Version{}, importError("NAMING_VARS_IMPORT_UNSUPPORTED", "format_version", "this export version is not supported")
	}
	if document.Runtime != namingvars.ProfileBase {
		return namingvars.Version{}, importError("NAMING_VARS_IMPORT_UNSUPPORTED", "runtime", "this program was written for an input version that is no longer supported")
	}
	digest, err := namingvars.SourceFingerprint(document.Source)
	if err != nil {
		return namingvars.Version{}, importError("NAMING_VARS_IMPORT_INVALID", "source", err.Error())
	}
	if digest != document.Digest {
		return namingvars.Version{}, importError("NAMING_VARS_IMPORT_INVALID", "digest", "the program text does not match its digest")
	}
	return s.publish(ctx, document.Source, document.Note, namingvars.OriginImport, expected)
}

func importError(code, field, message string) error {
	return &apperrors.Validation{Code: code, Details: []apperrors.FieldViolation{{Field: &field, Code: code, Message: message}}}
}

func invalidInput(field, message string) error {
	return &apperrors.Validation{Code: "NAMING_VARS_REQUEST_INVALID", Details: []apperrors.FieldViolation{{Field: &field, Code: "NAMING_VARS_REQUEST_INVALID", Message: message}}}
}

// mapStoreError translates namingvars outcomes to the typed application
// errors the transport maps.
func mapStoreError(err error) error {
	var conflict *namingvars.ConflictError
	if errors.As(err, &conflict) {
		return &apperrors.VersionConflict{Owner: "naming_vars", CurrentVersion: conflict.Current.Revision}
	}
	if errors.Is(err, namingvars.ErrVersionNotFound) {
		return &apperrors.NotFound{Resource: "naming_vars_version"}
	}
	var invalid *namingvars.InvalidSourceError
	if errors.As(err, &invalid) {
		for _, diagnostic := range invalid.Diagnostics {
			if diagnostic.Code == namingvars.DiagInterrupted {
				return &apperrors.Business{Status: 503, Code: "NAMING_VARS_EVALUATION_INTERRUPTED", Message: "The program check ran out of time; try again."}
			}
		}
		details := make([]apperrors.FieldViolation, 0, len(invalid.Diagnostics))
		field := "source"
		for _, diagnostic := range invalid.Diagnostics {
			violation := apperrors.FieldViolation{Field: &field, Code: diagnostic.Code, Message: diagnostic.Message}
			if diagnostic.Start >= 0 && diagnostic.End >= diagnostic.Start {
				start, end := diagnostic.Start, diagnostic.End
				violation.Start, violation.End = &start, &end
			}
			details = append(details, violation)
		}
		return &apperrors.Validation{Code: "NAMING_VARS_INVALID", Details: details}
	}
	return err
}
