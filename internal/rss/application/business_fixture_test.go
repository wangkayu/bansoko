package application

import (
	domain "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func seedRSSBusiness(t *testing.T, db *sql.DB, animeID string) {
	t.Helper()
	seedRSSBusinessReleaseFilter(t, db, animeID, nil)
}

// seedRSSBusinessReleaseFilter also pins one release filter into the frozen
// rules section, so a fixture feed can carry items the seed filter rejects.
// The section is rewritten before the digest is taken, so the stored snapshot
// and digest stay the same pair production writes.
func seedRSSBusinessReleaseFilter(t *testing.T, db *sql.DB, animeID string, releaseFilter *domain.RegexFilter) {
	t.Helper()
	ctx := context.Background()
	b, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	if releaseFilter != nil {
		var rules domain.EffectiveRules
		if err := json.Unmarshal(b.Sections[domain.SectionReleaseRules], &rules); err != nil {
			t.Fatal(err)
		}
		rules.ReleaseFilter = *releaseFilter
		if b.Sections[domain.SectionReleaseRules], err = json.Marshal(rules); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	label := "rss-business-fixture"
	d := domain.AnimeDraft{Origin: domain.SubscriptionOriginOneshot, Title: "Test Show", InstanceLabel: &label, Subgroups: domain.SubgroupsSection{MaxRss: 3, Entries: []domain.SubgroupScoreEntry{}}, Business: &b, BusinessDigest: &digest}
	for _, entry := range []struct {
		name string
		dst  *json.RawMessage
	}{{domain.SectionParser, &d.Rules.Sections.Parser}, {domain.SectionReleaseRules, &d.Rules.Sections.ReleaseFilter}, {domain.SectionFileRules, &d.Rules.Sections.FileFilter}, {domain.SectionNamingRules, &d.Rules.Sections.Naming}, {domain.SectionFileEffect, &d.Rules.Sections.FileEffect}} {
		*entry.dst, _ = json.Marshal(map[string]interface{}{"mode": "replace", "value": b.Sections[entry.name]})
	}
	service, err := draft.NewService(db, draft.Dependencies{WorkItems: &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := service.SaveAnimeConfigCandidate(ctx, draft.CandidateDraftInput{Draft: d})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE anime_business_configs SET candidate_id=$2, revision=$3, business_snapshot=$4, business_digest=$5, business_version=business_version+1,
    unclassified_route=COALESCE(unclassified_route, $6), file_effect_source_default_version=COALESCE(file_effect_source_default_version, 0), file_effect_version=COALESCE(file_effect_version, 1),
    updated_at=NOW() WHERE managed_anime_id=$1`, animeID, candidate.Id, *candidate.CurrentRevision, raw, digest, b.UnclassifiedRoute); err != nil {
		t.Fatal(err)
	}
}
