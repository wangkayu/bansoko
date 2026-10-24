package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/batchplan"
)

// SetTargetAcquisition wires the shared pre-download target admission boundary
// into finite one-shot admission. The planner is the same implementation
// ordinary RSS evaluation calls, so one-shot cannot grow a second naming,
// collision or adoption rule. Targets are absolute by construction, so the
// boundary needs the filesystem probe and the frozen target owner only. Once
// enabled, an unavailable probe, an unauthorized collision or an
// already-satisfied target set fails or terminates the command instead of
// silently continuing to qB.
func (s *Service) SetTargetAcquisition(probe acquisitiontarget.Probe, store acquisitiontarget.Store) {
	if s == nil {
		return
	}
	s.targetProbe = probe
	s.targetStore = store
	s.targetAcquisitionConfigured = true
}

// TargetAcquisitionReady reports whether the mandatory production pre-download
// target boundary is fully wired: a filesystem probe and the persistence-backed
// frozen target owner. The composition root asserts this at startup so a
// production boot can never silently fall back to the legacy fail-open
// admission path.
func (s *Service) TargetAcquisitionReady() bool {
	if s == nil || !s.targetAcquisitionConfigured || s.targetProbe == nil {
		return false
	}
	return s.targetStore.Configured()
}

// oneShotAnimeTitle resolves the instance title naming renders with. It mirrors
// the COALESCE(NULLIF(title_override,”), title) the RSS release snapshot reads
// back, so both paths name the same anime the same way.
func oneShotAnimeTitle(draft animedomain.AnimeDraft) string {
	if draft.TitleOverride != nil {
		if trimmed := strings.TrimSpace(*draft.TitleOverride); trimmed != "" {
			return trimmed
		}
	}
	return strings.TrimSpace(draft.Title)
}

// resolveInstanceParserEvidenceTx resolves the one selected parser/profile of
// an existing instance and runs it exactly once over the given release title.
// The selection carries the frozen metadata binding projected by the shared
// animedomain.FrozenMediaBinding, exactly like the RSS release snapshot, so a
// movie-bound instance resolves the canonical movie:0 identity instead of a TV
// episode guessed from the title. The same evidence then feeds the verdict, the
// target/naming plan and the frozen DownloadTask policy, so an admission path
// cannot run a second parser. The instance parser policy is required: a missing
// or unusable row fails the caller closed with the shared typed integrity error
// and no parser runs.
// oneShotTargetRequest assembles the shared planning request from the frozen
// one-shot admission inputs. The path-specific mapping stays here and is
// deliberately tiny: the instance title is the same coalesced title ordinary
// RSS evaluation reads back, the group is the source group's display name the
// admission resolved, the offset is the one-shot input's own offset, and the
// intent is fixed by PreflightOneShot. The already resolved evidence is handed
// over so the selected parser runs once.
func oneShotTargetRequest(
	releaseID string,
	draft animedomain.AnimeDraft,
	groupName string,
	offset int,
	parserKind string,
	effective animedomain.EffectiveConfig,
	item acquisitiondomain.FeedItem,
	preview animedomain.PreviewReleaseResult,
	evidence *acquisitiontarget.ParserEvidence,
	route string,
) acquisitiontarget.OneShotRequest {
	return acquisitiontarget.OneShotRequest{
		ReleaseID:         releaseID,
		AnimeTitle:        oneShotAnimeTitle(draft),
		GroupName:         groupName,
		Offset:            offset,
		ParserKind:        parserKind,
		Evidence:          evidence,
		Effective:         effective,
		Item:              item,
		Preview:           preview,
		UnclassifiedRoute: route,
	}
}

// oneShotUnclassifiedRoute is the unknown-file route the one-shot batch plan
// freezes: the input's own business snapshot value. Admission already refuses
// an input without one; Global is never read.
func oneShotUnclassifiedRoute(business animedomain.BusinessSnapshot) (string, error) {
	route := strings.TrimSpace(business.UnclassifiedRoute)
	if route != batchplan.RouteRoute && route != batchplan.RouteSkip {
		return "", &apperrors.Business{Status: 422, Code: "BUSINESS_SNAPSHOT_INVALID", Message: "draft business snapshot has no valid unclassified route"}
	}
	return route, nil
}

