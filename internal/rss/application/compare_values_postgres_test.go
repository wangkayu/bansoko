package application

import (
	"context"
	"sort"
	"strings"
	"testing"

	"bansoko/internal/qualityprofile"
	facts "bansoko/internal/release/facts"
	"bansoko/internal/workitems"
)

// Release evaluation freezes the replacement comparison values with the
// candidate facts: release.* from the title, anibt.* from the frozen AniBT
// tags (multi-valued language split into its values), and the provider-group
// RSS source as the subgroup source.
func TestReleaseEvaluationFreezesCompareValuesPostgres(t *testing.T) {
	db := openRSSDatabase(t)
	ctx := context.Background()
	sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
	if _, err := db.ExecContext(ctx, `
UPDATE rss_sources
SET source_kind = 'provider_group', provenance = '{"provider":"anibt","provider_entry_ref":"517057","provider_group_ref":"group-1","discovery_snapshot_version":"v1"}'::jsonb,
    provider = 'anibt', provider_entry_ref = '517057', provider_group_ref = 'group-1', discovery_snapshot_version = 'v1'
WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	// A provider-group source is refreshed only while its group is selected.
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,available,availability_generation) VALUES('compare-values-selection',$1,'anibt','group-1','Test Group','auto',TRUE,50,TRUE,1)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	var items = fetchAniBTFixture(t)
	kept := items[:0]
	for _, item := range items {
		// See TestAniBTTagFilterRealChain: this title trips an unrelated
		// activity-log redaction.
		if item.GUID != "rel_M_8_RXhEpRco" {
			kept = append(kept, item)
		}
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{items: kept})
	if err != nil {
		t.Fatal(err)
	}
	service.SetCandidateFactsRepository(facts.NewPGRepository(db))
	refresh := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRSSSourceRuntime, ID: sourceID}, workitems.KindRSSRefresh, 1)
	outcome, err := service.Handle(ctx, refresh)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := store.FinishWithResult(ctx, refresh, workitems.StateSucceeded, outcome.OwnerResult, outcome.WriteOwner); err != nil {
		t.Fatal(err)
	}
	if drainReleaseEvaluations(t, store, service) == 0 {
		t.Fatal("no release was evaluated")
	}

	read := func(guid string) qualityprofile.FrozenValues {
		t.Helper()
		var raw []byte
		if err := db.QueryRowContext(ctx, `
SELECT f.compare_values FROM release_candidate_facts f
JOIN releases r ON r.id = f.release_id
WHERE r.rss_source_id = $1 AND r.source_facts->>'identity_value' = $2`, sourceID, guid).Scan(&raw); err != nil {
			t.Fatalf("read compare values of %s: %v", guid, err)
		}
		values := qualityprofile.ParseValues(raw)
		if values.Version != qualityprofile.ValuesVersion {
			t.Fatalf("%s compare values = %s", guid, raw)
		}
		return values
	}
	folded := func(values []string) string {
		out := make([]string, 0, len(values))
		for _, value := range values {
			out = append(out, strings.ToLower(value))
		}
		sort.Strings(out)
		return strings.Join(out, "+")
	}
	group := qualityprofile.SubgroupSource{Provider: "anibt", GroupRef: "group-1"}

	full := read("rel_ELlQ5vDd4Aqu") // 1080p, CHS+CHT, INTERNAL, MKV
	if got := folded(full.Values[qualityprofile.KeyAniBTLanguage]); got != "chs+cht" {
		t.Fatalf("anibt.language = %v, want CHS and CHT", full.Values[qualityprofile.KeyAniBTLanguage])
	}
	if got := folded(full.Values[qualityprofile.KeyAniBTResolution]); got != "1080p" {
		t.Fatalf("anibt.resolution = %v", full.Values[qualityprofile.KeyAniBTResolution])
	}
	if got := folded(full.Values[qualityprofile.KeyAniBTFormat]); got != "mkv" {
		t.Fatalf("anibt.format = %v", full.Values[qualityprofile.KeyAniBTFormat])
	}
	if full.Subgroup == nil || *full.Subgroup != group {
		t.Fatalf("subgroup source = %+v, want %+v", full.Subgroup, group)
	}

	raw := read("rel_raw_none") // 720p, MP4
	if got := folded(raw.Values[qualityprofile.KeyAniBTResolution]); got != "720p" {
		t.Fatalf("raw anibt.resolution = %v", raw.Values[qualityprofile.KeyAniBTResolution])
	}

	// No tags: no anibt.* values (no fallback from the title), and the
	// subgroup source is still the RSS source's.
	untagged := read("rel_no_tags")
	for key := range untagged.Values {
		if strings.HasPrefix(key, "anibt.") {
			t.Fatalf("untagged release carries %s = %v", key, untagged.Values[key])
		}
	}
	if untagged.Subgroup == nil || *untagged.Subgroup != group {
		t.Fatalf("untagged subgroup source = %+v", untagged.Subgroup)
	}
}
