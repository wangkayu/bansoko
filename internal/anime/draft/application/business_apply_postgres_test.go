package application

import (
	"bansoko/internal/apperrors"
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/testutil/disposabledb"
	"context"
	"errors"
	"testing"
)

func TestBusinessApplyExactTargetCASReplayAndNoOwnerCreation(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service := draftNewTestService(t, db)
	draft, err := domainDraft(draftTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	b := frozenBusinessFixture(t)
	draft.Business = b.Business
	draft.BusinessDigest = b.BusinessDigest
	seedBusinessPrograms(t, db, &draft)
	original, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	activated, err := service.ActivateAnimeConfigCandidate(ctx, original.Id, original.Version, "business-create-0001", CandidateSubscribeInput{Revision: *original.CurrentRevision, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	// Changed current instance facts must be the edit baseline, not old revision.
	if _, err := db.ExecContext(ctx, `UPDATE managed_anime SET title='Current instance title' WHERE id=$1`, activated.ManagedAnime.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE anime_metadata SET bgm_work_id='current-bgm',total_episodes=24 WHERE managed_anime_id=$1`, activated.ManagedAnime.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE rss_sources SET episode_offset=8,title='Current source title' WHERE subscription_id=$1`, activated.Subscription.ID); err != nil {
		t.Fatal(err)
	}
	baseline, err := service.GetAnimeBusinessEdit(ctx, activated.ManagedAnime.ID)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Draft.Title != "Current instance title" || baseline.Draft.Metadata.Bgm.WorkId != "current-bgm" || baseline.Draft.Metadata.TotalEpisodes == nil || *baseline.Draft.Metadata.TotalEpisodes != 24 || len(baseline.Draft.RssSources) != 1 || baseline.Draft.RssSources[0].EpisodeOffset != 8 {
		t.Fatalf("stale instance baseline: %+v", baseline.Draft)
	}
	// A previously admitted task is an immutable commitment across apply.
	fixtureTx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fixtureTx.Rollback()
	if _, err := fixtureTx.ExecContext(ctx, `INSERT INTO releases(id,rss_source_id,stable_item_key,raw_facts,source_facts,verdict,reason_code) VALUES('business-inflight-release',$1,'business-inflight','{}','{}','accepted_pending_download','seeded')`, activated.RssSources[0].ID); err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownload(t, fixtureTx, "business-inflight-root", "business-inflight-task", "business-inflight-release", "business-inflight-effect", `{"sentinel":"input-before"}`, `{"sentinel":"policy-before"}`, "queued", `null`)
	if _, err := fixtureTx.ExecContext(ctx, `UPDATE releases SET download_task_id='business-inflight-task',verdict='submitted' WHERE id='business-inflight-release'`); err != nil {
		t.Fatal(err)
	}
	if err := fixtureTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var frozenInputBefore, frozenPolicyBefore string
	if err := db.QueryRowContext(ctx, `SELECT frozen_input::text,frozen_policy::text FROM download_tasks WHERE id='business-inflight-task'`).Scan(&frozenInputBefore, &frozenPolicyBefore); err != nil {
		t.Fatal(err)
	}
	draft.Business.UnclassifiedRoute = "skip"
	digest, err := draft.Business.Digest()
	if err != nil {
		t.Fatal(err)
	}
	draft.BusinessDigest = &digest
	edited, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	input := CandidateApplyInput{Revision: *edited.CurrentRevision, TargetManagedAnimeID: activated.ManagedAnime.ID, ExpectedCandidateVersion: edited.Version, ExpectedOwnerVersions: baseline.ExpectedOwnerVersions}
	stale := input
	stale.ExpectedOwnerVersions.Rules++
	_, err = service.ApplyAnimeConfigCandidate(ctx, edited.Id, "business-stale-0001", stale)
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Code != "BUSINESS_OWNER_VERSION_CONFLICT" {
		t.Fatalf("stale apply=%v", err)
	}
	result, err := service.ApplyAnimeConfigCandidate(ctx, edited.Id, "business-apply-0001", input)
	if err != nil {
		t.Fatal(err)
	}
	if result.TargetManagedAnimeID != activated.ManagedAnime.ID || result.OwnerVersions.Rules != baseline.ExpectedOwnerVersions.Rules+1 {
		t.Fatalf("apply=%+v", result)
	}
	replay, err := service.ApplyAnimeConfigCandidate(ctx, edited.Id, "business-apply-0001", input)
	if err != nil || replay != result {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if got := animetest.CountRows(t, db, "managed_anime"); got != 1 {
		t.Fatalf("apply created owners: %d", got)
	}
	if got := animetest.CountRows(t, db, "subscriptions"); got != 1 {
		t.Fatalf("apply created subscriptions: %d", got)
	}
	var route string
	if err := db.QueryRowContext(ctx, `SELECT unclassified_route FROM anime_business_configs WHERE managed_anime_id=$1`, activated.ManagedAnime.ID).Scan(&route); err != nil {
		t.Fatal(err)
	}
	if route != "skip" {
		t.Fatalf("route=%s want skip", route)
	}
	if got := animetest.CountRows(t, db, "download_tasks"); got != 1 {
		t.Fatalf("apply changed task count: %d", got)
	}
	var frozenInputAfter, frozenPolicyAfter string
	if err := db.QueryRowContext(ctx, `SELECT frozen_input::text,frozen_policy::text FROM download_tasks WHERE id='business-inflight-task'`).Scan(&frozenInputAfter, &frozenPolicyAfter); err != nil {
		t.Fatal(err)
	}
	if frozenInputAfter != frozenInputBefore || frozenPolicyAfter != frozenPolicyBefore {
		t.Fatal("apply rewrote in-flight frozen commitment")
	}
	after, err := service.GetAnimeBusinessEdit(ctx, activated.ManagedAnime.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Draft.Title != baseline.Draft.Title || after.Draft.Metadata.Bgm.WorkId != baseline.Draft.Metadata.Bgm.WorkId || *after.Draft.Metadata.TotalEpisodes != 24 || after.Draft.RssSources[0].EpisodeOffset != 8 {
		t.Fatal("business apply mutated current instance facts")
	}
}

// recordingPolicyTrigger records the after-commit replacement policy triggers.
type recordingPolicyTrigger struct{ animeIDs []string }

func (r *recordingPolicyTrigger) TriggerPolicySaved(_ context.Context, animeID string) error {
	r.animeIDs = append(r.animeIDs, animeID)
	return nil
}

// A candidate apply bumps replacement_version only when the replacement policy
// (admission mode or quality profile) changes, and only then tells the
// replacement trigger; an apply that changes other sections keeps every frozen
// replacement decision's policy version valid.
func TestBusinessApplyBumpsReplacementVersionOnlyOnPolicyChange(t *testing.T) {
	db := animetest.OpenTestDB(t)
	ctx := context.Background()
	service := draftNewTestService(t, db)
	trigger := &recordingPolicyTrigger{}
	service.SetReplacementPolicyTrigger(trigger)
	draft, err := domainDraft(draftTestDraft(t))
	if err != nil {
		t.Fatal(err)
	}
	b := frozenBusinessFixture(t)
	draft.Business = b.Business
	draft.BusinessDigest = b.BusinessDigest
	seedBusinessPrograms(t, db, &draft)
	original, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft})
	if err != nil {
		t.Fatal(err)
	}
	activated, err := service.ActivateAnimeConfigCandidate(ctx, original.Id, original.Version, "retention-create-0001", CandidateSubscribeInput{Revision: *original.CurrentRevision, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	animeID := activated.ManagedAnime.ID
	retentionVersion := func() int64 {
		t.Helper()
		var version int64
		if err := db.QueryRowContext(ctx, `SELECT replacement_version FROM anime_business_configs WHERE managed_anime_id=$1`, animeID).Scan(&version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	apply := func(key string, edit func()) CandidateApplyResult {
		t.Helper()
		baseline, err := service.GetAnimeBusinessEdit(ctx, animeID)
		if err != nil {
			t.Fatal(err)
		}
		edit()
		digest, err := draft.Business.Digest()
		if err != nil {
			t.Fatal(err)
		}
		draft.BusinessDigest = &digest
		edited, err := service.SaveAnimeConfigCandidate(ctx, CandidateDraftInput{Draft: draft})
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.ApplyAnimeConfigCandidate(ctx, edited.Id, key, CandidateApplyInput{Revision: *edited.CurrentRevision, TargetManagedAnimeID: animeID, ExpectedCandidateVersion: edited.Version, ExpectedOwnerVersions: baseline.ExpectedOwnerVersions})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	before := retentionVersion()
	// Another section changes, the replacement policy does not.
	result := apply("retention-apply-route-0001", func() { draft.Business.UnclassifiedRoute = "skip" })
	if got := retentionVersion(); got != before || result.OwnerVersions.Replacement != before {
		t.Fatalf("an unchanged replacement policy must keep replacement_version %d: row=%d result=%d", before, got, result.OwnerVersions.Replacement)
	}
	if len(trigger.animeIDs) != 0 {
		t.Fatalf("an unchanged replacement policy must not trigger replacement: %v", trigger.animeIDs)
	}
	// The admission mode changes: one bump, one trigger.
	mode := "single_copy"
	if draft.Business.Replacement.AdmissionMode == mode {
		mode = "all"
	}
	result = apply("retention-apply-mode-0001", func() { draft.Business.Replacement.AdmissionMode = mode })
	if got := retentionVersion(); got != before+1 || result.OwnerVersions.Replacement != before+1 {
		t.Fatalf("a changed admission mode must bump replacement_version once: row=%d result=%d want %d", got, result.OwnerVersions.Replacement, before+1)
	}
	if len(trigger.animeIDs) != 1 || trigger.animeIDs[0] != animeID {
		t.Fatalf("a changed replacement policy must trigger once after commit: %v", trigger.animeIDs)
	}
}
