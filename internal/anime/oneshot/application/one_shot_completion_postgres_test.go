package application

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/batchplan"
	"bansoko/internal/transport/httpapi/generated"
)

func TestOneShotFreezesExplicitCompletionWithoutTagFiltering(t *testing.T) {
	for _, source := range []string{"saved", "current"} {
		for _, action := range []string{"remove", "seed"} {
			for _, planner := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/planner=%t", source, action, planner), func(t *testing.T) {
					service, _, db, seed, refresh := humanOneShotFixture(t)
					service.targetAcquisitionConfigured = planner
					ctx := context.Background()
					mutate := func(draft generated.AnimeDraft) generated.AnimeDraft {
						var rules generated.RulesSections
						if err := json.Unmarshal([]byte(`{"tag_filters":{"mode":"replace","value":{"anibt":{"require":[],"exclude":[]},"title":{"require":[],"exclude":["resolution:1080p"]}}}}`), &rules); err != nil {
							t.Fatal(err)
						}
						draft.Rules.Sections.TagFilters = rules.TagFilters
						return draft
					}
					var input AcquireSeedInput
					if source == "current" {
						_, input = currentInputForSeed(t, service, seed, mutate)
					} else {
						draft := mutate(oneShotProviderDraftWithGroups(t, generated.SubscriptionOriginMikan, seed.ProviderEntryRef, seed.ProviderGroupRef))
						candidate, err := service.Draft.SaveAnimeConfigCandidate(ctx, oneShotCandidateInput(t, db, draft))
						if err != nil {
							t.Fatal(err)
						}
						input = AcquireSeedInput{CandidateID: string(candidate.Id), Revision: *candidate.CurrentRevision, Seed: seed, Caller: AcquireSeedCaller{Kind: AcquireSeedCallerHuman}}
					}
					input.Seed = bindSeedIdentity(seed, refresh([]acquisitiondomain.FeedItem{seedFeedItem(seed)}))
					policy := `{"action":"remove"}`
					if action == "seed" {
						policy = `{"action":"seed","seed_limits":{"ratio":2,"at_limit":"remove"}}`
					}
					if _, err := db.ExecContext(ctx, `INSERT INTO global_config_sections(section_name, schema_version, value, version)
VALUES ('download_completion', 'download-completion-v2', $1::jsonb, 1)
ON CONFLICT (section_name) DO UPDATE SET value = EXCLUDED.value, version = global_config_sections.version + 1`, policy); err != nil {
						t.Fatal(err)
					}
					const key = "oneshot-explicit-completion-0001"
					first, err := service.AcquireSeedOnce(ctx, input, key)
					if err != nil {
						t.Fatal(err)
					}
					if first.DownloadTaskID == nil {
						t.Fatal("tag filter incorrectly prevented download admission")
					}
					readPlan := func() string {
						var raw string
						if err := db.QueryRowContext(ctx, `SELECT release_facts->'batch_plan' FROM media_fact_snapshots WHERE release_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, first.ReleaseID).Scan(&raw); err != nil {
							t.Fatal(err)
						}
						return raw
					}
					before := readPlan()
					var plan batchplan.Plan
					if err := json.Unmarshal([]byte(before), &plan); err != nil {
						t.Fatal(err)
					}
					if plan.DownloadCompletion != action {
						t.Fatalf("completion = %s, want %s", plan.DownloadCompletion, action)
					}
					if action == "seed" && (plan.DownloadCompletionSeedLimits == nil || plan.DownloadCompletionSeedLimits.Ratio == nil || *plan.DownloadCompletionSeedLimits.Ratio != 2 || plan.DownloadCompletionSeedLimits.AtLimit != "remove") {
						t.Fatalf("seed limits lost: %+v", plan.DownloadCompletionSeedLimits)
					}
					if _, err := db.ExecContext(ctx, `UPDATE global_config_sections SET value = '{"action":"pause"}', version = version + 1 WHERE section_name = 'download_completion'`); err != nil {
						t.Fatal(err)
					}
					replay, err := service.AcquireSeedOnce(ctx, input, key)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(first, replay) || readPlan() != before {
						t.Fatal("replay changed the original handle or frozen completion")
					}
				})
			}
		}
	}
}
