package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	terminal "bansoko/internal/acquisition/terminal/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/batchplan"
	downloadadmission "bansoko/internal/download/admission"
	library "bansoko/internal/library/domain"
	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
	"path"
)

// targetAcquisition is the immutable result of the transaction-free phase of
// Release evaluation. The set and observations are written only after the
// WorkItem runtime has opened its fenced owner transaction.
type targetAcquisition struct {
	set                 acquisitiontarget.Set
	observations        []acquisitiontarget.Observation
	admission           acquisitiontarget.AdmissionResult
	planReason          string
	releaseFiles        []acquisitiondomain.ReleaseFile
	vars                *namingvars.FrozenEvaluation
	downloaderAdmission *downloadadmission.Result
	downloaderError     error
	// varsInterrupted: the Vars evaluation hit its time limit. Nothing is
	// frozen; the release evaluation retries instead.
	varsInterrupted error
	// replacements: one approval per overwrite target observed present (a
	// batch replaces per file).
	replacements []*terminal.ReplacementCapture
	// overwriteTargetAbsent: an overwrite target was observed missing. When
	// no target is present, acceptance freezes an ensure_present delivery
	// with no approval; otherwise the file plan downgrades only that target.
	overwriteTargetAbsent bool
	// overwriteDirectPresent: an overwrite target holds a file no approved
	// quality replacement names (unmanaged, or managed and displaced when the
	// successor activates). It needs no approval; the delivery keeps
	// collision overwrite so the file plan replaces it.
	overwriteDirectPresent bool
	// batchPlan is the final per-file plan conclusion the owner writer
	// freezes into the release media fact snapshot (nil when admission failed
	// before classification).
	batchPlan *batchplan.Plan
}

// errTargetNotAbsolute is the RSS-facing name of the shared target
// acquisition sentinel: a naming template rendered a relative target, which is
// never joined to a deployment root.
var errTargetNotAbsolute = acquisitiontarget.ErrTargetNotAbsolute

func resolveTargetPath(rendered string) (string, error) {
	return acquisitiontarget.ResolveTargetPath(rendered)
}

