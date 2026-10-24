package application

import (
	"context"
	"encoding/json"
	"testing"

	"bansoko/internal/episodeidentity"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/workitems"
)

// TestReleaseEvaluateAniBTModeDoesNotResolveEpisode proves the AniBT aux
// decision: provider torrent filenames are ambiguous episode evidence, so the
// aux mode never participates in episode resolution. With an AniBT provider
// source in override mode and a feed title without an episode, the identity
// stays unresolved even though the torrent filename could resolve one.
func TestReleaseEvaluateAniBTModeDoesNotResolveEpisode(t *testing.T) {
	db := openRSSDatabase(t)
	sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `
UPDATE rss_sources
SET source_kind = 'provider_group', provenance = '{"provider":"anibt","provider_entry_ref":"bgm-1","provider_group_ref":"group-1","discovery_snapshot_version":"v1"}'::jsonb,
    provider = 'anibt', provider_entry_ref = 'bgm-1', provider_group_ref = 'group-1',
	discovery_snapshot_version = 'v1', anibt_aux_mode = 'override'
WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	// Provider admission requires a real selected observation keyed by stable ID.
	if _, err := db.ExecContext(ctx, `INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,effective_selected,effective_score,count_in_limit,available,availability_generation) VALUES('anibt-aux-selection',$1,'anibt','group-1','Test Group','auto',TRUE,50,TRUE,TRUE,1)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	// The feed title carries no episode; the torrent filename does. The
	// filename must NOT become episode evidence.
	insertReleaseForEvaluation(t, db, sourceID, "release-anibt-aux", "[Test Group] Test Show [BDRip 1080p]", "https://downloads.example.test/anibt.torrent")
	internal, err := json.Marshal(map[string]any{
		"title":            "[Test Group] Test Show [BDRip 1080p]",
		"download_url":     "https://downloads.example.test/anibt.torrent",
		"guid":             "release-anibt-aux-guid",
		"torrent_filename": "[Test Group] Test Show - 05.mkv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE releases SET internal_facts = $1::jsonb WHERE id = 'release-anibt-aux'`, internal); err != nil {
		t.Fatal(err)
	}

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
	claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-anibt-aux"}, workitems.KindReleaseEvaluate, 1)
	// A missing scope must produce the typed no-admission outcome, not merely
	// fail later with an absent media snapshot.
	if _, err := db.ExecContext(ctx, `UPDATE provider_group_selections SET effective_selected=FALSE WHERE id='anibt-aux-selection'`); err != nil {
		t.Fatal(err)
	}
	blocked, err := service.Handle(ctx, claim)
	if err != nil || blocked.OwnerResult.ReasonCode != "provider_group_not_selected" || blocked.WriteOwner != nil {
		t.Fatalf("out-of-scope AniBT outcome=%+v err=%v", blocked, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE provider_group_selections SET effective_selected=TRUE WHERE id='anibt-aux-selection'`); err != nil {
		t.Fatal(err)
	}
	finishReleaseClaim(t, store, service, claim)

	var identityRaw []byte
	var inputDigest string
	if err := db.QueryRowContext(ctx, `
SELECT release_identity, input_digest FROM media_fact_snapshots WHERE release_id = 'release-anibt-aux'`).
		Scan(&identityRaw, &inputDigest); err != nil {
		t.Fatal(err)
	}
	identity, err := facts.UnmarshalIdentity(identityRaw)
	if err != nil {
		t.Fatalf("decode identity: %v", err)
	}
	if identity.State == episodeidentity.StateResolved || identity.Episode != nil {
		t.Fatalf("AniBT filename must not resolve episode identity: state=%s episode=%v", identity.State, identity.Episode)
	}

	// The mode is still part of the digest: a mode edit produces a different
	// digest, so the next evaluation freezes a fresh snapshot (conservative
	// re-evaluation of the reserved configuration).
	var digestAgain string
	if _, err := db.ExecContext(ctx, `
UPDATE rss_sources SET anibt_aux_mode = 'disabled', version = version + 1, updated_at = NOW()
WHERE id = $1`, sourceID); err != nil {
		t.Fatal(err)
	}
	claim = createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: "release-anibt-aux"}, workitems.KindReleaseEvaluate, 2)
	finishReleaseClaim(t, store, service, claim)
	if err := db.QueryRowContext(ctx, `
SELECT input_digest FROM media_fact_snapshots WHERE release_id = 'release-anibt-aux' ORDER BY created_at DESC, id DESC LIMIT 1`).
		Scan(&digestAgain); err != nil {
		t.Fatal(err)
	}
	if digestAgain == inputDigest {
		t.Fatalf("input digest unchanged after aux mode edit: %s", digestAgain)
	}
	var count int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM media_fact_snapshots WHERE release_id = 'release-anibt-aux'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("snapshots=%d, want 2 (fresh snapshot after mode edit)", count)
	}
}
