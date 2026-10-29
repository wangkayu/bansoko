package application

import (
	"bansoko/internal/batchplan"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/episodetitle/namingfacts"
	"bansoko/internal/fileidentity"
	"bansoko/internal/filekinds"
	"bansoko/internal/library/domain"
	"bansoko/internal/organizingpolicy"
	"bansoko/internal/parser"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	facts "bansoko/internal/release/facts"
	"bansoko/internal/workitems"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// artifactPlanInputOverride replaces the plan's configuration authority and
// explicit file selection. It is set only by a replan plan that captured new
// input; a nil override keeps the DownloadTask frozen policy.
type artifactPlanInputOverride struct {
	frozenPolicy json.RawMessage
}

// videoTitleTemplates are the compiled templates the render actually renders:
// the variant the episode-unrecognized route picks (unknown), else the TV or
// movie variant by the effective media kind — the same static selection
// naming.CompiledNamingRules.render applies per render. Only these templates'
// references decide whether an ep_title lookup runs.
func videoTitleTemplates(compiled naming.CompiledNamingRules, unknown, movie bool) []naming.CompiledTemplate {
	variant := compiled.TV
	switch {
	case unknown:
		variant = compiled.Unknown
	case movie:
		variant = compiled.Movie
	}
	return []naming.CompiledTemplate{variant.Directory, variant.VideoFilename}
}

// resolveEpisodeTitles provisions one render's provider episode titles
// (bgm.ep_title, tmdb.ep_title) from the persistent cache (migration 046).
// The library rename render was added to the network-query whitelist by the
// owner decree of 2026-10-22; this is the only fileoperation caller.
//
// Failure semantics: a transient lookup failure — no connection, network
// down, provider outage — returns a plain error wrapping
// episodetitle.ErrTransient. It is deliberately NOT routed through
// insertInvalidPlanTx: a blocked plan would be a terminal-looking stage
// failure for a problem that a retry can fix, while the plan stage's own
// attempt budget already re-enters this preparation (postprocessing_plan_retry)
// and terminalizes with a typed reason only when the budget is exhausted. A
// definite answer — a known title or a definite provider absence — proceeds;
// a definite absence renders the empty fact.
func (s *Service) resolveEpisodeTitles(ctx context.Context, templates []naming.CompiledTemplate, context naming.NamingContext) (namingfacts.Resolution, error) {
	// An unwired Querier is not itself an error: only a template that
	// references a fact needs the store, and only then the resolver reports
	// the missing wiring as transient.
	return s.episodeTitles.Resolve(ctx, templates, context)
}

func (s *Service) prepareForArtifactTxWithOverrides(ctx context.Context, tx *sql.Tx, artifactID string, oneShot *workitems.OneShotRef, override *artifactPlanInputOverride) error {
	if s == nil || (s.db == nil && s.preparedBatch == nil) {
		return errors.New("file operation service is not configured")
	}
	if ctx == nil {
		return errors.New("file operation prepare context is nil")
	}
	if tx == nil && s.preparedBatch == nil {
		return errors.New("file operation prepare transaction is nil")
	}
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return fmt.Errorf("%w: artifact id is required", ErrArtifactNotVerified)
	}
	// Acquire the one-shot marker before reading the artifact owner graph. The
	// row lock is retained by this transaction, so a reclaimer cannot advance
	// the epoch between preparation's owner reads and its WorkItem admission.
	// Every downstream insertion uses the marker-admitted helper below; the same
	// transaction retains this row lock through the owner and WorkItem writes.
	if oneShot != nil && s.preparedBatch == nil {
		if err := workitems.ValidateOneShotMarkerTx(ctx, txscope.Wrap(tx), *oneShot); err != nil {
			return err
		}
	}
	var artifact artifactContext
	var err error
	if s.preparedBatch != nil {
		artifact = s.preparedBatch.artifact
	} else {
		artifact, err = loadArtifactContextTx(ctx, tx, artifactID)
	}
	if err != nil {
		return err
	}
	if artifact.artifactVersion < 1 {
		return fmt.Errorf("%w: artifact version is invalid", ErrArtifactVersionMismatch)
	}
	if len(artifact.files) == 0 {
		return fmt.Errorf("%w: artifact has no files", ErrArtifactFileNotFound)
	}

	var prep prepareContext
	if s.preparedBatch != nil {
		prep = s.preparedBatch.prep
	} else {
		prep = s.prepareContext(ctx, tx, artifact, override)
	}
	prep.oneShot = oneShot

	// P5: frozen episode identities from the media facts snapshot are the only
	// naming/binding episode source; the map is keyed by file_ref. A video
	// without one is an unknown file. The same snapshot row supplies the frozen
	// parser provenance written into every FileOperation config snapshot below.
	frozen := frozenReleaseFacts{episodes: map[string]*naming.FrozenEpisodeNamingFacts{}}
	if s.preparedBatch != nil {
		frozen = s.preparedBatch.frozen
	} else if frozen, err = s.loadFrozenEpisodesTx(ctx, tx, artifact.releaseID); err != nil {
		return err
	}
	// A missing snapshot (or file row) blocks each video with
	// media_facts_missing; it never turns the videos into unknown files.
	prep.videoFacts = frozen.videoRows
	// The release-level resolution frozen with the same snapshot is the
	// accepted-plan fallback for files whose inspection carries none; it is
	// the value the pre-download TargetSet rendered with.
	artifact.frozenReleaseResolution = frozen.releaseResolution
	frozenEpisodes := frozen.episodes
	prep.parserProvenance = frozen.parserProvenance
	// The batch plan frozen at acceptance decides what is placed, and its
	// unknown-file route replaces any live instance switch: retry and replan
	// read the same value the release was accepted under.
	prep.plan = frozen.plan
	if frozen.plan != nil {
		prep.config.unclassifiedRoute = frozen.plan.UnclassifiedRoute
	}
	applyFrozenBinding(&artifact, frozen)

	// Only first-level videos and subtitles exist for planning (see
	// fileSelection); videos are planned first, subtitles bind to a planned
	// video by filename prefix and read its frozen target afterwards. Both
	// lists follow manifest path order so selection and binding never depend
	// on runtime arrival.
	selection := newFileSelection(artifact.frozenInput, frozen.fileKindsVersion, frozen.plan.Selection())
	videos := make([]artifactFileContext, 0, len(artifact.files))
	subtitles := make([]artifactFileContext, 0)
	for _, file := range artifact.files {
		kind, ignored := selection.classify(file)
		if ignored != "" {
			continue
		}
		if kind == filekinds.KindVideo {
			videos = append(videos, file)
		} else {
			subtitles = append(subtitles, file)
		}
	}
	byPath := func(files []artifactFileContext) {
		sort.SliceStable(files, func(i, j int) bool { return files[i].relativePath < files[j].relativePath })
	}
	byPath(videos)
	byPath(subtitles)

	// File rules select videos by filename; the final video set is decided once
	// before binder and operation iteration.
	finalVideos := make([]artifactFileContext, 0, len(videos))
	for _, video := range videos {
		if prep.admitsFile(video.fileRef) && fileAllowedByFilter(video, prep.config.fileFilter) {
			finalVideos = append(finalVideos, video)
		}
	}
	unknownVideos := movieExtras(artifact, finalVideos)
	eligibleVideos := videos
	videos = finalVideos
	// Every video the rules keep must agree with the frozen plan: the
	// inspection row and the plan come from the same frozen parser, so a
	// different coverage key (or a missing plan entry) is drift, not a
	// re-plan. A movie's videos carry the canonical movie identity in their
	// rows while the plan names only the main one, so the check is TV only.
	prep.drift = planDrift(prep.plan, selection, videos, frozen.coverage, artifact.tmdbMediaKind.Valid && artifact.tmdbMediaKind.String == "movie")

	// The binder freezes one decision per subtitle file; rows that already
	// carry a snapshot keep their frozen decision until re-verification.
	// Subtitles are not subject to file rules and follow their video: the
	// strict-prefix match runs over every first-level video, and a subtitle
	// whose video the plan does not carry (excluded by the rules, or not
	// admitted by a replan's explicit selection) is excluded with it, not
	// failed. Only a subtitle that matches no video at all is a binding
	// failure.
	if len(subtitles) > 0 {
		carried := map[string]bool{}
		for _, video := range videos {
			carried[video.fileRef] = true
		}
		videoFacts := make([]bindingFileFacts, 0, len(eligibleVideos))
		for _, video := range eligibleVideos {
			videoFacts = append(videoFacts, fileBindingFacts(video))
		}
		for index, subtitle := range subtitles {
			if !prep.admitsFile(subtitle.fileRef) {
				continue
			}
			if len(subtitle.bindingSnapshot) > 0 {
				if subtitle.boundVideoFileRef.Valid && !carried[subtitle.boundVideoFileRef.String] {
					s.markExcluded(subtitle.fileRef, excludedSubtitleVideoNotRun)
				}
				continue
			}
			decision := decideSubtitleBinding(fileBindingFacts(subtitle), videoFacts)
			if decision.state == bindingStateBound && !carried[decision.boundRef] {
				s.markExcluded(subtitle.fileRef, excludedSubtitleVideoNotRun)
				continue
			}
			if s.preparedBatch != nil {
				d := decision
				s.preparedBatch.bindings = append(s.preparedBatch.bindings, preparedBinding{file: subtitle, decision: &d})
			} else if err := persistBindingDecisionTx(ctx, tx, subtitle, decision); err != nil {
				return err
			}
			snapshot, err := marshalBindingSnapshot(decision, nil)
			if err != nil {
				return fmt.Errorf("marshal subtitle binding snapshot: %w", err)
			}
			subtitles[index].bindingState = decision.state
			subtitles[index].bindingSnapshot = snapshot
			if decision.state == bindingStateBound {
				subtitles[index].boundVideoFileRef = sql.NullString{String: decision.boundRef, Valid: true}
			}
		}
	}

	planned := map[string]plannedVideoTarget{}
	for _, file := range videos {
		if !prep.admitsFile(file.fileRef) {
			continue
		}
		// A video the plan covers elsewhere (an existing or in-flight
		// placement) or dropped for a target conflict is left out on
		// purpose: declared excluded with the plan reason, its subtitles
		// with it.
		if reason := planExclusion(prep.plan, selection, file, prep.drift); reason != "" {
			s.markExcluded(file.fileRef, reason)
			continue
		}
		recordTargetVersion(&prep, selection, file)
		target, err := s.prepareFileTx(ctx, tx, artifact, file, prep, frozenEpisodes[file.fileRef], unknownVideos[file.fileRef])
		if err != nil {
			return err
		}
		if target != nil {
			planned[file.id] = *target
		}
	}
	for _, file := range subtitles {
		if !prep.admitsFile(file.fileRef) {
			continue
		}
		if s.excludedReason(file.fileRef) != "" {
			continue
		}
		// A subtitle follows its video: when that video was left out on
		// purpose (an unknown file under the skip route) the subtitle is left
		// out with it and its binding decision is not declared.
		if file.boundVideoFileRef.Valid && s.excludedReason(file.boundVideoFileRef.String) != "" {
			s.markExcluded(file.fileRef, excludedSubtitleVideoNotRun)
			s.dropPreparedBinding(file.id)
			continue
		}
		// An unbound subtitle is not placed, but it is not silent either:
		// prepareSubtitleFileTx declares the typed subtitle_binding_missing
		// failure for it and leaves every video untouched.
		if err := s.prepareSubtitleFileTx(ctx, tx, artifact, file, videos, planned, prep, frozenEpisodes[file.fileRef]); err != nil {
			return err
		}
	}
	s.excludeDuplicateSubtitleTargets(subtitles)
	return nil
}

