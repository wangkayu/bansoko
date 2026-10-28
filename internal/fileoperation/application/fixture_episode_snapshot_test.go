package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"bansoko/internal/batchplan"
	"bansoko/internal/episodeidentity"
	facts "bansoko/internal/release/facts"
	"bansoko/internal/testutil/mediafactsfixture"
)

// seedFixtureEpisodeSnapshot freezes the media fact snapshot RSS admission
// writes for the fixture release: every manifest video resolves to episode 1 of
// season 1 (the fixture title is "... - 01") unless its row declares its own
// "episode", which is the per-file evidence the production parser freezes for a
// batch that carries several episodes (a row named "Show - 02.mkv" is episode
// 2, and a shared episode 1 would render the shipped default template's episode
// number as a duplicate target). Naming reads episodes only from this snapshot.
// A test that needs other identities inserts its own snapshot afterwards; the
// newest snapshot of a release wins.
func seedFixtureEpisodeSnapshot(t *testing.T, db *sql.DB, manifest json.RawMessage) {
	t.Helper()
	ctx := context.Background()
	var files []struct {
		FileRef      string `json:"file_ref"`
		RelativePath string `json:"relative_path"`
		MediaKind    string `json:"media_kind"`
		Size         int64  `json:"exact_size"`
		// Episode is the episode this row's own name carries. Empty keeps the
		// fixture's release episode 1.
		Episode string `json:"episode"`
	}
	if err := json.Unmarshal(manifest, &files); err != nil {
		t.Fatal(err)
	}
	resolve := func(episode string) episodeidentity.EpisodeIdentity {
		if episode == "" {
			episode = "1"
		}
		return episodeidentity.NewResolver().Resolve(episodeidentity.EpisodeIdentityInput{
			MediaKind:    "tv",
			FileEvidence: episodeidentity.NewValueEvidence(episodeidentity.EpisodeSourceFileParser, "file_name", episode),
		})
	}
	identity := resolve("1")
	identityRaw, err := facts.MarshalIdentity(identity)
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]episodeidentity.EpisodeIdentity{}
	manifestPaths := map[string]string{}
	for _, file := range files {
		if file.MediaKind == "video" {
			refs[file.FileRef] = resolve(file.Episode)
			manifestPaths[file.FileRef] = file.RelativePath
		}
	}
	planVideos := fixturePlanVideos(t, db, refs, manifestPaths)
	releaseFacts := mediafactsfixture.ReleaseFactsJSON(t, nil, batchplan.RouteSkip, planVideos...)
	if _, err := db.ExecContext(ctx, `
INSERT INTO media_fact_snapshots(id, release_id, schema_version, resolver_version, parser_kind, input_digest, release_identity, release_facts)
VALUES ('snap-fixture-default', 'release-file-test', $1, $2, 'regex', 'digest-fixture-default', $3::jsonb, $4::jsonb)`,
		facts.SchemaVersion, episodeidentity.ResolverVersion, identityRaw, releaseFacts); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.MediaKind != "video" {
			continue
		}
		evidence, _ := json.Marshal(map[string]any{"name": file.FileRef, "media_kind": "video", "size_bytes": file.Size})
		if _, err := db.ExecContext(ctx, `
INSERT INTO media_fact_files(snapshot_id, file_ref, media_kind, size_bytes, source_fingerprint, file_evidence, effective_facts)
VALUES ('snap-fixture-default', $1, 'video', $2, NULL, $3::jsonb, $4::jsonb)`,
			file.FileRef, max(file.Size, 0), evidence, mustMarshalEffectiveFacts(t, refs[file.FileRef])); err != nil {
			t.Fatal(err)
		}
	}
}

// fixturePlanVideos keys each fixture video by the path prepare maps it to:
// the artifact relative path (the fixture task freezes no manifest), else the
// given manifest path of a file the test adds later.
func fixturePlanVideos(t *testing.T, db *sql.DB, identities map[string]episodeidentity.EpisodeIdentity, manifestPaths map[string]string) []mediafactsfixture.PlanVideo {
	t.Helper()
	artifactPaths := map[string]string{}
	rows, err := db.QueryContext(context.Background(), `SELECT file_ref, relative_path FROM artifact_files WHERE artifact_id = 'artifact-file-test'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var ref, path string
		if err := rows.Scan(&ref, &path); err != nil {
			t.Fatal(err)
		}
		artifactPaths[ref] = path
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	videos := []mediafactsfixture.PlanVideo{}
	for ref, identity := range identities {
		path, ok := artifactPaths[ref]
		if !ok {
			path = manifestPaths[ref]
		}
		if path == "" {
			t.Fatalf("fixture video %s has no path", ref)
		}
		videos = append(videos, mediafactsfixture.PlanVideo{Path: path, Identity: identity})
	}
	return videos
}

// fixtureBatchPlan is the batch plan admission froze for the fixture release
// over the given videos (file ref to identity), route skip.
func fixtureBatchPlan(t *testing.T, db *sql.DB, identities map[string]episodeidentity.EpisodeIdentity) *batchplan.Plan {
	t.Helper()
	plan := mediafactsfixture.Plan(t, batchplan.RouteSkip, fixturePlanVideos(t, db, identities, nil)...)
	return &plan
}
