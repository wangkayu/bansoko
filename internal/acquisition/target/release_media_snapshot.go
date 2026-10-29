package target

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/attrrules"
	"bansoko/internal/batchplan"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
	"bansoko/internal/parser"
	"bansoko/internal/persistence/txscope"
	facts "bansoko/internal/release/facts"
)

// ReleaseMediaSnapshotInput is what every acquisition admission (RSS
// evaluation, one-shot, force download) freezes for one Release: its title,
// the bound metadata work ids, the one ParserEvidence that admission resolved,
// and its batch plan. AniBTMode is the RSS link's reserved auxiliary mode
// (empty for paths without one); it only enters the input digest.
//
// Plan is the admission's final per-file conclusion (deduplicated, target
// conflicts applied). A nil Plan classifies Files here with FileFilter and
// UnclassifiedRoute and no deduplication (force download, an explicit command).
type ReleaseMediaSnapshotInput struct {
	ReleaseID  string
	Title      string
	BGMWorkID  string
	TMDBWorkID string
	AniBTMode  string
	Evidence   ParserEvidence

	Plan              *batchplan.Plan
	Files             []acquisitiondomain.ReleaseFile
	FileFilter        animedomain.FileFilterRules
	UnclassifiedRoute string
	// Selection is the manual file selection a nil Plan classifies under.
	Selection *filekinds.Selection
	// DownloadCompletion is the action a nil Plan freezes (empty = seed).
	DownloadCompletion string
	// DownloadCompletionSeedLimits are the seed limits a nil Plan freezes.
	DownloadCompletionSeedLimits *batchplan.SeedLimits
}

// MaxReleaseMediaFactsBytes is the stored bound of
// media_fact_snapshots.release_facts (pg_column_size <= 2097152). The batch
// plan it holds has one entry per file of the torrent.
const MaxReleaseMediaFactsBytes = 2 << 20

// releaseMediaFactsRawLimit is the raw JSON bound checked before the snapshot
// is written, with the same one eighth of headroom over the jsonb datum the
// frozen task documents keep.
const releaseMediaFactsRawLimit = MaxReleaseMediaFactsBytes - MaxReleaseMediaFactsBytes/8

// batchPlanRawLimit leaves the release's own facts (its title, parser
// evidence and provenance, a few KiB) room next to the plan.
const batchPlanRawLimit = releaseMediaFactsRawLimit - 64<<10

// ErrReleaseMediaFactsTooLarge refuses a release whose batch plan is over the
// stored bound of its media fact snapshot: the torrent lists too many files
// for its download record. Nothing is written.
var ErrReleaseMediaFactsTooLarge = errors.New("release media facts exceed their stored bound")

// BatchPlanTooLarge reports a plan that FreezeReleaseMediaSnapshotTx refuses
// for its size, so an admission can refuse the release before it accepts it.
func BatchPlanTooLarge(plan batchplan.Plan) bool {
	raw, err := json.Marshal(plan)
	return err == nil && len(raw) > batchPlanRawLimit
}