// excludeDuplicateSubtitleTargets settles subtitles of one video that render
// one target by order: the first in manifest path order (the order binding
// ties use, never runtime arrival) keeps the target, every later one is left
// out as target_duplicate — excluded with that reason, its binding not
// declared — so the video and the kept subtitle still place. Subtitles of
// different videos sharing a target keep the fail-every-member rule
// (PLAN_NAMING_COLLISION). subtitles must be in manifest path order.
func (s *Service) excludeDuplicateSubtitleTargets(subtitles []artifactFileContext) {
	b := s.preparedBatch
	if b == nil {
		return
	}
	byFile := make(map[string]int, len(b.operations))
	for index, op := range b.operations {
		byFile[op.file.id] = index
	}
	claimed := map[string]bool{}
	drop := map[string]bool{}
	for _, file := range subtitles {
		index, ok := byFile[file.id]
		if !ok {
			continue
		}
		plan := b.operations[index].plan
		if plan.failureCode != "" || plan.boundVideoOperationID == "" || plan.targetPath == "" {
			continue
		}
		key := plan.boundVideoOperationID + "\x00" + plan.targetPath
		if !claimed[key] {
			claimed[key] = true
			continue
		}
		drop[file.id] = true
		s.markExcluded(file.fileRef, batchplan.ReasonTargetDuplicate)
		s.dropPreparedBinding(file.id)
		delete(b.satisfactions, file.fileRef)
	}
	if len(drop) == 0 {
		return
	}
	kept := b.operations[:0]
	for _, op := range b.operations {
		if !drop[op.file.id] {
			kept = append(kept, op)
		}
	}
	b.operations = kept
}

// planDrift marks the TV videos whose inspected identity disagrees with the
// frozen batch plan: no plan entry, a different coverage key set (a
// multi-episode file compares all its keys), or one side resolved while the
// other is not.
// applyFrozenBinding points the naming branch and the bound season at the
// binding frozen on the snapshot.
func applyFrozenBinding(artifact *artifactContext, frozen frozenReleaseFacts) {
	if frozen.hasFrozenBinding {
		// The snapshot freezes the metadata media kind/season binding at
		// evaluation time; after submission the current anime_metadata row
		// must not drift the naming branch (movie vs tv) or the bound season.
		artifact.tmdbMediaKind = sql.NullString{String: frozen.mediaKind, Valid: true}
		artifact.tmdbSeason = frozenSeasonColumn(frozen.season, frozen.seasonBound)
	} else if override := strings.TrimSpace(artifact.ownerMediaKind.String); override == "tv" || override == "movie" {
		// A legacy snapshot without any frozen binding falls back to the
		// current metadata row, exactly like before — but through the same
		// override-first precedence, so the pin still selects the branch.
		artifact.tmdbMediaKind = sql.NullString{String: override, Valid: true}
	}
}

// movieExtras are the videos a movie does not plan as its main video. A
// movie plans exactly its main video (the largest known size, ties and
// unknown sizes by manifest order). Every other eligible video is a plain
// unknown file, handled by the unclassified route like a TV unknown. videos
// are the final videos in manifest path order.
func movieExtras(artifact artifactContext, videos []artifactFileContext) map[string]bool {
	extras := map[string]bool{}
	if !artifact.tmdbMediaKind.Valid || artifact.tmdbMediaKind.String != "movie" {
		return extras
	}
	candidates := make([]organizingpolicy.Video, 0, len(videos))
	for i, video := range videos {
		size := uint64(video.sizeBytes)
		candidates = append(candidates, organizingpolicy.Video{Ref: organizingpolicy.FileRef(video.fileRef), Order: uint32(i), Size: &size, Eligible: true})
	}
	main := organizingpolicy.SelectMovie(candidates).Main
	for _, video := range videos {
		if organizingpolicy.FileRef(video.fileRef) != main {
			extras[video.fileRef] = true
		}
	}
	return extras
}

func planDrift(plan *batchplan.Plan, selection fileSelection, videos []artifactFileContext, coverage map[string]string, movie bool) map[string]bool {
	drift := map[string]bool{}
	if plan == nil || movie {
		return drift
	}
	for _, video := range videos {
		path, ok := selection.manifestPath(video.relativePath)
		if !ok {
			drift[video.fileRef] = true
			continue
		}
		entry, ok := plan.File(path)
		if !ok || entry.Identity == nil || strings.Join(entry.Keys(), ",") != coverage[video.fileRef] {
			drift[video.fileRef] = true
		}
	}
	return drift
}

// planExclusion is the plan reason a video is deliberately not placed:
// covered by an existing or in-flight placement, or dropped for a target
// conflict. A drifted video is never excluded; it fails visibly instead.
func planExclusion(plan *batchplan.Plan, selection fileSelection, video artifactFileContext, drift map[string]bool) string {
	if plan == nil || drift[video.fileRef] {
		return ""
	}
	path, ok := selection.manifestPath(video.relativePath)
	if !ok {
		return ""
	}
	entry, ok := plan.File(path)
	if !ok {
		return ""
	}
	switch entry.Outcome {
	case batchplan.OutcomeCoveredExisting, batchplan.OutcomeCoveredInFlight, batchplan.OutcomeTargetCollision:
		return entry.ReasonCode
	}
	return ""
}

// fileAllowedByFilter applies the file rules to a first-level video. They
// match the filename only, never a directory or the torrent name.
func fileAllowedByFilter(file artifactFileContext, rules fileOperationFileFilterRules) bool {
	compiled, err := animedomain.CompileRegexFilter(animedomain.RegexFilter{MustContain: rules.MustContain, NotIn: rules.NotIn})
	return err == nil && compiled.MatchFilename(file.relativePath).Allowed
}

func (s *Service) prepareContext(ctx context.Context, tx *sql.Tx, artifact artifactContext, override *artifactPlanInputOverride) prepareContext {
	// The admitted DownloadTask's frozen policy is the only configuration
	// authority for a post-download file effect. Reading current globals/instance
	// config here would let a configuration edit made after admission change this
	// operation's filter, naming, or file effect, so a missing/incomplete snapshot
	// fails closed as a visible invalid plan instead of drifting. A replan plan
	// carries its own captured snapshot, which replaces the download-task policy
	// for exactly that plan.
	authority := artifact.frozenPolicy
	if override != nil {
		if len(override.frozenPolicy) > 0 {
			authority = override.frozenPolicy
		}
	}
	config, configErr := effectiveConfigFromFrozenDownloadPolicy(authority)
	// The unknown-file route is the batch plan's frozen value; it is set once
	// the snapshot is read. Until then unknown files fail closed to skip.
	config.unclassifiedRoute = batchplan.RouteSkip
	// Targets are absolute by construction: the naming template carries its own
	// deployment path, so there is no library anchor to resolve. The executor
	// owns the read/write boundary; the app authorizes no location.
	return prepareContext{config: config, configErr: configErr, replanTargets: override != nil && len(override.frozenPolicy) > 0}
}

// plannedVideoTarget is the frozen target of one planned video operation,
// consumed by bound subtitle planning in the same prepare run.
type plannedVideoTarget struct {
	operationID string
	directory   string
	filename    string
}

// plannedTargetFromSnapshot reads the frozen directory/filename of an
// existing operation; nil means the operation has no usable frozen target.
func plannedTargetFromSnapshot(operationID string, snapshot []byte) *plannedVideoTarget {
	var parsed struct {
		Directory string `json:"directory"`
		Filename  string `json:"filename"`
	}
	if json.Unmarshal(snapshot, &parsed) != nil || parsed.Filename == "" {
		return nil
	}
	return &plannedVideoTarget{operationID: operationID, directory: parsed.Directory, filename: parsed.Filename}
}

