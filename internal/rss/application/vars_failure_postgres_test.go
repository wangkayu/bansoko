package application

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	acquisition "bansoko/internal/acquisition/domain"
	target "bansoko/internal/acquisition/target"
	domain "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	observability "bansoko/internal/observability/application"
	"bansoko/internal/parserpolicy"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/workitems"
)

func TestPGRealVarsMissingFailsAdmissionWithoutEffects(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		succeeds     bool
	}{
		{"missing_capture", `vars.actual={value:extract(input.release_title,"(never-matches)")};`, false},
		{"bad_call", `vars.actual={value:extract(input.release_title) || "fallback"};`, false},
		{"missing_fact", `vars.actual={value:extract(input.mediainfo,"(.*)") || "fallback"};`, false},
		{"interpolated_missing", "vars.actual={value:`${extract(input.release_title,'(never-matches)')}`};", false},
		{"interpolated_fallback", "vars.actual={value:`${extract(input.release_title,'(never-matches)') ?? 'chosen'}`};", true},
		{"explicit_fallback", `vars.actual={value:extract(input.release_title,"(never-matches)") ?? "chosen"};`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			business, err := frozenpolicy.SeedBusiness(ctx, db, tc.source)
			if err != nil {
				t.Fatal(err)
			}
			digest, _ := business.Digest()
			input := domain.AnimeDraft{Origin: domain.SubscriptionOriginCustom, Title: "Real Anime", InstanceLabel: ptrString("vars-failure"), Metadata: boundTVMetadata(), Subgroups: domain.SubgroupsSection{MaxRss: 3, Entries: []domain.SubgroupScoreEntry{}}, Business: &business, BusinessDigest: &digest, Rules: domain.RulesDraft{Sections: domain.RulesSections{}}, RssSources: []domain.RSSSourceDraft{{SourceKind: domain.SourceKindCustomRss, Locator: "https://feeds.example.test/rss", GroupName: "G", Title: "Actual Anime"}}}
			for _, entry := range []struct {
				name string
				dst  *json.RawMessage
			}{{domain.SectionParser, &input.Rules.Sections.Parser}, {domain.SectionReleaseRules, &input.Rules.Sections.ReleaseFilter}, {domain.SectionFileRules, &input.Rules.Sections.FileFilter}, {domain.SectionNamingRules, &input.Rules.Sections.Naming}, {domain.SectionFileEffect, &input.Rules.Sections.FileEffect}} {
				*entry.dst, _ = json.Marshal(map[string]any{"mode": "replace", "value": business.Sections[entry.name]})
			}
			candidate, err := drafts.SaveAnimeConfigCandidate(ctx, draft.CandidateDraftInput{Draft: input})
			if err != nil {
				t.Fatal(err)
			}
			activated, err := drafts.ActivateAnimeConfigCandidate(ctx, candidate.Id, candidate.Version, "vars-error-activate", draft.CandidateSubscribeInput{Revision: *candidate.CurrentRevision, Start: true})
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
			snapshot.animeTitle = "Real Anime"
			size := int64(17)
			item := acquisition.FeedItem{Title: "Real Anime - 01", ReleaseFiles: []acquisition.ReleaseFile{{SourcePath: "01.mkv", Filename: "01.mkv", MediaKind: "video", SizeBytes: &size}}}
			planned := service.preflightTargetAcquisition(ctx, snapshot, item, domain.PreviewReleaseResult{Verdict: "accepted_pending_download"}, nil)
			if planned == nil || planned.vars == nil {
				t.Fatalf("missing decision: %+v", planned)
			}
			if tc.succeeds {
				if planned.admission.Admission != target.AdmissionDownload || planned.vars.Status != "ok" || planned.vars.Output["actual"]["value"] != "chosen" {
					t.Fatalf("explicit fallback: %+v vars=%+v", planned, planned.vars)
				}
			} else {
				if planned.admission.Admission != target.AdmissionFailed || planned.vars.Status != "error" || planned.vars.Output != nil || planned.vars.Diagnostic == "" {
					t.Fatalf("failed Vars admitted: %+v vars=%+v", planned, planned.vars)
				}
				if !strings.Contains(planned.planReason, "naming_vars") && !strings.Contains(planned.planReason, "Vars") {
					t.Fatalf("missing diagnostic: %s", planned.planReason)
				}
			}
			for _, table := range []string{"download_tasks", "artifacts", "file_operations"} {
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("preflight wrote %s count=%d", table, count)
				}
			}
		})
	}
}