// oneShotDownloadCompletion is the download-completion value a one-shot
// freezes: the Global setting, like every acquisition (the code default seed
// when none is saved).
func oneShotDownloadCompletion(ctx context.Context, tx *sql.Tx) (animedomain.ResolvedDownloadCompletion, error) {
	var raw []byte
	var version int64
	err := tx.QueryRowContext(ctx, `SELECT value, version FROM global_config_sections WHERE section_name = 'download_completion'`).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return animedomain.DefaultResolvedDownloadCompletion(), nil
	}
	if err != nil {
		return animedomain.ResolvedDownloadCompletion{}, fmt.Errorf("read one-shot download completion: %w", err)
	}
	return animedomain.ResolveDownloadCompletion(map[string]animedomain.GlobalSection{
		animedomain.SectionDownloadCompletion: {SchemaVersion: animedomain.DownloadCompletionSchemaVersion, Value: raw, Version: version},
	})
}

// planOneShotTargets runs the shared admission for one finite one-shot
// acquisition. It is the one-shot entry point of
// acquisition/target.PreflightOneShot: planning, probing, collision and
// adoption decisions come from that single implementation.
func planOneShotTargets(ctx context.Context, probe acquisitiontarget.Probe, request acquisitiontarget.OneShotRequest) acquisitiontarget.PlanResult {
	return acquisitiontarget.PreflightOneShot(ctx, request, probe)
}

// oneShotTargetRefusesAdmission reports whether a fail-closed target plan is a
// real target-state refusal that must reject the admission synchronously, or a
// naming/library condition the accepted chain carries into its post-processing
// stages (download admission is decoupled from media-library reachability).
// A configured collision
// policy that blocks an existing conflicting target is a deliberate business
// refusal: downloading would produce a file the write stage will never place,
// so the command is refused with zero writes. An entirely filter-excluded set
// is always a synchronous refusal; neither manual commands nor unavailable
// target dependencies can bypass the independent filter gates. An unavailable root,
// unresolved naming or missing entry facts are conditions of the media library
// or of the captured metadata: the download is admitted and the diagnostic
// freezes with the input for the plan stage to surface.
func oneShotTargetRefusesAdmission(plan acquisitiontarget.PlanResult) bool {
	switch strings.TrimSpace(plan.Admission.Reason) {
	case "target_collision_blocked", "replacement_collision_blocked":
		return true
	case "target_set_empty", "no_applicable_files", batchplan.ReasonNoPlaceableFiles, "release_not_in_matched", "release_must_contain_not_matched", "release_filter_invalid", "release_filter_input_invalid", "file_filter_invalid", "file_filter_path_invalid", "file_selection_unsupported":
		// Filter refusal writes no owners or download work.
		return true
	default:
		return false
	}
}

// oneShotTargetFailure converts a fail-closed shared admission result into the
// typed transport-visible failure of the finite command. Nothing is written:
// the admission transaction rolls back, so no qB work is admitted and no owner
// state is fabricated for an acquisition that will not execute. Adoption is a
// success and is deliberately NOT mapped here; only a real refusal reaches this
// function.
func oneShotTargetFailure(plan acquisitiontarget.PlanResult) error {
	reason := strings.TrimSpace(plan.Admission.Reason)
	detail := strings.TrimSpace(plan.PlanReason)
	if detail == "" {
		detail = "target acquisition admission failed"
	}
	switch reason {
	case "target_collision_blocked", "replacement_collision_blocked":
		return &apperrors.Business{Status: 409, Code: "TARGET_COLLISION_BLOCKED", Message: reason + ": " + detail}
	case "target_set_empty", "no_applicable_files", batchplan.ReasonNoPlaceableFiles:
		return &apperrors.Business{Status: 422, Code: "FILE_SELECTION_EMPTY", Message: "No file of this release can be placed; change the rules before acquisition."}
	case "target_probe_unavailable":
		return &apperrors.Business{Status: 503, Code: "TARGET_PROBE_UNAVAILABLE", Message: reason + ": " + detail}
	default:
		return &apperrors.Business{Status: 422, Code: "TARGET_ACQUISITION_FAILED", Message: reason + ": " + detail}
	}
}