func (s *Service) prepareFileTx(ctx context.Context, tx *sql.Tx, artifact artifactContext, file artifactFileContext, prep prepareContext, frozenEpisode *naming.FrozenEpisodeNamingFacts, unknownFile bool) (*plannedVideoTarget, error) {
	if s.preparedBatch != nil {
		if existing, ok := s.preparedBatch.existing[file.id]; ok && !repairableBlockedOperation(existing) {
			s.preparedBatch.reused = append(s.preparedBatch.reused, existing)
			return plannedTargetFromSnapshot(existing.id, existing.renderSnapshot), nil
		}
	}
	if s.preparedBatch == nil {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, file.id); err != nil {
			return nil, fmt.Errorf("lock artifact file operation key: %w", err)
		}
		key := stableIdempotencyKey(file.id, artifact.artifactVersion)
		if existing, err := loadOperationByKeyTx(ctx, tx, key); err == nil {
			if !sameOperationInput(existing, artifact, file) {
				return nil, fmt.Errorf("%w: idempotency key is bound to different artifact facts", ErrArtifactVersionMismatch)
			}
			if !repairableBlockedOperation(existing) {
				return plannedTargetFromSnapshot(existing.id, existing.renderSnapshot), s.ensureWorkItemTxAfterMarkerAdmissionTx(ctx, tx, existing, prep.oneShot)
			}
			if err := validateWorkItemMarkersBeforeRepairTx(ctx, tx, existing.id, prep.oneShot); err != nil {
				return nil, err
			}
			// A blocked placeholder freezes only a prepare-time environment/config
			// failure, never an executed effect. Drop it and fall through so a
			// repaired environment re-plans through this same idempotent path
			// instead of retrying the frozen failure forever.
			if err := deleteOperationTx(ctx, tx, existing.id); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}

		if active, err := findActiveOperationTx(ctx, tx, file.id); err != nil {
			return nil, err
		} else if active != nil {
			return nil, fmt.Errorf("%w: %s", ErrActiveOperationConflict, active.id)
		}
	}
	key := stableIdempotencyKey(file.id, artifact.artifactVersion)

	plan := operationPlan{
		id:                uuid.NewString(),
		artifactID:        artifact.artifactID,
		artifactVersion:   artifact.artifactVersion,
		artifactFileID:    file.id,
		operationVersion:  1,
		operation:         string(domain.OperationCopy),
		sourcePath:        file.sourcePath,
		sourceFingerprint: append(json.RawMessage(nil), file.fingerprint...),
		expectedSizeBytes: file.sizeBytes,
		intent:            "ensure_present",
		collisionPolicy:   string(domain.CollisionBlock),
		state:             ownerStatePending,
		idempotencyKey:    key,
		correlationID:     artifact.correlationID.String,
	}

	if prep.configErr != nil {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, prep.configErr)
	}
	if err := prep.mediaFactsErr(file.fileRef); err != nil {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}
	plan.operation = string(prep.config.effect.Operation)
	plan.collisionPolicy, _ = collisionPolicyToDB(prep.config.effect.CollisionPolicy)
	if plan.operation != string(domain.OperationCopy) && plan.operation != string(domain.OperationMove) && plan.operation != string(domain.OperationHardlink) && plan.operation != string(domain.OperationNoop) {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, fmt.Errorf("unsupported file operation %q", plan.operation))
	}
	if plan.collisionPolicy == "" {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, fmt.Errorf("unsupported collision policy %q", prep.config.effect.CollisionPolicy))
	}
	if !validSourcePath(file.sourcePath) {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, ErrInvalidTarget)
	}
	fingerprint, fingerprintErr := decodeFingerprint(file.fingerprint)
	if fingerprintErr != nil ||
		(fingerprintFactUsesSize(fingerprint) && fingerprint.SizeBytes != file.sizeBytes) ||
		(!fingerprint.HasSize && file.sizeBytes > 0) {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, ErrArtifactFingerprintMismatch)
	}
	// A Release-level TargetSet is the frozen naming/effect contract for
	// submitted work. It also lets a mixed multi-file acquisition turn an
	// already satisfied target into a true no-op instead of re-copying it.
	if frozenTarget, skipped, targetErr := s.applyFrozenTargetTx(ctx, tx, artifact, file, &plan, prep); targetErr != nil {
		return nil, targetErr
	} else if skipped {
		return frozenTarget, nil
	} else if frozenTarget != nil {
		return frozenTarget, nil
	}

	var directory, renderedFilename string
	var resolved naming.ResolvedContext
	var err error
	engine := naming.NewEngine()
	if prep.config.vars == nil || prep.config.vars.Status != "ok" {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, fmt.Errorf("frozen Vars evaluation unavailable"))
	}
	compiled, err := engine.CompileRulesWithVars(prep.config.naming, prep.config.vars.Output)
	if err != nil {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}
	resolved, err = buildNamingContext(artifact, file, frozenEpisode, prep.config.vars)
	if err != nil {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}
	// An unknown video is one whose snapshot row carries no resolved episode
	// identity (range/list/season-only/special/fractional/missing): naming
	// never re-derives an episode from a title or filename. A video without a
	// row was already blocked as media_facts_missing above.
	// The unclassified route is a policy switch: route renders the unknown
	// template and the placement owner releases the file at admission; skip
	// (the default) leaves the file untouched. A movie's main video carries the
	// movie identity; its non-main videos (unknownFile) are plain unknown files
	// of the same route.
	movie := artifact.tmdbMediaKind.Valid && artifact.tmdbMediaKind.String == "movie"
	episodeUnrecognized := unknownFile || (file.mediaKind == "video" && !movie && frozenEpisode == nil)
	// The rename render is on the episode-title whitelist: before rendering,
	// a loaded template that reads bgm.ep_title / tmdb.ep_title fills both
	// facts from the persistent cache (only-if-used, per-source states). A
	// transient lookup failure stops the library run at the rename stage —
	// returned off the invalid-plan path so the plan stage's bounded plan
	// retry machinery re-enters this same preparation; a definite answer
	// (known or absent) proceeds, empty title included. The facts must reach
	// the render itself: the filename is the render's output, not a
	// rewritten copy.
	episodeTitles, episodeTitlesErr := s.resolveEpisodeTitles(ctx, videoTitleTemplates(compiled, episodeUnrecognized, movie), resolved.Context)
	if episodeTitlesErr != nil {
		return nil, episodeTitlesErr
	}
	resolved.Context.EpisodeTitles = episodeTitles.Facts
	if episodeUnrecognized {
		// The unclassified route is the frozen instance policy (snapshot at
		// subscribe; never inherited at runtime).
		if prep.config.unclassifiedRoute == "skip" {
			// Left out on purpose: declared excluded, not failed.
			s.markExcluded(file.fileRef, excludedUnknownSkipped)
			return nil, nil
		}
		directory, renderedFilename, err = compiled.RenderUnknown(engine, resolved.Context)
		if err != nil {
			return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
		}
	} else {
		directory, renderedFilename, err = compiled.RenderVideo(engine, resolved.Context)
		if err != nil {
			return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
		}
	}
	plan.targetPath, err = renderedTarget(directory, renderedFilename)
	if err != nil {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}
	if versioned := versionedRenderedTarget(prep, file, plan.targetPath); versioned != plan.targetPath {
		plan.targetPath, renderedFilename = versioned, filepath.Base(versioned)
	}
	if err := validateTargetPath(plan.targetPath); err != nil {
		return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}
	renderSnapshot := map[string]any{
		"renderer_version": rendererVersion,
		"naming_renderer":  naming.RendererVersion,
		"directory":        directory,
		"filename":         renderedFilename,
		"target":           plan.targetPath,
		"resolved":         frozenResolvedValues(resolved.Results),
		"binding_facts":    renderBindingFacts(resolved.Context),
		"render_facts":     naming.ReplayFactValues(resolved.Context),
	}
	// The block lands only when a template referenced an ep_title fact; note
	// 025's render validator accepts snapshots without the key.
	if block := episodeTitles.EpisodeTitlesBlock(); block != nil {
		renderSnapshot["episode_titles"] = block
	}
	if episodeUnrecognized {
		// The placement owner admits this file as an unknown file: released
		// at admission, never part of coverage.
		renderSnapshot["unknown_file"] = true
	}
	plan.renderSnapshot, err = marshalObject(renderSnapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal frozen render snapshot: %w", err)
	}
	plan.configSnapshot, err = marshalConfigSnapshot(prep.config, nil, prep.parserProvenance)
	if err != nil {
		return nil, fmt.Errorf("marshal frozen config snapshot: %w", err)
	}
	// The overwrite collision path needs a frozen execution snapshot (source/
	// target observations with identity) for its crash-recovery checkpoint.
	// It is built only when the target already exists: a missing target means
	// the overwrite degenerates to an absent-target placement, which the plain
	// executor path handles without a checkpoint.
	if plan.collisionPolicy == string(domain.CollisionOverwrite) {
		snapshot, snapshotErr := buildOverwriteExecutionSnapshot(plan, s.parentAuthorizer)
		if snapshotErr != nil {
			return nil, s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, snapshotErr)
		}
		plan.overwriteSnapshot = snapshot
	}
	if err := s.persistOperationPlan(ctx, tx, plan, artifact, file); err != nil {
		if isUniqueViolation(err) {
			if existing, loadErr := loadOperationByKeyTx(ctx, tx, key); loadErr == nil && sameOperationInput(existing, artifact, file) {
				return plannedTargetFromSnapshot(existing.id, existing.renderSnapshot), s.ensureWorkItemTxAfterMarkerAdmissionTx(ctx, tx, existing, prep.oneShot)
			}
		}
		return nil, err
	}
	return &plannedVideoTarget{operationID: plan.id, directory: directory, filename: renderedFilename},
		s.ensureWorkItemTxAfterMarkerAdmissionTx(ctx, tx, operationRecord{
			id:              plan.id,
			artifactID:      plan.artifactID,
			artifactVersion: plan.artifactVersion,
			artifactFileID:  plan.artifactFileID,
			operation:       plan.operation,
			collisionPolicy: plan.collisionPolicy,
			state:           plan.state,
			idempotencyKey:  plan.idempotencyKey,
			version:         plan.operationVersion,
		}, prep.oneShot)
}

