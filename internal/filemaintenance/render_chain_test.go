package filemaintenance

import (
	"bansoko/internal/testutil/acquisitionfixture"
	"bansoko/internal/testutil/disposabledb"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"bansoko/internal/episodeidentity"
	"bansoko/internal/postprocessing/naming"
)

// placedRenderChainSpec describes the durable evidence of one placed file: the
// managed anime and its metadata binding, the RSS source, the release, the
// download task, the verified artifact file with its inspected media facts,
// the completed file operation with its frozen snapshots, the release media
// fact snapshot carrying the frozen episode identity, and the active placement.
type placedRenderChainSpec struct {
	// Key makes this chain's rows unique. Several chains may belong to one anime.
	Key string
	// AnimeID/AnimeTitle are shared by every chain of the same anime.
	AnimeID    string
	AnimeTitle string
	// TMDBMediaKind and TMDBSeason are the bound metadata values the naming
	// branch and season folder come from.
	TMDBMediaKind string
	TMDBSeason    int
	// SourceGroup is the RSS source group name; naming reads it for the group fact.
	SourceGroup string
	// ReleaseTitle is the original release title (release raw_facts.title).
	ReleaseTitle string
	// PlacedPath is the absolute library path of the active placement; the
	// completed operation target equals it.
	PlacedPath string
	// DownloadName is the file name recorded in the frozen snapshot evidence.
	DownloadName string
	// DownloadPath is the pre-effect source path (defaults to /downloads/<DownloadName>).
	DownloadPath string
	// MediaFacts is the artifact file facts JSON as the inspector writes it.
	MediaFacts string
	SizeBytes  int64
	// Rules are the effective naming rules frozen on the operation.
	Rules naming.NamingRules
	// FrozenEpisode is the raw episode capture the frozen identity resolved.
	FrozenEpisode string
	// Coverage is the canonical coverage set frozen on the placement.
	Coverage []string
	// NoPlacement seeds the acquisition chain without a placement: a file that
	// was ingested but never placed, which has only its own lineage.
	NoPlacement bool
	// BindingFacts is the metadata binding the file effect read when it
	// (absent in most fixtures; lines without a binding keep the release
	// snapshot behaviour). It is what makes a maintenance render's naming
	// context bound and its episode title lookups able to run.
	BindingFacts *frozenRenderBindingFacts
}

// placedRenderChain returns the ids a seeded chain created.
type placedRenderChain struct {
	OperationID    string
	PlacementID    string
	ArtifactFileID string
	SnapshotID     string
}

// frozenRulesForTest is the shared maintenance naming rule set: an absolute
// title/season directory plus the episode and source group facts. The directory
// template renders the deployment path itself — targets are absolute by
// construction and no root anchor exists. It deliberately avoids facts a
// fixture does not carry, so a test only grows the fixture it actually needs.
func frozenRulesForTest() naming.NamingRules {
	return naming.NamingRules{
		Language: naming.Language,
		TV: naming.NamingVariant{
			DirectoryTemplate:     "/library/${sanitize(anime.title)}/${defaultValue('Season 01', anime.season_folder)}",
			VideoFilenameTemplate: "${sanitize(anime.title)} - ${ep.display} [${anime.group}]",
		},
		Movie: naming.NamingVariant{
			DirectoryTemplate:     "/library/${sanitize(anime.title)}",
			VideoFilenameTemplate: "${sanitize(anime.title)}.${file.ext}",
		},
	}
}

