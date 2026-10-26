package application_test

import (
	terminal "bansoko/internal/acquisition/terminal/application"
	"bansoko/internal/acquisition/torrent"
	"bansoko/internal/anime/animeconfig"
	anime "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	ownership "bansoko/internal/anime/ownership/application"
	catalog "bansoko/internal/catalog/domain"
	observability "bansoko/internal/observability/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/testutil/frozenpolicy"
	"bansoko/internal/testutil/nativeinput"
	"bansoko/internal/testutil/storedsections"
	"bansoko/internal/workitems"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

// One-shot retention, cleanup and a rules PATCH each run against a concurrent
// acceptance: the writer and the acceptor must serialize on the ManagedAnime
// owner row without a deadlock.
func TestDirectConcurrencyPostgres(t *testing.T) {
	for _, mode := range []string{"retention", "cleanup", "rules_patch"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			db := disposabledb.ResetDisposableSchemaAsOwner(t)
			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			revisions, err := draft.NewService(db, draft.Dependencies{WorkItems: store})
			if err != nil {
				t.Fatal(err)
			}
			b, err := frozenpolicy.SeedBusiness(ctx, db, `vars.actual = {title: input.release_title};`)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := b.Digest()
			if err != nil {
				t.Fatal(err)
			}
			label := "concurrent"
			d := anime.AnimeDraft{Origin: anime.SubscriptionOriginOneshot, Title: "Show", InstanceLabel: &label, Subgroups: anime.SubgroupsSection{MaxRss: 3, Entries: []anime.SubgroupScoreEntry{}}, Business: &b, BusinessDigest: &digest}
			for _, section := range []struct {
				name string
				dst  *json.RawMessage
			}{{anime.SectionParser, &d.Rules.Sections.Parser}, {anime.SectionReleaseRules, &d.Rules.Sections.ReleaseFilter}, {anime.SectionFileRules, &d.Rules.Sections.FileFilter}, {anime.SectionNamingRules, &d.Rules.Sections.Naming}, {anime.SectionFileEffect, &d.Rules.Sections.FileEffect}} {
				*section.dst, err = json.Marshal(map[string]any{"mode": "replace", "value": b.Sections[section.name]})
				if err != nil {
					t.Fatal(err)
				}
			}
			candidate, err := revisions.SaveAnimeConfigCandidate(ctx, draft.CandidateDraftInput{Draft: d})
			if err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				`INSERT INTO managed_anime(id,title) VALUES('anime-1','Show')`,
				`INSERT INTO subscriptions(id,managed_anime_id,origin,instance_label,state) VALUES('sub-1','anime-1','custom','Show','active')`,
				`INSERT INTO anime_metadata(managed_anime_id,bgm_work_id,tmdb_work_id,tmdb_media_kind,tmdb_season) VALUES('anime-1','424242','99','tv',1)`,
				`INSERT INTO rss_sources(id,subscription_id,source_kind,locator,locator_display,group_name,title) VALUES('src-1','sub-1','custom_rss','https://feeds.example/a1','feeds.example/a1','G','Show')`,
				`INSERT INTO rss_source_runtime(rss_source_id) VALUES('src-1')`,
				`INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code) VALUES('rel-1','src-1','key','{"title":"[Group] Show - 01 [1080p]","download_url":"https://example.test/direct.torrent"}','{}','discovered','new')`,
				`INSERT INTO anime_business_configs(managed_anime_id, parser_kind, parser_source_default_version, parser_version, admission_mode, quality_profile, replacement_source_default_version, replacement_version, unclassified_route, file_effect_source_default_version, file_effect_version) VALUES('anime-1','regex',1,1,'all','{"version":"profile-v1","items":[]}',1,1,'skip',0,1)`,
			} {
				if _, err := db.ExecContext(ctx, q); err != nil {
					t.Fatal(err)
				}
			}
			storedsections.Seed(t, db, "anime-1")
			if _, err := db.ExecContext(ctx, `UPDATE anime_business_configs SET candidate_id=rev.candidate_id, revision=rev.revision, business_snapshot=rev.business_snapshot, business_digest=rev.business_digest, business_version=anime_business_configs.business_version+1 FROM anime_config_candidate_revisions rev WHERE anime_business_configs.managed_anime_id='anime-1' AND rev.candidate_id=$1 AND rev.revision=$2`, candidate.Id, *candidate.CurrentRevision); err != nil {
				t.Fatal(err)
			}
			payload := []byte("d4:infod6:lengthi1000e4:name8:ep01.mkvee")
			meta, err := torrent.Parse(payload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO release_torrent_inputs(id,release_id,payload,payload_digest,source_url_digest,filename,parser_version,torrent_version,info_hash_v1,payload_size_bytes) VALUES('concurrent-input','rel-1',$1,$2,$3,'ep01.mkv','torrent-bencode-v1','v1',$4,$5)`, payload, catalog.DigestBytes(payload), catalog.DigestBytes([]byte("locator")), meta.InfoHashV1, len(payload)); err != nil {
				t.Fatal(err)
			}
			if mode == "retention" {
				if _, err := db.ExecContext(ctx, `UPDATE releases SET verdict='adopted_existing' WHERE id='rel-1'`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, `INSERT INTO one_shot_acquisitions(id,managed_anime_id,subscription_id,rss_source_id,release_id,download_task_id,correlation_id,request_idempotency_key,request_hash,result_snapshot,state,terminal_outcome,terminal_reason_code,terminal_reason,terminal_at,retention_until) VALUES('concurrent-marker','anime-1','sub-1','src-1','rel-1',NULL,'concurrent-correlation','concurrent-marker-key',$1,'{}','reclaim_pending','succeeded','one_shot_completed','adopted',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP-interval '1 hour')`, catalog.DigestBytes([]byte("marker"))); err != nil {
					t.Fatal(err)
				}
			}
			var version int64
			if err := db.QueryRowContext(ctx, `SELECT version FROM releases WHERE id='rel-1'`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			gate, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback()
			acceptor, err := terminal.NewManualRetryAcceptor(db, revisions)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := animeconfig.LoadResolved(ctx, gate, "anime-1")
			if err != nil {
				t.Fatal(err)
			}
			resolution, err := terminal.NewOwnerResolution(resolved)
			if err != nil {
				t.Fatal(err)
			}
			policy, err := nativeinput.OwnerPolicy(resolved)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := acceptor.Prepare(ctx, txscope.Wrap(gate), terminal.EntryTask{ReleaseID: "rel-1", ExpectedVersion: version, FrozenInput: json.RawMessage(`{}`), FrozenPolicy: policy, Resolution: resolution, MaxAttempts: 5})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "rules_patch" {
				activity, err := observability.NewService(db)
				if err != nil {
					t.Fatal(err)
				}
				owner, err := ownership.NewService(db, ownership.Dependencies{WorkItems: store, ActivityWriter: activity})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := owner.PatchAnimeRules(ctx, "anime-1", ownership.RulesPatchInput{ExpectedVersion: 1, Sections: d.Rules.Sections}); err != nil {
					t.Fatal(err)
				}
				if _, err := acceptor.Accept(ctx, txscope.Wrap(gate), prepared); err == nil {
					t.Fatal("rules PATCH after capture accepted")
				} else {
					var ae *terminal.AcceptanceError
					if !errors.As(err, &ae) || ae.Status != 409 || ae.Code != "INPUT_STALE" {
						t.Fatalf("rules PATCH error: %v", err)
					}
				}
				if err := gate.Rollback(); err != nil {
					t.Fatal(err)
				}
				var count int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM acquisitions`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("PATCH stale roots %d: %v", count, err)
				}
				return
			}
			if mode == "cleanup" {
				if _, err := db.ExecContext(ctx, `UPDATE managed_anime SET lifecycle_state='retired',fence_epoch=fence_epoch+1,retired_at=CURRENT_TIMESTAMP,retired_subscription_version=1,lifecycle_version=lifecycle_version+1 WHERE id='anime-1'`); err != nil {
					t.Fatal(err)
				}
			}
			var ignored string
			if err := gate.QueryRowContext(ctx, `SELECT lifecycle_state FROM managed_anime WHERE id='anime-1' FOR UPDATE`).Scan(&ignored); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			writerCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			go func() {
				switch mode {
				case "cleanup":
					_, err := db.ExecContext(writerCtx, `SELECT cleanup_managed_anime_local_data('anime-1','concurrent-cleanup',$1,2)`, catalog.DigestBytes([]byte("cleanup")))
					done <- err
				case "retention":
					service, err := workitems.NewOneShotReclaimService(db, nil)
					if err != nil {
						done <- err
						return
					}
					result, err := service.RunPass(writerCtx)
					if err == nil && result.Reclaimed != 1 {
						err = fmt.Errorf("retention did not reclaim: %+v", result)
					}
					done <- err
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			waiting := false
			for time.Now().Before(deadline) {
				var n int
				if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock'`).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n > 0 {
					waiting = true
					break
				}
				select {
				case err := <-done:
					t.Fatalf("writer finished before overlap: %v", err)
				default:
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !waiting {
				t.Fatal("writer lock wait not observed")
			}
			t.Log("observed actual writer lock wait")
			if mode != "cleanup" {
				if _, err := gate.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id='rel-1'`); err != nil {
					t.Fatal(err)
				}
				if _, err := acceptor.Accept(ctx, txscope.Wrap(gate), prepared); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := acceptor.Accept(ctx, txscope.Wrap(gate), prepared); err == nil {
					t.Fatal("retired accepted")
				}
			}
			if err := gate.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			t.Log("actual writer and acceptance converged without deadlock")
		})
	}
}