// prepareSubtitleFileTx plans one bound subtitle operation. The subtitle never
// renders the video template independently: it reads the frozen bound-video
// target (directory and basename are locked) and appends only the configured
// suffix plus its own original extension.
func (s *Service) prepareSubtitleFileTx(ctx context.Context, tx *sql.Tx, artifact artifactContext, file artifactFileContext, videos []artifactFileContext, planned map[string]plannedVideoTarget, prep prepareContext, frozenEpisode *naming.FrozenEpisodeNamingFacts) error {
	if s.preparedBatch != nil {
		if existing, ok := s.preparedBatch.existing[file.id]; ok && !repairableBlockedOperation(existing) {
			s.preparedBatch.reused = append(s.preparedBatch.reused, existing)
			return nil
		}
	}
	if s.preparedBatch == nil {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, file.id); err != nil {
			return fmt.Errorf("lock artifact file operation key: %w", err)
		}
		key := stableIdempotencyKey(file.id, artifact.artifactVersion)
		if existing, err := loadOperationByKeyTx(ctx, tx, key); err == nil {
			if !sameOperationInput(existing, artifact, file) {
				return fmt.Errorf("%w: idempotency key is bound to different artifact facts", ErrArtifactVersionMismatch)
			}
			if !repairableBlockedOperation(existing) {
				return s.ensureWorkItemTxAfterMarkerAdmissionTx(ctx, tx, existing, prep.oneShot)
			}
			if err := validateWorkItemMarkersBeforeRepairTx(ctx, tx, existing.id, prep.oneShot); err != nil {
				return err
			}
			// Same repair contract as video planning: drop the terminal blocked
			// placeholder and fall through to a full re-plan.
			if err := deleteOperationTx(ctx, tx, existing.id); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		if active, err := findActiveOperationTx(ctx, tx, file.id); err != nil {
			return err
		} else if active != nil {
			return fmt.Errorf("%w: %s", ErrActiveOperationConflict, active.id)
		}
	}
	key := stableIdempotencyKey(file.id, artifact.artifactVersion)

	plan := operationPlan{
		id:                uuid.NewString(),
		artifactID:        artifact.artifactID,
		artifactVersion:   artifact.artifactVersion,
		artifactFileID:    file.id,
		operationVersion:  1,
		operation:         string(domain.OperationCopy),
		sourcePath:        file.sourcePath,
		sourceFingerprint: append(json.RawMessage(nil), file.fingerprint...),
		expectedSizeBytes: file.sizeBytes,
		intent:            "ensure_present",
		collisionPolicy:   string(domain.CollisionBlock),
		state:             ownerStatePending,
		idempotencyKey:    key,
		correlationID:     artifact.correlationID.String,
	}

	if prep.configErr != nil {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, prep.configErr)
	}
	plan.operation = string(prep.config.effect.Operation)
	plan.collisionPolicy, _ = collisionPolicyToDB(prep.config.effect.CollisionPolicy)
	if plan.operation != string(domain.OperationCopy) && plan.operation != string(domain.OperationMove) && plan.operation != string(domain.OperationHardlink) && plan.operation != string(domain.OperationNoop) {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, fmt.Errorf("unsupported file operation %q", plan.operation))
	}
	if plan.collisionPolicy == "" {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, fmt.Errorf("unsupported collision policy %q", prep.config.effect.CollisionPolicy))
	}
	if !validSourcePath(file.sourcePath) {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, ErrInvalidTarget)
	}
	fingerprint, fingerprintErr := decodeFingerprint(file.fingerprint)
	if fingerprintErr != nil ||
		(fingerprintFactUsesSize(fingerprint) && fingerprint.SizeBytes != file.sizeBytes) ||
		(!fingerprint.HasSize && file.sizeBytes > 0) {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, ErrArtifactFingerprintMismatch)
	}
	if bindingErr := subtitleBindingFailure(string(file.bindingState)); bindingErr != nil {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, bindingErr)
	}
	// A saved binding is evidence, not permission to bypass the final video set.
	if !file.boundVideoFileRef.Valid {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, fmt.Errorf("subtitle binding missing"))
	}
	finalBound := false
	for _, video := range videos {
		if video.fileRef == file.boundVideoFileRef.String {
			if _, ok := planned[video.id]; ok {
				finalBound = true
			}
			break
		}
	}
	if !finalBound {
		// The bound video is not planned (an unknown file under the skip
		// route, or a blocked video that owns its own reason): the subtitle
		// follows it and is not placed.
		return nil
	}
	// Missing/ambiguous binding never falls back to the video template: the
	// subtitle operation fails explicitly and the video
	// operations planned earlier in this run are unaffected.
	confidenceToken, confidenceErr := bindingConfidenceToken(file.bindingSnapshot)
	if confidenceErr != nil && file.bindingState == bindingStateBound {
		return confidenceErr
	}
	snapshot, err := decodeBindingSnapshot(file.bindingSnapshot)
	if err != nil {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}
	switch file.bindingState {
	case bindingStateMissing:
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, ErrSubtitleBindingMissing)
	case bindingStateAmbiguous:
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, ErrSubtitleBindingAmbiguous)
	case bindingStateBound:
	default:
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, ErrSubtitleBindingMissing)
	}

	boundRef := strings.TrimSpace(file.boundVideoFileRef.String)
	var videoFile *artifactFileContext
	for index := range videos {
		if videos[index].fileRef == boundRef {
			videoFile = &videos[index]
			break
		}
	}
	if boundRef == "" || videoFile == nil {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep,
			fmt.Errorf("%w: bound video file %q is not in the artifact", ErrBoundTargetMismatch, boundRef))
	}

	// The bound video target comes from this run's plan or from the frozen
	// operation row on an idempotent replay.
	videoTarget, ok := planned[videoFile.id]
	if !ok && s.preparedBatch == nil {
		existing, loadErr := loadOperationByKeyTx(ctx, tx, stableIdempotencyKey(videoFile.id, artifact.artifactVersion))
		if loadErr == nil {
			if frozen := plannedTargetFromSnapshot(existing.id, existing.renderSnapshot); frozen != nil {
				videoTarget = *frozen
				ok = true
			}
		} else if !errors.Is(loadErr, sql.ErrNoRows) {
			return loadErr
		}
	}
	if !ok {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep,
			fmt.Errorf("%w: bound video operation has no frozen target", ErrBoundTargetMismatch))
	}
	lockedBasename := strings.TrimSuffix(videoTarget.filename, filepath.Ext(videoTarget.filename))
	lockedExtension := strings.TrimPrefix(strings.ToLower(filepath.Ext(videoTarget.filename)), ".")

	// Frozen drift guard: a previously locked target that no longer matches
	// the bound video operation fails explicitly, never silently rebinds.
	if snapshot.Locked != nil &&
		(snapshot.Locked.VideoFileRef != boundRef ||
			snapshot.Locked.Directory != videoTarget.directory ||
			snapshot.Locked.Basename != lockedBasename ||
			snapshot.Locked.Extension != lockedExtension) {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, ErrBoundTargetMismatch)
	}
	locked := bindingLockedTarget{
		VideoFileRef:     boundRef,
		VideoOperationID: videoTarget.operationID,
		Directory:        videoTarget.directory,
		Basename:         lockedBasename,
		Extension:        lockedExtension,
	}

	// A Release-level TargetSet already froze this subtitle's target at
	// admission. It is authoritative, but it must sit beside the bound video
	// under the video's basename, and the binding still has to lock the video
	// target: the execution-time dependency gate refuses an unlocked binding.
	if record, found, err := s.frozenTargetRecordTx(ctx, tx, artifact, file, prep); err != nil {
		return err
	} else if found {
		if !frozenSubtitleTargetBesideVideo(record.targetPath, videoTarget, lockedBasename) {
			return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, ErrBoundTargetMismatch)
		}
		plan.boundVideoOperationID = videoTarget.operationID
		_, skipped, targetErr := s.applyFrozenTargetTx(ctx, tx, artifact, file, &plan, prep)
		if targetErr != nil || skipped {
			return targetErr
		}
		return s.lockSubtitleBindingTx(ctx, tx, file, snapshot, locked)
	}

	engine := naming.NewEngine()
	if prep.config.vars == nil || prep.config.vars.Status != "ok" {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, fmt.Errorf("frozen Vars evaluation unavailable"))
	}
	compiled, err := engine.CompileRulesWithVars(prep.config.naming, prep.config.vars.Output)
	if err != nil {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}

	resolved, err := buildNamingContext(artifact, file, frozenEpisode, prep.config.vars)
	if err != nil {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}
	// The subtitle suffix template may read the episode title facts too. Same
	// only-if-used semantics as the video render, including stopping on a
	// transient lookup failure rather than planning an invalid placeholder.
	var episodeTitles namingfacts.Resolution
	if compiled.SubtitleSuffix != nil {
		episodeTitles, err = s.resolveEpisodeTitles(ctx, []naming.CompiledTemplate{*compiled.SubtitleSuffix}, resolved.Context)
		if err != nil {
			return err
		}
		resolved.Context.EpisodeTitles = episodeTitles.Facts
	}
	suffix, err := compiled.RenderSubtitleSuffix(engine, resolved.Context)
	if err != nil {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}
	// The rule lives with the naming engine, so the RSS preview that derives
	// a bound subtitle's target composes the same string (naming.
	// CompanionSidecarFilename); this caller only joins the directory.
	renderedFilename := naming.CompanionSidecarFilename(lockedBasename, suffix, resolved.Context.File.Extension)
	plan.targetPath, err = renderedTarget(videoTarget.directory, renderedFilename)
	if err != nil {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}
	if err := validateTargetPath(plan.targetPath); err != nil {
		return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, err)
	}
	plan.boundVideoOperationID = videoTarget.operationID
	renderSnapshot := map[string]any{
		"renderer_version": rendererVersion,
		"naming_renderer":  naming.RendererVersion,
		"directory":        videoTarget.directory,
		"filename":         renderedFilename,
		"target":           plan.targetPath,
		"resolved":         frozenResolvedValues(resolved.Results),
		"binding_facts":    renderBindingFacts(resolved.Context),
		"render_facts":     naming.ReplayFactValues(resolved.Context),
		"suffix":           suffix,
		"binding": map[string]any{
			"state":                    bindingStateBound,
			"bound_video_file_ref":     boundRef,
			"bound_video_operation_id": videoTarget.operationID,
			"locked_directory":         videoTarget.directory,
			"locked_basename":          lockedBasename,
			"locked_extension":         lockedExtension,
			"method":                   snapshot.Method,
			"confidence":               confidenceToken,
		},
	}
	if block := episodeTitles.EpisodeTitlesBlock(); block != nil {
		renderSnapshot["episode_titles"] = block
	}
	plan.renderSnapshot, err = marshalObject(renderSnapshot)
	if err != nil {
		return fmt.Errorf("marshal frozen render snapshot: %w", err)
	}
	plan.configSnapshot, err = marshalConfigSnapshot(prep.config, nil, prep.parserProvenance)
	if err != nil {
		return fmt.Errorf("marshal frozen config snapshot: %w", err)
	}
	if plan.collisionPolicy == string(domain.CollisionOverwrite) {
		snapshot, snapshotErr := buildOverwriteExecutionSnapshot(plan, s.parentAuthorizer)
		if snapshotErr != nil {
			return s.insertInvalidPlanTx(ctx, tx, plan, artifact, file, prep, snapshotErr)
		}
		plan.overwriteSnapshot = snapshot
	}
	if err := s.persistOperationPlan(ctx, tx, plan, artifact, file); err != nil {
		if isUniqueViolation(err) {
			if existing, loadErr := loadOperationByKeyTx(ctx, tx, key); loadErr == nil && sameOperationInput(existing, artifact, file) {
				return s.ensureWorkItemTxAfterMarkerAdmissionTx(ctx, tx, existing, prep.oneShot)
			}
		}
		return err
	}
	if err := s.lockSubtitleBindingTx(ctx, tx, file, snapshot, locked); err != nil {
		return err
	}
	return s.ensureWorkItemTxAfterMarkerAdmissionTx(ctx, tx, operationRecord{
		id:              plan.id,
		artifactID:      plan.artifactID,
		artifactVersion: plan.artifactVersion,
		artifactFileID:  plan.artifactFileID,
		operation:       plan.operation,
		collisionPolicy: plan.collisionPolicy,
		state:           plan.state,
		idempotencyKey:  plan.idempotencyKey,
		version:         plan.operationVersion,
	}, prep.oneShot)
}

