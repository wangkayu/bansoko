package application

import (
	"bansoko/internal/testutil/storedsections"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	anime "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	catalogdb "bansoko/internal/catalog/adapter/persistence"
	catalogapp "bansoko/internal/catalog/application"
	catalog "bansoko/internal/catalog/domain"
	provider "bansoko/internal/provider/domain"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/disposabledb/guard"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/workitems"
)

func TestQualificationCaptureRecheckPostgres(t *testing.T) { checkQualificationFixture(t) }

func checkQualificationFixture(t *testing.T) {
	db := disposabledb.ResetDisposableSchemaAsOwner(t)
	ctx := context.Background()
	fixture := qualificationFixture(t, "automatic_source")
	b, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title};`)
	if err != nil {
		t.Fatal(err)
	}
	label := "qualification-a1"
	d := anime.AnimeDraft{Origin: anime.SubscriptionOriginOneshot, Title: "Qualified Show", InstanceLabel: &label, Subgroups: anime.SubgroupsSection{MaxRss: 3, Entries: []anime.SubgroupScoreEntry{}}, Business: &b}
	digest, err := b.Digest()
	if err != nil {
		t.Fatal(err)
	}
	d.BusinessDigest = &digest
	for _, section := range []struct {
		name string
		dst  *json.RawMessage
	}{{anime.SectionParser, &d.Rules.Sections.Parser}, {anime.SectionReleaseRules, &d.Rules.Sections.ReleaseFilter}, {anime.SectionFileRules, &d.Rules.Sections.FileFilter}, {anime.SectionNamingRules, &d.Rules.Sections.Naming}, {anime.SectionFileEffect, &d.Rules.Sections.FileEffect}} {
		*section.dst, err = json.Marshal(map[string]any{"mode": "replace", "value": b.Sections[section.name]})
		if err != nil {
			t.Fatal(err)
		}
	}
	service, err := draft.NewService(db, draft.Dependencies{WorkItems: &workitems.Store{DB: db, RetentionDays: workitems.DefaultRetentionDays}})
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := service.SaveAnimeConfigCandidate(ctx, draft.CandidateDraftInput{Draft: d})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO managed_anime(id,title) VALUES('anime-1','Show')`,
		`INSERT INTO subscriptions(id,managed_anime_id,origin,instance_label,state) VALUES('sub-1','anime-1','custom','Show','active')`,
		`INSERT INTO anime_metadata(managed_anime_id) VALUES('anime-1')`,
		`INSERT INTO anime_business_configs(managed_anime_id, parser_kind, parser_source_default_version, parser_version, admission_mode, quality_profile, cleanup_policy, retention_source_default_version, retention_version, unclassified_route, file_effect_source_default_version, file_effect_version) VALUES('anime-1','regex',1,1,'all','{"version":"profile-v1","items":[]}','{"version":"cleanup-v1","effect":"quarantine","grace_seconds":3600}',1,1,'skip',0,1)`,
		`INSERT INTO rss_sources(id,subscription_id,source_kind,locator,locator_display,group_name,title) VALUES('src-1','sub-1','custom_rss','https://feeds.example/a1','feeds.example/a1','G','Show')`,
		`INSERT INTO rss_source_runtime(rss_source_id) VALUES('src-1')`,
		`INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code) VALUES('rel-1','src-1','key','{}','{}','discovered','new')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	storedsections.Seed(t, db, "anime-1")
	if _, err := db.ExecContext(ctx, `UPDATE anime_business_configs SET candidate_id=rev.candidate_id, revision=rev.revision, business_snapshot=rev.business_snapshot, business_digest=rev.business_digest, business_version=anime_business_configs.business_version+1 FROM anime_config_candidate_revisions rev WHERE anime_business_configs.managed_anime_id='anime-1' AND rev.candidate_id=$1 AND rev.revision=$2`, candidate.Id, *candidate.CurrentRevision); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	yes := true
	groupRef := provider.GroupRef{Provider: provider.ProviderMikan, ExternalID: "10"}
	groupResult := provider.GroupReadResult{Provider: provider.ProviderMikan, CatalogEntryRef: "123", ObservedAt: now, SchemaVersion: "mikan-detail-groups-v1", Complete: true, CompletenessEvidence: "mikan-detail-group-blocks-v1", Groups: []provider.GroupSnapshotEntry{{Ref: groupRef, DisplayName: "字幕组", ObservedAt: now, Active: &yes, FeedLocator: provider.FeedLocator{Provider: provider.ProviderMikan, CatalogEntryRef: "123", Group: groupRef}}}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	group, err := catalogapp.PersistGroupObservationTx(ctx, tx, groupResult)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f := fixture.catalog.feed
	f.Seeds[0].Title = "[Group] Show - 01 [1080p]"
	for i := range f.Seeds {
		if f.Seeds[i].PayloadDigest != nil {
			size := int64(len(f.Seeds[i].Payload))
			f.Seeds[i].SizeBytes = &size
		}
	}
	f.ContentHash, err = catalog.CanonicalFeedSnapshotContentHash(f.Seeds)
	if err != nil {
		t.Fatal(err)
	}
	feed, err := catalogdb.NewRepository(db).PersistProviderFeedSnapshot(ctx, catalog.ProviderFeedSnapshotWrite{Provider: f.Provider, ProviderEntryRef: f.ProviderEntryRef, ProviderGroupRef: f.ProviderGroupRef, FetchedAt: now, FreshUntil: now.Add(15 * time.Minute), ContentHash: f.ContentHash, HashSchema: f.HashSchema, ProjectionSchema: f.ProjectionSchema, Seeds: f.Seeds})
	if err != nil {
		t.Fatal(err)
	}
	// Capture and SQL recheck use the separated runtime, while fixture setup uses
	// the owner. No capture API has a writer or role-provisioning capability.
	runtimeDB := guard.MustRuntime(t)
	runtimeService, err := draft.NewService(runtimeDB, draft.Dependencies{WorkItems: &workitems.Store{DB: runtimeDB, RetentionDays: workitems.DefaultRetentionDays}})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"owner", "saved_revision", "submitted"} {
		t.Run(kind, func(t *testing.T) {
			req := qualificationRequest{entryMode: "automatic_source", configKind: kind, catalogKind: "provider_snapshot", releaseID: "rel-1", requestHash: fixture.config.requestHash, catalog: &catalogRequest{observationID: group.ID, feedID: feed.ID, seedKey: "seed"}}
			if kind == "saved_revision" {
				req.candidateID, req.revision = candidate.Id, *candidate.CurrentRevision
			}
			if kind == "submitted" {
				req.submitted = &b
				req.submittedDraft = &d
			}
			captured, err := captureQualification(ctx, runtimeDB, runtimeService, req)
			if err != nil {
				t.Fatal(err)
			}
			q, err := protocol.DecodeQualification(captured.qualification.Canonical())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(q.Canonical(), captured.qualification.Canonical()) {
				t.Fatal("canonical drift")
			}
			recheck, err := runtimeDB.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer recheck.Rollback()
			var at time.Time
			if err := recheck.QueryRowContext(ctx, `SELECT recheck_acquisition_qualification($1::jsonb,$2,$3,$4)`, string(q.Canonical()), captured.requestHash, captured.configDigest, captured.payloadDigest).Scan(&at); err != nil {
				t.Fatal(err)
			}
			if at.IsZero() {
				t.Fatal("no DB verdict")
			}
			if err := recheck.Commit(); err != nil {
				t.Fatal(err)
			}
			t.Run("witness_locked_recheck", func(t *testing.T) {
				checkWitnessRecheckPostgres(t, ctx, runtimeDB, captured)
			})
			t.Run("qualified_queued_accept", func(t *testing.T) {
				checkQualifiedQueuedPostgres(t, ctx, runtimeDB, captured)
			})
			if kind == "owner" {
				for _, ownerColumn := range []string{"rules_version", "subgroups_version"} {
					t.Run(ownerColumn+"_owner_change", func(t *testing.T) {
						if _, err := db.ExecContext(ctx, `UPDATE anime_business_configs SET `+ownerColumn+`=`+ownerColumn+`+1 WHERE managed_anime_id='anime-1'`); err != nil {
							t.Fatal(err)
						}
						defer db.ExecContext(ctx, `UPDATE anime_business_configs SET `+ownerColumn+`=`+ownerColumn+`-1 WHERE managed_anime_id='anime-1'`)
						var at time.Time
						err := runtimeDB.QueryRowContext(ctx, `SELECT recheck_acquisition_qualification($1::jsonb,$2,$3,$4)`, string(q.Canonical()), captured.requestHash, captured.configDigest, captured.payloadDigest).Scan(&at)
						var ae *AcceptanceError
						if !errors.As(mapAcceptanceError(err), &ae) || ae.Status != 409 || ae.Code != "INPUT_STALE" {
							t.Fatalf("%s change error: %v", ownerColumn, err)
						}
						var count int
						if err := db.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&count); err != nil || count != 0 {
							t.Fatalf("%s stale root count %d: %v", ownerColumn, count, err)
						}
					})
				}
			}
			t.Run("parser_policy_change", func(t *testing.T) {
				if _, err := db.ExecContext(ctx, `UPDATE anime_business_configs SET parser_version=parser_version+1 WHERE managed_anime_id='anime-1'`); err != nil {
					t.Fatal(err)
				}
				var at time.Time
				err := runtimeDB.QueryRowContext(ctx, `SELECT recheck_acquisition_qualification($1::jsonb,$2,$3,$4)`, string(q.Canonical()), captured.requestHash, captured.configDigest, captured.payloadDigest).Scan(&at)
				if kind == "owner" {
					mapped := mapAcceptanceError(err)
					var ae *AcceptanceError
					if !errors.As(mapped, &ae) || ae.Status != 409 || ae.Code != "INPUT_STALE" {
						t.Fatalf("parser change error: %v", mapped)
					}
				} else if err != nil {
					t.Fatalf("frozen parser affected by instance edit: %v", err)
				}
			})
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*qualificationRequest)
	}{
		{"missing release", func(r *qualificationRequest) { r.releaseID = "missing" }},
		{"missing observation", func(r *qualificationRequest) { r.catalog.observationID = "missing" }},
		{"missing feed", func(r *qualificationRequest) { r.catalog.feedID = "missing" }},
		{"missing selected seed", func(r *qualificationRequest) { r.catalog.seedKey = "missing" }},
		{"missing catalog identity", func(r *qualificationRequest) { r.catalog.feedID = "" }},
		{"stale revision", func(r *qualificationRequest) {
			r.configKind = "saved_revision"
			r.candidateID = candidate.Id
			r.revision = *candidate.CurrentRevision + 1
		}},
		{"missing submitted", func(r *qualificationRequest) { r.configKind = "submitted" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := qualificationRequest{configKind: "owner", catalogKind: "provider_snapshot", releaseID: "rel-1", requestHash: fixture.config.requestHash, catalog: &catalogRequest{observationID: group.ID, feedID: feed.ID, seedKey: "seed"}}
			tc.change(&req)
			_, err := captureQualification(ctx, runtimeDB, runtimeService, req)
			var typed *qualificationMaterialError
			if !errors.Is(err, errQualificationMaterial) || !errors.As(err, &typed) {
				t.Fatalf("missing material not typed: %v", err)
			}
		})
	}
	var roots int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if roots != 0 {
		t.Fatalf("capture/recheck wrote %d roots", roots)
	}
	// Read-only against the root set; it restores the metadata binding,
	// work titles and release tags it arranges before the accepting subtests.
	t.Run("U1g_vars_preview_equals_frozen", func(t *testing.T) {
		checkVarsPreviewEqualsFrozenPostgres(t, ctx, db, runtimeDB, runtimeService, qualificationRequest{configKind: "owner", catalogKind: "provider_snapshot", releaseID: "rel-1", requestHash: fixture.config.requestHash, catalog: &catalogRequest{observationID: group.ID, feedID: feed.ID, seedKey: "seed"}})
	})
	t.Run("B2b_direct", func(t *testing.T) { checkDirectReleasePostgres(t, ctx, db, runtimeDB, runtimeService) })
	t.Run("service", func(t *testing.T) {
		checkAcceptServicePostgres(t, ctx, db, runtimeDB, runtimeService, qualificationRequest{configKind: "owner", catalogKind: "provider_snapshot", releaseID: "rel-1", catalog: &catalogRequest{observationID: group.ID, feedID: feed.ID, seedKey: "seed"}}, b)
	})
}
