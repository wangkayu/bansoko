package bootstrap

import "bansoko/internal/runtime/buildinfo"

// Selection facts are observations, not authority to perform an effect.
type selectionFacts struct {
	mode, kind       string
	unusable         bool
	rollbackBlocked  bool
	pending          bool
	active           bool
	consumeFamily    bool
	activeTakeover   bool
	pointerPresent   bool
	evidenceConflict bool
	overlayProven    bool
	imageAnchor      bool
	compatible       bool
	previousTakeover string
	base, overlay    buildinfo.BuildInfo
}

type arbitration struct {
	action     launchMode
	reasonCode string
}

func arbitrateBaseVsOverlay(f selectionFacts) arbitration {
	consumer := f.kind == "server" && f.mode == "embedded"
	switch {
	case f.unusable:
		return arbitration{modeMaintenance, "update_evidence_unusable"}
	case f.rollbackBlocked:
		return arbitration{modeMaintenance, "rollback_blocked"}
	case f.pending && consumer:
		return arbitration{modePendingMarker, "overlay_pending"}
	case f.pending:
		return arbitration{modeMaintenance, "embedded_mode_required"}
	case f.activeTakeover:
		// This version cannot recover a takeover. Leave its evidence in place.
		return arbitration{modeMaintenance, "takeover_resuming"}
	case f.active && f.mode != "embedded":
		return arbitration{modeMaintenance, "embedded_mode_required"}
	case f.active && consumer && f.consumeFamily:
		return arbitration{modeConsume, "overlay_update_active"}
	case f.active:
		return arbitration{modeMaintenance, "overlay_update_active"}
	case f.evidenceConflict:
		return arbitration{modeMaintenance, "selection_evidence_conflict"}
	case !f.pointerPresent && f.previousTakeover == "succeeded":
		return arbitration{modeBase, "base_takeover_succeeded"}
	case !f.pointerPresent:
		return arbitration{modeBase, "base_no_overlay"}
	case !f.overlayProven:
		return arbitration{modeMaintenance, "overlay_unproven"}
	case !f.imageAnchor:
		return arbitration{modeOverlay, "anchor_unsupported"}
	case !f.base.Ordered():
		return arbitration{modeOverlay, "base_unordered"}
	case !f.overlay.Ordered():
		return arbitration{modeOverlay, "overlay_unordered"}
	case f.base.Series != f.overlay.Series:
		return arbitration{modeOverlay, "series_mismatch"}
	case f.base.Lane != f.overlay.Lane:
		return arbitration{modeOverlay, "lane_mismatch"}
	case !f.compatible:
		return arbitration{modeOverlay, "takeover_incompatible"}
	case f.base.ReleaseSequence < f.overlay.ReleaseSequence:
		return arbitration{modeOverlay, "overlay_newer"}
	case f.base.ReleaseSequence == f.overlay.ReleaseSequence:
		if f.base.Commit != f.overlay.Commit || f.base.BuildID != f.overlay.BuildID || f.base.Version != f.overlay.Version {
			return arbitration{modeOverlay, "release_identity_conflict"}
		}
		return arbitration{modeOverlay, "same_release"}
	case f.previousTakeover == "failed":
		return arbitration{modeOverlay, "takeover_previously_failed"}
	case f.previousTakeover == "rolled_back":
		return arbitration{modeOverlay, "takeover_rolled_back"}
	default:
		return arbitration{modeOverlay, "base_newer_takeover_not_enabled"}
	}
}