func (s *Service) insertInvalidPlanTx(ctx context.Context, tx *sql.Tx, plan operationPlan, artifact artifactContext, file artifactFileContext, prep prepareContext, reason error) error {
	root := placeholderRoot(file.sourcePath)
	if canonical, err := canonicalRootForPlan(root); err == nil {
		root = canonical
	}
	plan.targetPath = filepath.Join(root, invalidPlaceholderDir, plan.id+".blocked")
	if plan.targetPath == plan.sourcePath {
		plan.targetPath = filepath.Join(root, invalidPlaceholderDir, plan.id+"-blocked")
	}
	plan.state = ownerStateFailed
	plan.failureCode = boundedCode(failureCode(reason), "file_operation_blocked")
	plan.failureReason = safeReason(reason)
	if plan.operation != string(domain.OperationCopy) && plan.operation != string(domain.OperationMove) && plan.operation != string(domain.OperationHardlink) {
		plan.operation = string(domain.OperationCopy)
	}
	if plan.collisionPolicy != string(domain.CollisionBlock) && plan.collisionPolicy != string(domain.CollisionVersion) && plan.collisionPolicy != string(domain.CollisionOverwrite) {
		plan.collisionPolicy = string(domain.CollisionBlock)
	}
	if !isJSONObject(plan.sourceFingerprint) {
		return fmt.Errorf("%w: artifact fingerprint is not an object", ErrArtifactFingerprintMismatch)
	}
	plan.renderSnapshot, _ = marshalObject(map[string]any{
		"renderer_version": rendererVersion,
		"validation": map[string]any{
			"state":  "blocked",
			"code":   plan.failureCode,
			"reason": plan.failureReason,
		},
	})
	plan.configSnapshot, _ = marshalConfigSnapshot(prep.config, map[string]any{
		"code":   plan.failureCode,
		"reason": plan.failureReason,
	}, prep.parserProvenance)
	if err := validateTargetPath(plan.targetPath); err != nil {
		return fmt.Errorf("%w: invalid placeholder target: %v", ErrInvalidTarget, err)
	}
	if err := s.persistOperationPlan(ctx, tx, plan, artifact, file); err != nil {
		if isUniqueViolation(err) {
			if existing, loadErr := loadOperationByKeyTx(ctx, tx, plan.idempotencyKey); loadErr == nil && sameOperationInput(existing, artifact, file) {
				return s.ensureWorkItemTxAfterMarkerAdmissionTx(ctx, tx, existing, prep.oneShot)
			}
		}
		return err
	}
	return s.ensureWorkItemTxAfterMarkerAdmissionTx(ctx, tx, operationRecord{
		id:              plan.id,
		artifactID:      plan.artifactID,
		artifactVersion: plan.artifactVersion,
		artifactFileID:  plan.artifactFileID,
		operation:       plan.operation,
		collisionPolicy: plan.collisionPolicy,
		state:           plan.state,
		idempotencyKey:  plan.idempotencyKey,
		version:         plan.operationVersion,
	}, prep.oneShot)
}

func canonicalFileExecuteInput(operation operationRecord) (json.RawMessage, error) {
	inputValues := map[string]string{
		"operation_id":     operation.id,
		"artifact_id":      operation.artifactID,
		"artifact_file_id": operation.artifactFileID,
	}
	if operation.collisionPolicy == string(domain.CollisionOverwrite) {
		inputValues["recovery_mode"] = string(domain.CollisionOverwrite)
	}
	return marshalObject(inputValues)
}

// expectedFileExecuteInput builds the input snapshot an existing live
// file_execute WorkItem must carry: the canonical operation identity plus, for
// a one-shot prepare run, the exact one-shot marker reference. Merging through
// the store's shared facility keeps an idempotent prepare rerun from stripping
// the marker metadata (or falsely reporting a leased item stale because the
// comparison ignored the reference), while a genuinely different snapshot —
// including a conflicting one-shot reference — still fails.
func expectedFileExecuteInput(operation operationRecord, oneShot *workitems.OneShotRef) (json.RawMessage, error) {
	canonical, err := canonicalFileExecuteInput(operation)
	if err != nil {
		return nil, fmt.Errorf("marshal canonical file execute input: %w", err)
	}
	if oneShot == nil {
		return canonical, nil
	}
	expected, err := workitems.MergeOneShotInput(canonical, *oneShot)
	if err != nil {
		return nil, fmt.Errorf("merge one-shot reference into file execute input: %w", err)
	}
	return expected, nil
}

// ensureWorkItemTx creates or repairs the stable file_execute WorkItem inside
// the caller's transaction. It is the defensive entry point for callers that
// have not already acquired one-shot admission.
func (s *Service) ensureWorkItemTx(ctx context.Context, tx *sql.Tx, operation operationRecord, oneShot *workitems.OneShotRef) error {
	return s.ensureWorkItemTxWithAdmission(ctx, tx, operation, oneShot, false)
}

// ensureWorkItemTxAfterMarkerAdmissionTx is used by artifact preparation after
// the marker was locked before the artifact/owner graph was read. The marker
// row lock is retained until tx ends, so this path must not issue a second
// SELECT FOR UPDATE on the same marker.
func (s *Service) ensureWorkItemTxAfterMarkerAdmissionTx(ctx context.Context, tx *sql.Tx, operation operationRecord, oneShot *workitems.OneShotRef) error {
	return s.ensureWorkItemTxWithAdmission(ctx, tx, operation, oneShot, true)
}

