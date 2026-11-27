package bootstrap

import (
	"testing"

	"bansoko/internal/runtime/buildinfo"
)

func TestArbitrateBaseVsOverlay(t *testing.T) {
	cases := []struct {
		name    string
		arrange func(*selectionFacts)
		want    arbitration
	}{
		{"unusable evidence", func(f *selectionFacts) { f.unusable, f.pending = true, true }, arbitration{modeMaintenance, "update_evidence_unusable"}},
		{"blocked before marker", func(f *selectionFacts) { f.rollbackBlocked, f.pending = true, true }, arbitration{modeMaintenance, "rollback_blocked"}},
		{"pending", func(f *selectionFacts) { f.pending = true }, arbitration{modePendingMarker, "overlay_pending"}},
		{"pending manual", func(f *selectionFacts) { f.pending, f.mode = true, "manual" }, arbitration{modeMaintenance, "embedded_mode_required"}},
		{"pending migrate", func(f *selectionFacts) { f.pending, f.kind = true, "migrate" }, arbitration{modeMaintenance, "embedded_mode_required"}},
		{"active takeover", func(f *selectionFacts) { f.activeTakeover = true }, arbitration{modeTakeover, "takeover_resuming"}},
		{"active takeover manual", func(f *selectionFacts) { f.activeTakeover, f.mode = true, "manual" }, arbitration{modeTakeover, "takeover_resuming"}},
		{"active takeover migrate", func(f *selectionFacts) { f.activeTakeover, f.kind = true, "migrate" }, arbitration{modeMaintenance, "takeover_resuming"}},
		{"active consume", func(f *selectionFacts) { f.active, f.consumeFamily = true, true }, arbitration{modeConsume, "overlay_update_active"}},
		{"active other", func(f *selectionFacts) { f.active = true }, arbitration{modeMaintenance, "overlay_update_active"}},
		{"active manual", func(f *selectionFacts) { f.active, f.consumeFamily, f.mode = true, true, "manual" }, arbitration{modeMaintenance, "embedded_mode_required"}},
		{"no overlay", func(f *selectionFacts) { f.pointerPresent = false }, arbitration{modeBase, "base_no_overlay"}},
		{"succeeded", func(f *selectionFacts) { f.pointerPresent, f.previousTakeover = false, "succeeded" }, arbitration{modeBase, "base_takeover_succeeded"}},
		{"contradiction", func(f *selectionFacts) { f.pointerPresent, f.evidenceConflict = false, true }, arbitration{modeMaintenance, "selection_evidence_conflict"}},
		{"unproven", func(f *selectionFacts) { f.overlayProven = false }, arbitration{modeMaintenance, "overlay_unproven"}},
		{"native anchor", func(f *selectionFacts) { f.imageAnchor = false }, arbitration{modeOverlay, "anchor_unsupported"}},
		{"unordered base", func(f *selectionFacts) { f.base.Order = buildinfo.Order{} }, arbitration{modeOverlay, "base_unordered"}},
		{"legacy overlay", func(f *selectionFacts) { f.overlay.Order = buildinfo.Order{} }, arbitration{modeOverlay, "overlay_unordered"}},
		{"other series", func(f *selectionFacts) { f.overlay.Series = "another-series" }, arbitration{modeOverlay, "series_mismatch"}},
		{"other lane", func(f *selectionFacts) { f.overlay.Lane = "test" }, arbitration{modeOverlay, "lane_mismatch"}},
		{"incompatible", func(f *selectionFacts) { f.compatible = false }, arbitration{modeOverlay, "takeover_incompatible"}},
		{"newer overlay", func(f *selectionFacts) { f.overlay.ReleaseSequence = 12 }, arbitration{modeOverlay, "overlay_newer"}},
		{"same release", func(f *selectionFacts) { f.overlay = f.base }, arbitration{modeOverlay, "same_release"}},
		{"same sequence different commit", func(f *selectionFacts) { f.overlay = f.base; f.overlay.Commit = "different" }, arbitration{modeOverlay, "release_identity_conflict"}},
		{"failed", func(f *selectionFacts) { f.previousTakeover = "failed" }, arbitration{modeOverlay, "takeover_previously_failed"}},
		{"rolled back", func(f *selectionFacts) { f.previousTakeover = "rolled_back" }, arbitration{modeOverlay, "takeover_rolled_back"}},
		{"newer base", func(f *selectionFacts) {}, arbitration{modeTakeover, "base_newer_verified"}},
		{"newer base manual", func(f *selectionFacts) { f.mode = "manual" }, arbitration{modeTakeover, "base_newer_verified"}},
		{"newer base migrate", func(f *selectionFacts) { f.kind = "migrate" }, arbitration{modeOverlay, "base_newer_takeover_not_enabled"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := selectionFacts{
				kind: "server", mode: "embedded", pointerPresent: true, overlayProven: true, imageAnchor: true, compatible: true,
				base:    buildinfo.BuildInfo{Order: buildinfo.Order{Series: buildinfo.CISeries, Lane: "stable", ReleaseSequence: 11}, Version: "1.1.0", BuildID: "v1.1.0", Commit: "base"},
				overlay: buildinfo.BuildInfo{Order: buildinfo.Order{Series: buildinfo.CISeries, Lane: "stable", ReleaseSequence: 10}, Version: "1.0.0", BuildID: "v1.0.0", Commit: "overlay"},
			}
			tc.arrange(&facts)
			if got := arbitrateBaseVsOverlay(facts); got != tc.want {
				t.Fatalf("selection = %#v, want %#v", got, tc.want)
			}
		})
	}
}
