package filemaintenance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bansoko/internal/postprocessing/naming"
	"bansoko/internal/testutil/disposabledb"
)

// A placement written before the fact-source convergence carries its release
// source evidence only on releases.source_facts: neither the frozen render
// snapshot nor the acquisition input has it. The replay must still read that
// evidence, or every pre-existing file loses its recorded language (and every
// other rt.*/anibt.* fact) and is refused with render_facts_unavailable.
func TestNamingTargetRendererReplaysLegacyReleaseSourceFacts(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	defer db.Close()
	rules := frozenRulesForTest()
	chain := seedPlacedRenderChain(t, db, placedRenderChainSpec{
		Key: "legacy-source", AnimeID: "anime-legacy-source", AnimeTitle: "Legacy Show",
		TMDBMediaKind: "tv", TMDBSeason: 1, SourceGroup: "Group",
		ReleaseTitle: "[Group] Legacy Show - 01 [1080p][CHS]",
		PlacedPath:   "/library/Legacy Show/Season 01/Legacy Show - 1 [Group]",
		DownloadName: "Legacy Show - 01.mkv", Rules: rules, FrozenEpisode: "1",
	})
	// The fixture writes the converged input; pre-convergence rows have no
	// tag_evidence, so remove it to reproduce the real legacy shape. The row
	// predates the input contract that now requires the field, so the fixture
	// drops that check for this one historical shape.
	owner := disposabledb.Owner(t)
	if _, err := owner.Exec(`ALTER TABLE acquisitions DROP CONSTRAINT acquisitions_input_check`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(`UPDATE acquisitions
SET input_canonical_utf8 = (input_canonical_utf8::jsonb #- '{source,tag_evidence}')::text
WHERE id = (SELECT acquisition_id FROM file_operations WHERE id = $1)`, chain.OperationID); err != nil {
		t.Fatal(err)
	}
	// The effect recorded the language it rendered; the snapshot predates the
	// release_source_facts field, so replay has to take it from the release row.
	if _, err := db.Exec(`UPDATE file_operations SET frozen_render_snapshot = $1::jsonb WHERE id = $2`,
		`{"render_facts":{"Parsed.Language.Code":"sc"}}`, chain.OperationID); err != nil {
		t.Fatal(err)
	}
	renderer := NewNamingTargetRenderer(db, fakeNamingRulesSource{rules: rules})
	const placedPath = "/library/Legacy Show/Season 01/Legacy Show - 1 [Group]"
	render := func() (string, error) {
		return renderer.RenderCalibratedTarget(context.Background(), "anime-legacy-source",
			placedPath, 1024, CalibratedRenderFacts{PlacementID: chain.PlacementID, EpisodeDisplay: "1"})
	}
	got, err := render()
	if err != nil {
		t.Fatalf("render legacy placement: %v", err)
	}
	if got != placedPath {
		t.Fatalf("rendered target = %q, want %q", got, placedPath)
	}
	// Both file-manager match entries read the same legacy evidence.
	lineage, err := renderer.matchLineageOf(context.Background(), chain.PlacementID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lineage.sourceFacts), "title_tags") {
		t.Fatalf("match lineage lost legacy source facts: %s", lineage.sourceFacts)
	}
	byPath, err := renderer.matchLineageByPath(context.Background(), placedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(byPath.sourceFacts), "title_tags") {
		t.Fatalf("match lineage by path lost legacy source facts: %s", byPath.sourceFacts)
	}
	// With the release row cleared there is no source evidence left, and the
	// replay must refuse rather than silently drop the recorded language.
	if _, err := db.Exec(`UPDATE releases SET source_facts = '{}'::jsonb WHERE id = 'legacy-source-rel'`); err != nil {
		t.Fatal(err)
	}
	if _, err := render(); !errors.Is(err, naming.ErrRenderFactsUnavailable) {
		t.Fatalf("render without source facts = %v, want ErrRenderFactsUnavailable", err)
	}
}