func (s *Service) ensureWorkItemTxWithAdmission(ctx context.Context, tx *sql.Tx, operation operationRecord, oneShot *workitems.OneShotRef, markerLocked bool) error {
	if s.preparedBatch != nil {
		return nil
	}
	// One-shot downstream admission is marker-first. Lock and validate the
	// marker before even reading the existing WorkItem so a reclaimed/missing
	// marker cannot observe, repair, or otherwise mutate its live item. The row
	// lock is retained by tx for both the existing and new-item paths.
	if oneShot != nil && !markerLocked {
		if err := workitems.ValidateOneShotMarkerTx(ctx, txscope.Wrap(tx), *oneShot); err != nil {
			return err
		}
	}

	var id, state string
	var snapshot []byte
	var existingKind string
	var existingErr error
	existingErr = workitems.LockLatestFileExecuteTx(ctx, tx, workitems.OwnerFileOperation, operation.id, workitems.KindFileExecute).Scan(&id, &state, &existingKind, &snapshot)
	if existingErr == nil {
		if existingKind != workitems.KindFileExecute {
			return fmt.Errorf("file execute work item kind changed")
		}
		existingOneShot, parseErr := workitems.ParseOneShotRef(json.RawMessage(snapshot))
		if parseErr != nil {
			return fmt.Errorf("parse existing file execute one-shot reference: %w", parseErr)
		}
		if oneShot == nil && existingOneShot != nil {
			// An ordinary prepare must never turn a previously gated item into an
			// ordinary one by rebuilding its snapshot without the marker.
			return fmt.Errorf("%w: existing file execute input declares a one-shot marker", workitems.ErrInvalidOneShotClaim)
		}
		if oneShot != nil && existingOneShot != nil && *existingOneShot != *oneShot {
			// A valid marker reference is part of the frozen effect identity. A
			// different reference is not an available-item repair, and must also
			// be rejected for leased items rather than being treated as stale
			// input or silently rebound.
			return fmt.Errorf("%w: existing file execute input carries a different one-shot reference", workitems.ErrInvalidSpec)
		}
		expected, expectedErr := expectedFileExecuteInput(operation, oneShot)
		if expectedErr != nil {
			return expectedErr
		}
		if state == workitems.StateLeased {
			if !jsonEqual(snapshot, expected) {
				return errors.New("leased file execute work item has a stale input snapshot")
			}
		} else if state == workitems.StateAvailable && !jsonEqual(snapshot, expected) {
			if updateErr := workitems.UpdateAvailableInputTx(ctx, tx, id, expected); updateErr != nil {
				return fmt.Errorf("repair file execute input snapshot: %w", updateErr)
			}
		}
		if operation.state == ownerStateFailed && state == workitems.StateAvailable {
			return terminalizeWorkItemTx(ctx, tx, id, operation.failureCode.String, operation.failureReason.String)
		}
		return nil
	}
	if !errors.Is(existingErr, sql.ErrNoRows) {
		return fmt.Errorf("read file execute work item: %w", existingErr)
	}
	input, err := canonicalFileExecuteInput(operation)
	if err != nil {
		return fmt.Errorf("marshal file execute input: %w", err)
	}
	store := &workitems.Store{RetentionDays: workitems.DefaultRetentionDays}
	spec := workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerFileOperation, ID: operation.id},
		Kind:                 workitems.KindFileExecute,
		IdempotencyKey:       operation.idempotencyKey,
		InputSnapshot:        input,
		MaxAttempts:          defaultMaxAttempts,
		ExpectedOwnerVersion: maxVersion(operation.version),
	}
	var workID string
	if oneShot != nil {
		// The marker is already locked and validated above. Keep that lock while
		// using the regular merge primitive; setting OneShot preserves the typed
		// snapshot merge without issuing a second marker SELECT FOR UPDATE.
		spec.OneShot = oneShot
		workID, _, err = store.CreateOrMerge(ctx, tx, spec)
	} else {
		workID, err = store.Create(ctx, tx, spec)
	}
	if errors.Is(err, workitems.ErrLiveItemConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	if operation.state == ownerStateFailed {
		return terminalizeWorkItemTx(ctx, tx, workID, operation.failureCode.String, operation.failureReason.String)
	}
	return nil
}

// frozenResolvedValues projects resolver results into the frozen render
// snapshot with bounded value/evidence summaries.
func frozenResolvedValues(results []naming.ResolverResult) []map[string]any {
	frozen := make([]map[string]any, 0, len(results))
	for _, result := range results {
		entry := map[string]any{
			"key":   result.Key,
			"state": result.State,
		}
		if summary := naming.SummarizeResolverValue(result); summary != "" {
			entry["value"] = summary
		}
		if summary := naming.SummarizeResolverEvidence(result.Evidence); summary != "" {
			entry["evidence"] = summary
		}
		frozen = append(frozen, entry)
	}
	return frozen
}

func terminalizeWorkItemTx(ctx context.Context, tx *sql.Tx, workItemID, reasonCode, reason string) error {
	if err := workitems.FailAvailableFileOperationTx(ctx, tx, workItemID, boundedCode(reasonCode, "file_operation_blocked"), safeString(reason)); err != nil {
		return fmt.Errorf("terminalize file execute work item: %w", err)
	}
	return nil
}

func loadArtifactContextTx(ctx context.Context, tx *sql.Tx, artifactID string) (artifactContext, error) {
	return loadArtifactContextWithLocks(ctx, tx, artifactID, true)
}

func loadArtifactContextWithLocks(ctx context.Context, tx *sql.Tx, artifactID string, lock bool) (artifactContext, error) {
	ownerLock, fileLock := "", ""
	if lock {
		ownerLock = " FOR SHARE OF a, t, r, src, sub, anime, m"
		fileLock = " FOR SHARE"
	}
	var result artifactContext
	var state string
	err := tx.QueryRowContext(ctx, `
SELECT a.id, a.version, a.download_task_id, a.state, a.correlation_id,
       t.state, t.frozen_input, t.frozen_policy,
       r.id, r.version, r.stable_item_key, r.raw_facts, r.source_facts, r.parsed_facts,
       src.id, src.source_kind, src.group_name, COALESCE(src.provider, ''), COALESCE(src.provider_group_ref, ''),
       anime.id, anime.title, anime.title_override, COALESCE(anime.provider_title, ''),
       m.bgm_work_id, m.tmdb_work_id, m.tmdb_media_kind, m.tmdb_season, m.media_kind
FROM artifacts a
JOIN download_tasks t ON t.id = a.download_task_id
JOIN releases r ON r.id = t.release_id
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
JOIN managed_anime anime ON anime.id = sub.managed_anime_id
JOIN anime_metadata m ON m.managed_anime_id = anime.id
WHERE a.id = $1
`+ownerLock, artifactID).Scan(
		&result.artifactID, &result.artifactVersion, &result.downloadTaskID, &state, &result.correlationID,
		&result.taskState, &result.frozenInput, &result.frozenPolicy,
		&result.releaseID, &result.releaseVersion, &result.stableItemKey, &result.rawFacts, &result.sourceFacts, &result.parsedFacts,
		&result.sourceID, &result.sourceKind, &result.groupName, &result.sourceProvider, &result.sourceGroupRef,
		&result.managedAnimeID, &result.animeTitle, &result.titleOverride, &result.providerTitle,
		&result.metadataBGM, &result.tmdbWorkID, &result.tmdbMediaKind, &result.tmdbSeason, &result.ownerMediaKind)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return artifactContext{}, fmt.Errorf("%w: %q", ErrOperationNotFound, artifactID)
		}
		return artifactContext{}, fmt.Errorf("read artifact context: %w", err)
	}
	if state != "verified" {
		return artifactContext{}, fmt.Errorf("%w: artifact state is %q", ErrArtifactNotVerified, state)
	}
	rows, err := tx.QueryContext(ctx, `
SELECT id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts,
       subtitle_binding_state, bound_video_file_ref, subtitle_binding_snapshot
FROM artifact_files
WHERE artifact_id = $1
ORDER BY file_ref, id
`+fileLock, artifactID)
	if err != nil {
		return artifactContext{}, fmt.Errorf("read artifact files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var file artifactFileContext
		if err := rows.Scan(&file.id, &file.fileRef, &file.relativePath, &file.sourcePath, &file.mediaKind, &file.sizeBytes, &file.fingerprint, &file.facts,
			&file.bindingState, &file.boundVideoFileRef, &file.bindingSnapshot); err != nil {
			return artifactContext{}, fmt.Errorf("scan artifact file: %w", err)
		}
		result.files = append(result.files, file)
	}
	if err := rows.Err(); err != nil {
		return artifactContext{}, fmt.Errorf("iterate artifact files: %w", err)
	}
	return result, nil
}

func loadOperationByKeyTx(ctx context.Context, tx *sql.Tx, key string) (operationRecord, error) {
	var record operationRecord
	return record, scanOperation(tx.QueryRowContext(ctx, operationQuery+" WHERE idempotency_key = $1 FOR UPDATE NOWAIT", key), &record)
}

// repairableBlockedOperation reports whether the existing row is a blocked
// placeholder in its terminal failed state. Only terminal rows are dropped:
// a blocked row in any other state is mid-repair (for example a manual retry
// reset) and keeps the ordinary idempotent replay path.
func repairableBlockedOperation(operation operationRecord) bool {
	return operation.state == ownerStateFailed && invalidSnapshot(operation)
}