// preflightTargetAcquisition asks the shared target admission boundary to plan
// and probe one ordinary Release. The batch plan classifies the manifest and is
// deduplicated against the anime's placements and in-flight acquisitions
// before the replacement decision is frozen over what remains; a plan with
// nothing placeable is skipped (no_placeable_files). A nil probe, a relative
// rendered target, an unresolvable path, or a render failure is a typed
// admission failure; an exact existing target set is adopted_existing. Neither
// case submits qB work.
func (s *Service) preflightTargetAcquisition(ctx context.Context, snapshot releaseSnapshot, item acquisitiondomain.FeedItem, release animedomain.PreviewReleaseResult, evidence *releaseParserEvidence) *targetAcquisition {
	if s == nil || !s.targetAcquisitionConfigured {
		return nil
	}
	if release.Verdict != animedomain.ReleaseVerdict("accepted_pending_download") {
		return nil
	}
	failed := func(reason, detail string) *targetAcquisition {
		return &targetAcquisition{admission: acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: reason}, planReason: detail}
	}
	if evidence == nil {
		resolved, err := resolveReleaseParserEvidence(ctx, snapshot, item.Title)
		if err != nil {
			return failed("parser_selection_unavailable", "the selected parser/profile is unavailable: "+strings.TrimSpace(err.Error()))
		}
		evidence = &resolved
	}
	route, err := loadUnclassifiedRoute(ctx, s.db, snapshot.managedAnimeID)
	if err != nil {
		return failed("unclassified_route_unavailable", err.Error())
	}
	plan, err := acquisitiontarget.BuildBatchPlan(ctx, acquisitiontarget.BatchPlanInput{
		Evidence: evidence.ParserEvidence, Files: item.ReleaseFiles, FileFilter: snapshot.effective.Rules.FileFilter, UnclassifiedRoute: route,
		DownloadCompletion: string(snapshot.effective.DownloadCompletion.Rules.Action), DownloadCompletionSeedLimits: snapshot.effective.DownloadCompletion.Rules.SeedLimits,
	})
	if err != nil {
		return failed("file_parser_unavailable", err.Error())
	}
	// The plan freezes with the release's media facts; one over that row's
	// bound is refused before anything is planned or written.
	if acquisitiontarget.BatchPlanTooLarge(plan) {
		_, reason, detail := torrentTooLargeOutcome()
		return failed(reason, detail)
	}
	intent := acquisitiontarget.IntentEnsurePresent
	if decision := snapshot.competitionDecision; decision != nil {
		// Decided stage: the competition's per-key verdicts deduplicate the
		// plan. A key it did not decide is claimed by another candidate as
		// far as this release knows (fail closed).
		plan.ApplyDedup(decidedVerdicts(*decision, plan.CandidateKeys()))
	} else if s.replacementAdmission != nil {
		// A batch only fills what is missing: keys a present placement already
		// holds, or another running acquisition already claims, are dropped
		// before the decision is frozen over the remaining keys.
		verdicts, err := s.replacementAdmission.CoverageVerdicts(ctx, snapshot.managedAnimeID, snapshot.releaseID, acquisitiontarget.CandidateEvidence{
			Title: item.Title, Coverage: plan.CandidateKeys(), SizeBytes: releaseSize(item), Units: plan.CandidateUnits(),
		})
		if err != nil {
			return failed("replacement_admission_failed", "coverage could not be compared: "+strings.TrimSpace(err.Error()))
		}
		plan.ApplyDedup(verdicts)
	}
	if !plan.Accepted() {
		return &targetAcquisition{
			admission:  acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionSkipped, Reason: batchplan.ReasonNoPlaceableFiles},
			planReason: "no file of the release can be placed", batchPlan: &plan,
		}
	}
	// The replacement decisions (one per planned key) are frozen only for an
	// active subscription (the decision owner refuses a stopped one); an
	// explicit retry of a stopped subscription's Release keeps the
	// deduplicated ensure_present plan. A planned file replaces only when
	// every key it covers has an admitted replace decision.
	var replaceKeys map[string]bool
	if s.replacementAdmission != nil && snapshot.subscriptionState == "active" {
		intents, err := s.replacementAdmission.CandidateIntents(ctx, snapshot.managedAnimeID, snapshot.releaseID, &acquisitiontarget.CandidateEvidence{
			Title: item.Title, Coverage: plan.CoverageKeys, SizeBytes: releaseSize(item), Units: plan.CandidateUnits(),
		})
		if err != nil {
			return failed("replacement_admission_failed", "replacement admission could not be frozen: "+strings.TrimSpace(err.Error()))
		}
		for key, resolved := range intents {
			if resolved == acquisitiontarget.IntentReplace {
				if replaceKeys == nil {
					replaceKeys = map[string]bool{}
				}
				replaceKeys[key] = true
			}
		}
	}
	planned := &evidence.ParserEvidence
	program, err := varspersistence.LoadInstanceProgram(ctx, s.db, snapshot.managedAnimeID)
	if err != nil {
		return &targetAcquisition{admission: acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: "naming_vars_program_missing"}, planReason: err.Error()}
	}
	episode := naming.VarsEpisode(evidence.namingFacts())
	varsEvidence, err := varspersistence.ItemEvidence(ctx, s.db, snapshot.managedAnimeID, snapshot.releaseID, item, episode)
	if err != nil {
		return &targetAcquisition{admission: acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: "naming_vars_evidence_unavailable"}, planReason: err.Error()}
	}
	for _, file := range item.ReleaseFiles {
		if file.SizeBytes == nil {
			return &targetAcquisition{admission: acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: "naming_vars_inventory_missing"}, planReason: "exact Vars inventory size missing"}
		}
		varsEvidence.Files = append(varsEvidence.Files, namingvars.File{Name: path.Base(file.SourcePath), Path: file.SourcePath, Size: *file.SizeBytes})
	}
	varsInput := namingvars.BuildInput(varsEvidence)
	if namingvars.InputTooLarge(varsInput) {
		_, code, detail := torrentTooLargeOutcome()
		return &targetAcquisition{admission: acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: code}, planReason: detail}
	}
	frozenVars, varsErr := program.Freeze(varsInput)
	if varsErr != nil {
		return &targetAcquisition{varsInterrupted: varsErr}
	}
	release.FrozenVars = &frozenVars
	// A managed loser of an admitted replace decision at a replace target's
	// path is not a collision: its approval is captured here and the target
	// is published as an atomic replacement whatever the global policy.
	approvedLosers := map[string]*terminal.ReplacementCapture{}
	var approvalErr error
	approveLoser := func(targetPath string) bool {
		managed, err := terminal.OverwriteTargetManaged(ctx, s.db, targetPath)
		if err != nil {
			approvalErr = err
			return false
		}
		if !managed {
			return false
		}
		capture, err := terminal.CaptureReplacement(ctx, s.db, snapshot.releaseID, targetPath)
		if err != nil {
			if !errors.Is(err, terminal.ErrReplacementApprovalUnavailable) {
				approvalErr = err
			}
			return false
		}
		approvedLosers[targetPath] = capture
		return true
	}
	// A replace target whose present occupant is an unclaimed scan-managed
	// file with the exact entry size is claimed (adopted) instead of being
	// downloaded again: the owner writer attaches the release to it.
	claimable := func(targetPath string) bool {
		ok, err := scanManagedUnclaimedAt(ctx, s.db, snapshot.managedAnimeID, targetPath)
		if err != nil {
			approvalErr = err
			return false
		}
		return ok
	}
	admitted := acquisitiontarget.PreflightOrdinary(ctx, acquisitiontarget.OrdinaryRequest{
		ReleaseID:           snapshot.releaseID,
		AnimeTitle:          snapshot.animeTitle,
		GroupName:           snapshot.groupName,
		Group:               providerdomain.GroupRefOrZero(providerdomain.Kind(snapshot.sourceProvider.String), snapshot.sourceGroupRef.String),
		Offset:              snapshot.episodeOffset,
		SourceVersion:       snapshot.sourceVersion,
		ParserKind:          string(selectedParserKind(snapshot)),
		ParserPolicyVersion: snapshot.parserVersion,
		ProfileVersion:      snapshot.profileVersion,
		RulesOwnerVersion:   snapshot.rulesVersion,
		MediaKind:           snapshot.mediaKind,
		Season:              snapshot.mediaSeason,
		SeasonBound:         snapshot.mediaSeasonBound,
		Evidence:            planned,
		Effective:           snapshot.effective,
		Intent:              intent,
		ReplaceKeys:         replaceKeys,
		Item:                item,
		Preview:             release,
		BatchPlan:           &plan,
		UnclassifiedRoute:   route,
		ApprovedLoserAt:     approveLoser,
		ClaimableAt:         claimable,
	}, s.targetProbe)
	result := &targetAcquisition{
		vars: &frozenVars,
		set:  admitted.Set, observations: admitted.Observations, admission: admitted.Admission,
		planReason: admitted.PlanReason, releaseFiles: admitted.ReleaseFiles,
	}
	if admitted.BatchPlan.Version != "" {
		final := admitted.BatchPlan
		result.batchPlan = &final
	}
	if approvalErr != nil {
		result.admission = acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: "replacement_approval_unavailable"}
		result.planReason = approvalErr.Error()
		return result
	}
	if admitted.Admission.Admission == acquisitiontarget.AdmissionDownload {
		covered := map[string]bool{}
		for _, spec := range admitted.Set.Targets {
			if capture, ok := approvedLosers[spec.TargetPath]; ok && spec.CollisionPolicy == library.CollisionOverwrite {
				result.replacements = append(result.replacements, capture)
				covered[spec.TargetPath] = true
				continue
			}
			if spec.CollisionPolicy == library.CollisionOverwrite {
				// Overwrite only authorizes replacing an existing file. An
				// absent target needs no replacement approval: acceptance
				// freezes it as an ordinary ensure_present delivery.
				if targetObservedMissing(admitted.Observations, spec.ID) {
					result.overwriteTargetAbsent = true
					continue
				}
				// Only a managed incumbent can carry a quality replacement
				// approval. Any present file no approval names is replaced
				// under the global setting; a managed one's placement becomes
				// displaced when the successor activates.
				managed, err := terminal.OverwriteTargetManaged(ctx, s.db, spec.TargetPath)
				if err != nil {
					result.admission = acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: "replacement_approval_unavailable"}
					result.planReason = err.Error()
					return result
				}
				if !managed {
					result.overwriteDirectPresent = true
					continue
				}
				capture, err := terminal.CaptureReplacement(ctx, s.db, snapshot.releaseID, spec.TargetPath)
				if errors.Is(err, terminal.ErrReplacementApprovalUnavailable) {
					result.overwriteDirectPresent = true
					continue
				}
				if err != nil {
					result.admission = acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: "replacement_approval_unavailable"}
					result.planReason = err.Error()
					return result
				}
				result.replacements = append(result.replacements, capture)
				covered[spec.TargetPath] = true
			}
		}
		// A replacement lifted above the skip or version policy is accepted
		// only with its approval frozen into the delivery; otherwise the
		// release fails here, before anything is downloaded.
		if uncovered := acquisitiontarget.UncoveredApprovedTarget(admitted.Set, admitted.Observations, snapshot.effective, covered); uncovered != "" {
			result.admission = acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: "replacement_approval_unavailable"}
			result.planReason = "an approved replacement target has no frozen approval"
			result.replacements = nil
			return result
		}
	}
	if admitted.Admission.Admission != acquisitiontarget.AdmissionDownload || s.downloaderAdmission == nil {
		return result
	}
	matched, err := s.downloaderAdmission.MatchAdmission(ctx, downloadadmission.Request{
		OperationKey: terminal.EffectKey(snapshot.releaseID, snapshot.downloadAttempt()),
		InfoHash:     item.TorrentInfoHash, InfoHashV1: item.TorrentInfoHashV1, InfoHashV2: item.TorrentInfoHashV2,
	})
	if err != nil {
		if !errors.Is(err, downloadadmission.ErrAmbiguousMatch) && !errors.Is(err, downloadadmission.ErrConflictingMatch) {
			result.downloaderError = err
			return result
		}
		reason := "qb_adoption_ambiguous"
		if errors.Is(err, downloadadmission.ErrConflictingMatch) {
			reason = "qb_adoption_conflict"
		}
		result.admission = acquisitiontarget.AdmissionResult{Admission: acquisitiontarget.AdmissionFailed, Reason: reason}
		result.planReason = strings.TrimSpace(err.Error())
		return result
	}
	result.downloaderAdmission = &matched
	return result
}

// loadUnclassifiedRoute reads the instance's own unknown-file route. Global is
// never read; an instance without one fails closed. The batch plan freezes it
// at acceptance; nothing downstream reads the live value again.
func loadUnclassifiedRoute(ctx context.Context, q queryer, managedAnimeID string) (string, error) {
	var route sql.NullString
	if err := q.QueryRowContext(ctx, `
SELECT unclassified_route FROM anime_business_configs WHERE managed_anime_id = $1`, managedAnimeID).Scan(&route); err != nil {
		return "", fmt.Errorf("read instance unclassified route: %w", err)
	}
	if !route.Valid {
		return "", fmt.Errorf("instance %s has no unclassified route", managedAnimeID)
	}
	return route.String, nil
}

// targetObservedMissing reports whether the frozen pre-download observation of
// one target found nothing at its path.
func targetObservedMissing(observations []acquisitiontarget.Observation, targetID string) bool {
	for _, observation := range observations {
		if observation.TargetID == targetID {
			return observation.State == acquisitiontarget.ObservationMissing
		}
	}
	return false
}
