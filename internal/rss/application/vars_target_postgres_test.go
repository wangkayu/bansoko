package application

import (
	acquisition "bansoko/internal/acquisition/domain"
	target "bansoko/internal/acquisition/target"
	domain "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	observability "bansoko/internal/observability/application"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRSSTargetFreezesRealVarsAndPerFileEpisodes(t *testing.T) {
	db := disposabledb.ResetDisposableSchema(t)
	ctx := context.Background()
	store := &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}
	drafts, err := draft.NewService(db, draft.Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	drafts.SetFrozenParserPolicy(parserpolicy.NewService(db))
	writer, err := observability.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	drafts.SetActivityWriter(writer)
	business, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title, first: input.files[0].path, second: input.files[1].path};`)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := business.Digest()
	input := domain.AnimeDraft{Origin: domain.SubscriptionOriginCustom, Title: "Actual Anime", InstanceLabel: ptrString("rss-vars"), Metadata: boundTVMetadata(), Subgroups: domain.SubgroupsSection{MaxRss: 3, Entries: []domain.SubgroupScoreEntry{}}, Business: &business, BusinessDigest: &digest, Rules: domain.RulesDraft{Sections: domain.RulesSections{}}, RssSources: []domain.RSSSourceDraft{{SourceKind: domain.SourceKindCustomRss, Locator: "https://feeds.example.test/rss", GroupName: "G", Title: "Actual Anime"}}}
	// Build explicit rule envelopes consistent with the business sections.
	for _, entry := range []struct {
		name string
		dst  *json.RawMessage
	}{{domain.SectionParser, &input.Rules.Sections.Parser}, {domain.SectionReleaseRules, &input.Rules.Sections.ReleaseFilter}, {domain.SectionFileRules, &input.Rules.Sections.FileFilter}, {domain.SectionNamingRules, &input.Rules.Sections.Naming}, {domain.SectionFileEffect, &input.Rules.Sections.FileEffect}} {
		*entry.dst, _ = json.Marshal(map[string]interface{}{"mode": "replace", "value": business.Sections[entry.name]})
	}
	candidate, err := drafts.SaveAnimeConfigCandidate(ctx, draft.CandidateDraftInput{Draft: input})
	if err != nil {
		t.Fatal(err)
	}
	activated, err := drafts.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "rss-vars-activate", draft.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	observeBoundDraftYear(t, db, activated.ManagedAnime.ID)
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetTargetAcquisition(missingTargetProbe{})
	snapshot := targetPlanningSnapshot(t, "regex", `(?i)(?:^|[-_ ])(\d{1,3})(?:\D|$)`, 0)
	snapshot.managedAnimeID = activated.ManagedAnime.ID
	snapshot.animeTitle = "Actual Anime"
	size := int64(17)
	item := acquisition.FeedItem{Title: "Actual batch 01", ReleaseFiles: []acquisition.ReleaseFile{{SourcePath: "01.mkv", Filename: "01.mkv", MediaKind: "video", SizeBytes: &size}, {SourcePath: "02.mkv", Filename: "02.mkv", MediaKind: "video", SizeBytes: &size}}}
	planned := service.preflightTargetAcquisition(ctx, snapshot, item, domain.PreviewReleaseResult{Verdict: "accepted_pending_download"}, nil)
	if planned == nil || planned.admission.Admission != target.AdmissionDownload || len(planned.set.Targets) != 2 {
		t.Fatalf("plan=%+v", planned)
	}
	if planned.vars == nil || planned.vars.Program != business.Programs.Vars || planned.vars.Output["actual"]["title"] != "Actual batch 01" || planned.vars.Output["actual"]["second"] != "02.mkv" {
		t.Fatalf("Vars=%+v", planned.vars)
	}
	if !strings.Contains(planned.set.Targets[0].TargetPath, "E01") || !strings.Contains(planned.set.Targets[1].TargetPath, "E02") {
		t.Fatalf("file identities drift=%+v", planned.set.Targets)
	}
}
func ptrString(value string) *string { return &value }