// validateWorkItemMarkersBeforeRepairTx checks every WorkItem that would be
// removed with a blocked placeholder before deleteOperationTx runs. Ordinary
// prepare must not silently strip a one-shot marker while repairing a blocked
// operation, and a one-shot retry must not rebind an existing marker to a
// different acquisition. The rows are locked so the checked set is stable
// until the enclosing transaction either deletes it or rolls back.
func validateWorkItemMarkersBeforeRepairTx(ctx context.Context, tx *sql.Tx, operationID string, oneShot *workitems.OneShotRef) error {
	rows, err := workitems.LockFileOperationInputsTx(ctx, tx, workitems.OwnerFileOperation, operationID)
	if err != nil {
		return fmt.Errorf("read file execute work items before repair: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var snapshot []byte
		if err := rows.Scan(&snapshot); err != nil {
			return fmt.Errorf("scan file execute work item before repair: %w", err)
		}
		existingOneShot, parseErr := workitems.ParseOneShotRef(json.RawMessage(snapshot))
		if parseErr != nil {
			return fmt.Errorf("parse existing file execute one-shot reference before repair: %w", parseErr)
		}
		if oneShot == nil && existingOneShot != nil {
			return fmt.Errorf("%w: blocked file execute input declares a one-shot marker", workitems.ErrInvalidOneShotClaim)
		}
		if oneShot != nil && existingOneShot != nil && *existingOneShot != *oneShot {
			return fmt.Errorf("%w: blocked file execute input carries a different one-shot reference", workitems.ErrInvalidSpec)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate file execute work items before repair: %w", err)
	}
	return nil
}

// deleteOperationTx removes one blocked placeholder operation together with
// its work items (the only FK references a file operation can carry). A
// placeholder never executed an effect, so dropping it loses no frozen
// execution facts and lets this prepare run re-plan the file after the
// blocking environment/config condition was repaired.
func deleteOperationTx(ctx context.Context, tx *sql.Tx, operationID string) error {
	if err := workitems.DeleteFileOperationWorkItemsTx(ctx, tx, workitems.OwnerFileOperation, operationID); err != nil {
		return fmt.Errorf("delete blocked file operation work items: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM file_operations
WHERE id = $1 AND state = $2`, operationID, ownerStateFailed); err != nil {
		return fmt.Errorf("delete blocked file operation: %w", err)
	}
	return nil
}

func loadOperationTx(ctx context.Context, tx *sql.Tx, id string) (operationRecord, error) {
	var record operationRecord
	return record, scanOperation(tx.QueryRowContext(ctx, operationQuery+" WHERE id = $1 FOR UPDATE NOWAIT", id), &record)
}

func findActiveOperationTx(ctx context.Context, tx *sql.Tx, artifactFileID string) (*operationRecord, error) {
	var record operationRecord
	err := scanOperation(tx.QueryRowContext(ctx, operationQuery+`
WHERE artifact_file_id = $1 AND state IN ('pending', 'running', 'waiting_retry')
ORDER BY created_at, id
LIMIT 1
FOR UPDATE NOWAIT`, artifactFileID), &record)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

const operationQuery = `
SELECT id, artifact_id, artifact_version, artifact_file_id, operation,
       source_path, target_path, source_fingerprint, target_fingerprint,
       expected_size_bytes, observed_target_size_bytes, intent, collision_policy,
       frozen_render_snapshot, frozen_config_snapshot,
       overwrite_execution_snapshot, publication_checkpoint, state, observed_at, failure_code, failure_reason,
       idempotency_key, version, bound_video_operation_id
FROM file_operations`

func scanOperation(row interface{ Scan(...any) error }, record *operationRecord) error {
	var overwriteSnapshot, publicationCheckpoint sql.NullString
	if err := row.Scan(
		&record.id, &record.artifactID, &record.artifactVersion, &record.artifactFileID, &record.operation,
		&record.sourcePath, &record.targetPath, &record.sourceFingerprint, &record.targetFingerprint,
		&record.expectedSizeBytes, &record.observedTargetSize, &record.intent,
		&record.collisionPolicy, &record.renderSnapshot, &record.configSnapshot,
		&overwriteSnapshot, &publicationCheckpoint, &record.state, &record.observedAt, &record.failureCode, &record.failureReason,
		&record.idempotencyKey, &record.version, &record.boundVideoOperationID); err != nil {
		return err
	}
	if overwriteSnapshot.Valid {
		record.overwriteSnapshot = []byte(overwriteSnapshot.String)
	} else {
		record.overwriteSnapshot = nil
	}
	if publicationCheckpoint.Valid {
		record.publicationCheckpoint = []byte(publicationCheckpoint.String)
	} else {
		record.publicationCheckpoint = nil
	}
	return nil
}

func sameOperationInput(operation operationRecord, artifact artifactContext, file artifactFileContext) bool {
	return operation.artifactID == artifact.artifactID &&
		operation.artifactVersion == artifact.artifactVersion &&
		operation.artifactFileID == file.id &&
		operation.sourcePath == file.sourcePath &&
		jsonEqual(operation.sourceFingerprint, file.fingerprint)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// frozenReleaseFacts is the frozen Release evidence one prepare run consumes:
// the per-file episode identities plus the parser provenance frozen on the same
// media fact snapshot row.
type frozenReleaseFacts struct {
	episodes         map[string]*naming.FrozenEpisodeNamingFacts
	parserProvenance *parser.Provenance
	// mediaKind/season are the metadata binding frozen on the snapshot
	// ("movie" selects the movie naming branch); hasFrozenBinding is false for
	// a legacy snapshot written before the binding was frozen, in which case
	// the naming context keeps reading the current metadata row.
	mediaKind        string
	season           int
	seasonBound      bool
	hasFrozenBinding bool
	// releaseResolution is the release-title resolution frozen on the same
	// snapshot at evaluation (ParseReleaseFacts under the then-current
	// attribute rules) — the exact value the TargetSet render used.
	releaseResolution string
	// fileKindsVersion is the file kind list version frozen on the snapshot;
	// empty (a snapshot written before it was frozen) reads the current list.
	fileKindsVersion string
	// videoRows holds every video file ref the snapshot has a row for,
	// resolved or not; nil means the Release has no snapshot.
	videoRows map[string]bool
	// coverage is each video row's resolved coverage keys joined by "," (absent when the
	// identity is unresolved): what the drift check compares with the plan.
	coverage map[string]string
	// plan is the batch plan frozen with the snapshot; nil when it has none.
	plan *batchplan.Plan
}

// loadFrozenEpisodesTx reads the release's media fact snapshot file rows and
// projects each resolved video identity into the naming/binding frozen
// episode shape. Unresolved identities and non-video files are skipped: such a
// video is an unknown file. The snapshot's frozen parser provenance
// travels with the same result so the frozen FileOperation config snapshot names
// the parser that produced the episodes; a row that predates the frozen
// provenance reconstructs only the provable same-source kind + fingerprint and
// never invents owner versions, digests, or the typed parse result.
func (s *Service) loadFrozenEpisodesTx(ctx context.Context, tx *sql.Tx, releaseID string) (frozenReleaseFacts, error) {
	result := frozenReleaseFacts{episodes: map[string]*naming.FrozenEpisodeNamingFacts{}}
	if s.mediaFacts == nil || strings.TrimSpace(releaseID) == "" {
		return result, nil
	}
	var snapshotID string
	var parserKind, parserFingerprint sql.NullString
	var releaseFactsRaw, releaseIdentityRaw []byte
	err := tx.QueryRowContext(ctx, `
SELECT id, parser_kind, parser_config_fingerprint, release_facts, release_identity
FROM media_fact_snapshots
WHERE release_id = $1
ORDER BY created_at DESC, id DESC
LIMIT 1`, releaseID).Scan(&snapshotID, &parserKind, &parserFingerprint, &releaseFactsRaw, &releaseIdentityRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("read release media fact snapshot: %w", err)
	}
	var releaseFacts struct {
		Resolution       string          `json:"resolution"`
		FileKindsVersion string          `json:"file_kinds_version"`
		BatchPlan        *batchplan.Plan `json:"batch_plan"`
	}
	if json.Unmarshal(releaseFactsRaw, &releaseFacts) == nil {
		result.releaseResolution = strings.TrimSpace(releaseFacts.Resolution)
		result.fileKindsVersion = strings.TrimSpace(releaseFacts.FileKindsVersion)
		result.plan = releaseFacts.BatchPlan
	}
	if kind := strings.TrimSpace(parserKind.String); kind != "" {
		result.parserProvenance = frozenParserProvenance(parser.ParserKind(kind), strings.TrimSpace(parserFingerprint.String), releaseFactsRaw, releaseIdentityRaw)
	}
	if result.parserProvenance != nil && strings.TrimSpace(result.parserProvenance.MediaKind) != "" {
		// The binding frozen on the snapshot is the naming authority: a
		// metadata edit after submission must not drift the render.
		result.mediaKind = result.parserProvenance.MediaKind
		result.season = result.parserProvenance.Season
		result.seasonBound = result.parserProvenance.SeasonBound
		result.hasFrozenBinding = true
	} else {
		result.mediaKind, result.season, result.seasonBound = facts.DecodeFrozenMediaBinding(releaseFactsRaw)
		// A legacy snapshot without any frozen binding evidence falls back to
		// the current metadata row at the caller, exactly like before.
		result.hasFrozenBinding = false
	}
	rows, err := s.mediaFacts.ListFileRowsTx(ctx, txscope.Wrap(tx), snapshotID)
	if err != nil {
		return result, fmt.Errorf("read release media fact file rows: %w", err)
	}
	result.videoRows = map[string]bool{}
	result.coverage = map[string]string{}
	for _, row := range rows {
		if row.MediaKind != "video" {
			continue
		}
		result.videoRows[row.FileRef] = true
		identity := row.EffectiveFacts.Identity
		if identity.State == "resolved" && len(identity.Keys()) > 0 {
			result.coverage[row.FileRef] = strings.Join(identity.Keys(), ",")
		}
		if identity.State != "resolved" || identity.Episode == nil {
			continue
		}
		result.episodes[row.FileRef] = &naming.FrozenEpisodeNamingFacts{
			Raw:     rawEpisodeCapture(identity),
			Number:  string(*identity.Episode),
			Offset:  identity.Offset,
			Display: string(*identity.Episode),
			Source:  frozenEpisodeSource(identity),
		}
		if identity.EpisodeEnd != nil {
			result.episodes[row.FileRef].NumberEnd = string(*identity.EpisodeEnd)
		}
	}
	return result, nil
}

// frozenEpisodeSource carries the identity's selected evidence source into
// the frozen naming facts, so ep.source names the evidence this episode came
// from here exactly as it does on the maintenance path. An unresolved
// identity never reaches here.
func frozenEpisodeSource(identity episodeidentity.EpisodeIdentity) string {
	if identity.SelectedSource == nil {
		return ""
	}
	return string(*identity.SelectedSource)
}

// rawEpisodeCapture returns the raw pre-offset capture for diagnostics; the
// resolver preserves it as the identity raw episode when available.
func rawEpisodeCapture(identity episodeidentity.EpisodeIdentity) string {
	if identity.RawEpisode != nil {
		return string(*identity.RawEpisode)
	}
	if identity.Episode != nil {
		return string(*identity.Episode)
	}
	return ""
}

// frozenParserProvenance returns the parser evidence frozen on a media fact
// snapshot. The full frozen `release_facts.parser_provenance` is preferred; a
// legacy snapshot reconstructs only the provable kind/fingerprint descriptor
// plus the frozen identity offset, never the owner versions, digests, or typed
// parse result it never stored.
func frozenParserProvenance(kind parser.ParserKind, fingerprint string, releaseFactsRaw, releaseIdentityRaw []byte) *parser.Provenance {
	var frozen struct {
		ParserProvenance *parser.Provenance `json:"parser_provenance"`
	}
	if len(releaseFactsRaw) > 0 && string(releaseFactsRaw) != "null" {
		if json.Unmarshal(releaseFactsRaw, &frozen) == nil && frozen.ParserProvenance != nil {
			return frozen.ParserProvenance
		}
	}
	provenance := parser.DescribeProvenance(kind, fingerprint)
	var identity episodeidentity.EpisodeIdentity
	if len(releaseIdentityRaw) > 0 && string(releaseIdentityRaw) != "null" {
		if json.Unmarshal(releaseIdentityRaw, &identity) == nil {
			provenance.Offset = identity.Offset
		}
	}
	return &provenance
}

// buildOverwriteExecutionSnapshot freezes the source/target observations for
// the overwrite checkpoint. The source is the verified artifact file (must
// exist); the target must already exist (otherwise overwrite degenerates to an
// absent-target placement and no checkpoint is needed). Returns nil when the
// target is absent.
func buildOverwriteExecutionSnapshot(plan operationPlan, authorizer ParentIdentityAuthorizer) (json.RawMessage, error) {
	canonicalRoot, canonicalSource, canonicalTarget, stagingPath, err := overwriteExecutionPaths(plan)
	if err != nil {
		return nil, err
	}
	sourceObservation, err := observeFileObservation(canonicalSource)
	if err != nil {
		return nil, fmt.Errorf("observe overwrite source %s: %w", canonicalSource, err)
	}
	planFingerprint, err := decodeFingerprint(plan.sourceFingerprint)
	if err != nil || !sameFingerprint(sourceObservation.Fingerprint, planFingerprint.Value) ||
		(fingerprintFactUsesSize(planFingerprint) && sourceObservation.SizeBytes != planFingerprint.SizeBytes) {
		return nil, fmt.Errorf("%w: overwrite source changed before checkpoint preparation", ErrArtifactFingerprintMismatch)
	}
	targetObservation, err := observeFileObservation(canonicalTarget)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // absent target: plain absent-target execution
	}
	if err != nil {
		return nil, fmt.Errorf("observe overwrite target %s: %w", canonicalTarget, err)
	}
	if authorizer == nil {
		return nil, errors.New("overwrite parent identity authorizer is unavailable")
	}
	sourceParentIdentity, targetParentIdentity, err := authorizer.ParentIdentities(canonicalRoot, canonicalSource, canonicalTarget)
	if err != nil {
		return nil, fmt.Errorf("authorize overwrite parent identities: %w", err)
	}
	recovery := domain.OverwriteRecovery{
		Stage:                domain.OverwriteStagePrepared,
		CanonicalRoot:        canonicalRoot,
		CanonicalSource:      canonicalSource,
		CanonicalTarget:      canonicalTarget,
		StagingPath:          stagingPath,
		SourceParentIdentity: sourceParentIdentity,
		TargetParentIdentity: targetParentIdentity,
		SourceObservation:    sourceObservation,
		TargetObservation:    targetObservation,
	}
	return encodeOverwriteExecutionSnapshotForPlan(plan.id, operationPlanDigestForPlan(plan), recovery)
}

func overwriteExecutionPaths(plan operationPlan) (string, string, string, string, error) {
	canonicalSource, err := canonicalOverwriteSource(plan.sourcePath)
	if err != nil {
		return "", "", "", "", fmt.Errorf("canonicalize overwrite source: %w", err)
	}
	// The target is absolute by construction, so the authorization root is the
	// filesystem root rather than a configured library anchor.
	canonicalRoot := "/"
	canonicalTarget := filepath.Clean(plan.targetPath)
	if !filepath.IsAbs(canonicalTarget) {
		return "", "", "", "", fmt.Errorf("%w: overwrite target must be absolute", ErrInvalidTarget)
	}
	sum := sha256.Sum256([]byte(plan.id))
	stagingPath := filepath.Join(filepath.Dir(canonicalTarget), ".bansoko-overwrite-"+hex.EncodeToString(sum[:12]))
	return canonicalRoot, canonicalSource, canonicalTarget, stagingPath, nil
}

func canonicalOverwriteSource(path string) (string, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", errors.New("source path must be absolute")
	}
	clean := filepath.Clean(path)
	info, err := os.Lstat(clean)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("source must be a regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	resolvedInfo, err := os.Lstat(resolved)
	if err != nil || resolvedInfo.Mode()&os.ModeSymlink != 0 || !resolvedInfo.Mode().IsRegular() {
		return "", errors.New("resolved source must be a regular non-symlink file")
	}
	return filepath.Clean(resolved), nil
}

// observeFileObservation stats and hashes one regular file into the typed
// observation the overwrite checkpoint requires (fingerprint + size + identity).
func observeFileObservation(path string) (domain.FileObservation, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return domain.FileObservation{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return domain.FileObservation{}, fmt.Errorf("%s is not a regular non-symlink file", path)
	}
	pathIdentity, err := fileidentity.FromPath(path)
	if err != nil {
		return domain.FileObservation{}, err
	}
	if !fileidentity.IsPresent(pathIdentity) {
		return domain.FileObservation{}, errors.New("file identity is unavailable")
	}
	initialChange, err := fileidentity.ChangeNanosFromPath(path)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat file change marker: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return domain.FileObservation{}, err
	}
	defer file.Close()
	initial, err := file.Stat()
	if err != nil {
		return domain.FileObservation{}, err
	}
	initialIdentity, err := fileidentity.FromFile(file)
	if err != nil {
		return domain.FileObservation{}, err
	}
	if !initial.Mode().IsRegular() || !fileidentity.Matches(pathIdentity, initialIdentity) {
		return domain.FileObservation{}, errors.New("file path changed while opening descriptor")
	}
	handleEvidence, err := fileidentity.HandleEvidenceFromFile(file)
	if err != nil {
		var unavailable fileidentity.EvidenceUnavailableError
		if !errors.As(err, &unavailable) {
			return domain.FileObservation{}, fmt.Errorf("observe opened file handle: %w", err)
		}
		handleEvidence = nil
	}
	// Re-sample after opening so a same-inode mutation in the path/open gap is
	// not mistaken for a stable observation. The descriptor identity remains the
	// authoritative object check; ChangeNanos is only an observation marker.
	openedChange, err := fileidentity.ChangeNanosFromPath(path)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat file change marker after opening descriptor: %w", err)
	}
	if initialChange != 0 && openedChange != 0 && initialChange != openedChange {
		return domain.FileObservation{}, errors.New("file changed while opening descriptor")
	}

	sum := sha256.New()
	readSize, err := io.Copy(sum, file)
	if err != nil {
		return domain.FileObservation{}, err
	}
	final, err := file.Stat()
	if err != nil {
		return domain.FileObservation{}, err
	}
	finalIdentity, err := fileidentity.FromFile(file)
	if err != nil {
		return domain.FileObservation{}, err
	}
	if !final.Mode().IsRegular() || initial.Size() != final.Size() || readSize != final.Size() || !fileidentity.Matches(initialIdentity, finalIdentity) {
		return domain.FileObservation{}, errors.New("file size or identity changed while reading")
	}
	finalChange, err := fileidentity.ChangeNanosFromPath(path)
	if err != nil {
		return domain.FileObservation{}, fmt.Errorf("stat file change marker after read: %w", err)
	}
	if openedChange != 0 && finalChange != 0 && openedChange != finalChange {
		return domain.FileObservation{}, errors.New("file changed while reading")
	}
	return domain.FileObservation{
		Fingerprint:    hex.EncodeToString(sum.Sum(nil)),
		SizeBytes:      final.Size(),
		Identity:       finalIdentity,
		HandleEvidence: handleEvidence,
		ChangeNanos:    finalChange,
	}, nil
}

// hashFileSHA256File returns the hex sha256 digest of a regular file.
func hashFileSHA256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sum := sha256.New()
	buffer := make([]byte, 1<<20)
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			_, _ = sum.Write(buffer[:n])
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", readErr
		}
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