// frozenConfigSnapshotForTest freezes the naming rules the way a completed file
// operation does, so the maintenance render reads the same effective rules
// acquisition used instead of the current rules owner.
func frozenConfigSnapshotForTest(t *testing.T, rules naming.NamingRules) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"naming_rules": rules})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// seedPlacedRenderChain writes one complete placed-file chain and returns its
// ids. Every fact the placement render consumes is durable: nothing is read
// from the current configuration, the current metadata row, or the file name.
func seedPlacedRenderChain(t *testing.T, db *sql.DB, spec placedRenderChainSpec) placedRenderChain {
	t.Helper()
	if spec.SourceGroup == "" {
		spec.SourceGroup = "Group"
	}
	if spec.TMDBSeason == 0 {
		spec.TMDBSeason = 1
	}
	if spec.SizeBytes == 0 {
		spec.SizeBytes = 1024
	}
	if spec.DownloadName == "" {
		spec.DownloadName = filepath.Base(spec.PlacedPath)
	}
	if spec.DownloadPath == "" {
		spec.DownloadPath = "/downloads/" + spec.DownloadName
	}
	if spec.FrozenEpisode == "" {
		spec.FrozenEpisode = "1"
	}
	chain := placedRenderChain{
		OperationID:    "op-" + spec.Key,
		PlacementID:    "plc-" + spec.Key,
		ArtifactFileID: "af-" + spec.Key,
		SnapshotID:     "snap-" + spec.Key,
	}
	ctx := context.Background()
	tx, err := disposabledb.Owner(t).BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed placed render chain %s: %v", spec.Key, err)
		}
	}

	exec(`INSERT INTO managed_anime(id, title) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		spec.AnimeID, spec.AnimeTitle)
	exec(`INSERT INTO anime_metadata(managed_anime_id, tmdb_work_id, tmdb_media_kind, tmdb_season)
VALUES ($1, 'work-' || $1, $2, $3)
ON CONFLICT (managed_anime_id) DO UPDATE SET tmdb_media_kind = EXCLUDED.tmdb_media_kind, tmdb_season = EXCLUDED.tmdb_season`,
		spec.AnimeID, spec.TMDBMediaKind, spec.TMDBSeason)
	// One subscription per anime (unique managed_anime_id): reuse the caller's
	// subscription when the test already seeded one.
	exec(`INSERT INTO subscriptions(id, managed_anime_id, origin, instance_label, state)
VALUES ($1, $2, 'custom', 'Render chain', 'active')
ON CONFLICT (managed_anime_id) DO NOTHING`, spec.AnimeID+"-sub", spec.AnimeID)
	var subscriptionID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM subscriptions WHERE managed_anime_id = $1`, spec.AnimeID).Scan(&subscriptionID); err != nil {
		t.Fatalf("read chain subscription for %s: %v", spec.AnimeID, err)
	}
	exec(`INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title)
VALUES ($1, $2, 'custom_rss', $3, $3, $4, $5)`,
		spec.Key+"-src", subscriptionID, "https://feeds.example.test/rss/"+spec.Key, spec.SourceGroup, spec.AnimeTitle)
	exec(`INSERT INTO releases(id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts, verdict, reason_code, download_task_id, version)
VALUES ($1, $2, $3, $4::jsonb, '{}'::jsonb, '{}'::jsonb, 'submitted', 'submitted', $5, 1)`,
		spec.Key+"-rel", spec.Key+"-src", "guid:"+spec.Key,
		fmt.Sprintf(`{"title":%q}`, spec.ReleaseTitle), spec.Key+"-task")
	manifest, err := json.Marshal([]any{map[string]any{"file_ref": "primary", "ordinal": 0, "relative_path": spec.DownloadName, "exact_size": spec.SizeBytes, "media_kind": "video", "identity_evidence": map[string]any{}}})
	if err != nil {
		t.Fatal(err)
	}
	acquisitionfixture.SeedObservedDownloadManifest(t, tx, "acq-"+spec.Key, spec.Key+"-task", spec.Key+"-rel", "task:"+spec.Key, `{}`, `{}`, "completed", `null`, string(manifest))
	exec(`INSERT INTO artifacts(id, download_task_id, acquisition_id, inventory_snapshot, fingerprint_snapshot, state, verified_at)
VALUES ($1, $2, (SELECT acquisition_id FROM download_tasks WHERE id=$2), '{}'::jsonb, '{}'::jsonb, 'verified', CURRENT_TIMESTAMP)`,
		spec.Key+"-art", spec.Key+"-task")

	fingerprint := fmt.Sprintf(`{"algorithm":"sha256","value":"%s"}`, fmt.Sprintf("%064d", len(spec.Key)))
	mediaFacts := spec.MediaFacts
	if mediaFacts == "" {
		mediaFacts = "{}"
	}
	exec(`INSERT INTO artifact_files(id, artifact_id, acquisition_id, file_ref, relative_path, source_path, media_kind, size_bytes, fingerprint, facts, subtitle_binding_state)
VALUES ($1, $2, (SELECT acquisition_id FROM artifacts WHERE id=$2), 'primary', $3, $4, 'video', $5, $6::jsonb, $7::jsonb, 'not_applicable')`,
		chain.ArtifactFileID, spec.Key+"-art", spec.DownloadName, spec.DownloadPath, spec.SizeBytes, fingerprint, mediaFacts)
	renderSnapshot := json.RawMessage(`{}`)
	if spec.BindingFacts != nil {
		binding, err := json.Marshal(map[string]any{"binding_facts": spec.BindingFacts})
		if err != nil {
			t.Fatalf("marshal binding facts for %s: %v", spec.Key, err)
		}
		renderSnapshot = binding
	}
	acquisitionfixture.CommitOperations(t, tx, spec.Key+"-art", []acquisitionfixture.PlanOperation{{ID: chain.OperationID, FileID: chain.ArtifactFileID, FileRef: "primary", Source: spec.DownloadPath, Target: spec.PlacedPath, Operation: "move", Key: "op:" + spec.Key, Size: spec.SizeBytes, Fingerprint: json.RawMessage(fingerprint), Render: renderSnapshot, Config: frozenConfigSnapshotForTest(t, spec.Rules)}})
	// Owner arranges the downstream completed-effect precondition; the prepare stage itself
	// only creates the immutable legal Plan and pending Operation.
	exec(`UPDATE file_operations SET state='completed',observed_at=CURRENT_TIMESTAMP,target_fingerprint=source_fingerprint WHERE id=$1`, chain.OperationID)
	exec(`SELECT public.work_item_close(id,'succeeded') FROM work_items WHERE owner_type='file_operation' AND owner_id=$1`, chain.OperationID)

	identity := episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
		MediaKind:    spec.TMDBMediaKind,
		Season:       spec.TMDBSeason,
		SeasonBound:  true,
		FileEvidence: episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", spec.FrozenEpisode),
	})
	identityRaw, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	releaseFacts, err := json.Marshal(map[string]any{
		"title":        spec.ReleaseTitle,
		"group_name":   spec.SourceGroup,
		"media_kind":   spec.TMDBMediaKind,
		"season":       spec.TMDBSeason,
		"season_bound": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ($1, $2, 'media-facts-v1', $3, 'regex', $4, $5::jsonb, $6::jsonb)`,
		chain.SnapshotID, spec.Key+"-rel", episodeidentity.ResolverVersion, "digest:"+spec.Key, identityRaw, releaseFacts)
	evidence, err := json.Marshal(map[string]any{
		"name":       spec.DownloadName,
		"media_kind": "video",
		"size_bytes": spec.SizeBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := json.Marshal(map[string]any{
		"identity":        identity,
		"mapping_version": "facts-map-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, file_evidence, effective_facts)
VALUES ($1, 'primary', 'video', $2, $3::jsonb, $4::jsonb)`,
		chain.SnapshotID, spec.SizeBytes, evidence, effective)

	if !spec.NoPlacement {
		exec(`INSERT INTO placements(id, physical_path, managed_anime_id, source_evidence_type, source_owner_ref,
 source_fingerprint, target_fingerprint, quality_facts_ref, facts_version, verification_state, state, replacement_eligible, version, created_at, updated_at)
VALUES ($1, $2, $3, 'file_operation', $4, $5::jsonb, $5::jsonb, $6, 'facts-v1', 'verified', 'active', true, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			chain.PlacementID, spec.PlacedPath, spec.AnimeID, chain.OperationID, fingerprint, spec.Key+"-rel")
		for ordinal, key := range spec.Coverage {
			exec(`INSERT INTO placement_coverage(placement_id, observation_key, coverage_ordinal, facts_version, evidence_ref)
VALUES ($1, $2, $3, 'facts-v1', $4)`,
				chain.PlacementID, key, ordinal, chain.OperationID)
		}
	}

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	freezeRenderFixtureBusiness(t, db, chain.OperationID)
	return chain
}