// FreezeReleaseMediaSnapshotTx writes one append-only Release media fact
// snapshot in the caller's admission transaction. It is the only writer of
// that snapshot: artifact inspection resolves every file's own identity with
// the parser, offset and metadata binding frozen here, and prepare refuses a
// Release without it. The release-level identity is the title's evidence only;
// it never becomes a file's identity. The batch plan, with the unknown-file
// route it was accepted under, is frozen with it: prepare, retry and replan
// read this plan, never the live instance switch.
func FreezeReleaseMediaSnapshotTx(ctx context.Context, scope txscope.Tx, store facts.MediaRepository, in ReleaseMediaSnapshotInput) error {
	if store == nil {
		return errors.New("release media fact store is not configured")
	}
	if in.ReleaseID == "" || in.Title == "" {
		return fmt.Errorf("release media fact snapshot needs a release id and title")
	}
	evidence := in.Evidence
	var plan batchplan.Plan
	if in.Plan != nil {
		plan = *in.Plan
	} else {
		built, err := BuildBatchPlan(ctx, BatchPlanInput{Evidence: evidence, Files: in.Files, FileFilter: in.FileFilter, UnclassifiedRoute: in.UnclassifiedRoute, Selection: in.Selection, DownloadCompletion: in.DownloadCompletion, DownloadCompletionSeedLimits: in.DownloadCompletionSeedLimits})
		if err != nil {
			return fmt.Errorf("classify release files: %w", err)
		}
		plan = built
	}
	if plan.UnclassifiedRoute != batchplan.RouteRoute && plan.UnclassifiedRoute != batchplan.RouteSkip {
		return fmt.Errorf("batch plan has no frozen unknown-file route")
	}
	// An unset action freezes as seed; a plan never carries an unknown one.
	if plan.DownloadCompletion == "" {
		plan.DownloadCompletion = batchplan.CompletionSeed
	}
	if !batchplan.ValidCompletionAction(plan.DownloadCompletion) {
		return fmt.Errorf("batch plan has an unknown frozen download completion")
	}
	if err := validSeedLimits(plan.DownloadCompletion, plan.DownloadCompletionSeedLimits); err != nil {
		return fmt.Errorf("batch plan has invalid frozen seed limits: %w", err)
	}
	rules, _, _ := attrrules.Current()
	base := acquisitiondomain.ParseReleaseFacts(in.Title, rules)
	releaseFacts := facts.ReleaseFacts{
		Title:      in.Title,
		GroupName:  base.GroupName,
		Resolution: base.Resolution,
		Quality:    base.Quality,
		Special:    base.Special,
		Warnings:   base.Warnings,
	}
	if base.Language != "" {
		releaseFacts.Languages = []string{base.Language}
	}
	releaseEvidence := parser.Evidence(evidence.Parsed, parser.InputReleaseTitle)
	releaseFacts.EpisodeEvidence = &releaseEvidence
	provenance := evidence.Provenance()
	// The metadata binding this identity resolved with is frozen so per-file
	// recognition and naming never re-read current metadata after admission.
	releaseFacts.MediaKind = provenance.MediaKind
	releaseFacts.Season = provenance.Season
	releaseFacts.SeasonBound = provenance.SeasonBound
	releaseFacts.BGMWorkID = in.BGMWorkID
	releaseFacts.TMDBWorkID = in.TMDBWorkID
	releaseFacts.ParserProvenance = &provenance
	releaseFacts.FileKindsVersion = filekinds.Version
	releaseFacts.BatchPlan = &plan
	if raw, err := json.Marshal(releaseFacts); err == nil && len(raw) > releaseMediaFactsRawLimit {
		return ErrReleaseMediaFactsTooLarge
	}
	return store.CreateReleaseSnapshotTx(ctx, scope, facts.Snapshot{
		ID:                      uuid.NewString(),
		ReleaseID:               in.ReleaseID,
		ResolverVersion:         evidence.Identity.ResolverVersion,
		ParserKind:              string(evidence.Selection.Kind()),
		ParserConfigFingerprint: evidence.Selection.ConfigFingerprint,
		ParserConfig:            evidence.Selection.Parser.Config,
		InputDigest:             ReleaseMediaInputDigest(in),
		ReleaseIdentity:         evidence.Identity,
		ReleaseFacts:            releaseFacts,
	})
}

// ReleaseMediaInputDigest freezes every identity-relevant input: the title,
// the selected parser/profile evidence, the reserved AniBT mode, the metadata
// binding and the one episode offset. Any change produces a different digest.
func ReleaseMediaInputDigest(in ReleaseMediaSnapshotInput) string {
	e := in.Evidence
	return episodeidentity.InputDigest(
		in.Title, e.Parsed.ParserID, e.Parsed.ParserImplementationVersion, e.Parsed.ResultDigest,
		e.Selection.ConfigFingerprint,
		e.Selection.Descriptor.ImplementationVersion, e.Selection.Descriptor.ConfigVersion,
		in.AniBTMode,
		e.MediaKind,
		in.BGMWorkID,
		in.TMDBWorkID,
		fmt.Sprintf("%d", e.Season),
		fmt.Sprintf("%t", e.SeasonBound),
		fmt.Sprintf("%d", e.Selection.Offset),
	)
}
