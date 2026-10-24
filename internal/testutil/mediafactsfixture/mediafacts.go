// Package mediafactsfixture freezes Release media facts for PostgreSQL tests
// that seed a Release directly with SQL instead of through an admission.
package mediafactsfixture

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/batchplan"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/filekinds"
	"bansoko/internal/parser"
	"bansoko/internal/persistence/txscope"
	facts "bansoko/internal/release/facts"
)

// Video is one video file's inspection input: Name is the file name its
// episode is resolved from, exactly as artifact inspection resolves it. Path
// is the manifest path the batch plan keys the file by (the artifact relative
// path when the download task froze no manifest); empty means Name.
type Video struct {
	FileRef string
	Name    string
	Path    string
	Size    int64
}

// PlanVideo is one first-level video of a fixture batch plan: the manifest
// path prepare maps the artifact file to, and the identity its media fact row
// carries. Prepare refuses a video whose row and plan entry disagree.
type PlanVideo struct {
	Path     string
	Identity episodeidentity.EpisodeIdentity
}

// Plan classifies fixture videos into the batch plan an admission freezes:
// each video keeps the given identity, nothing is deduplicated, and the plan
// carries the given unknown-file route.
func Plan(t testing.TB, route string, videos ...PlanVideo) batchplan.Plan {
	t.Helper()
	identities := make(map[string]episodeidentity.EpisodeIdentity, len(videos))
	files := make([]batchplan.InputFile, 0, len(videos))
	for _, video := range videos {
		path := batchplan.NormalizePath(video.Path)
		identities[path] = video.Identity
		files = append(files, batchplan.InputFile{SourcePath: path})
	}
	plan, err := batchplan.Classify(batchplan.Input{
		FileKindsVersion: filekinds.Version, UnclassifiedRoute: route, Files: files,
		Identity: func(sourcePath string) (episodeidentity.EpisodeIdentity, error) {
			return identities[sourcePath], nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// ReleaseFactsJSON is a release_facts object for SQL-seeded snapshots: the
// given fields plus the fixture batch plan of the videos.
func ReleaseFactsJSON(t testing.TB, fields map[string]any, route string, videos ...PlanVideo) string {
	t.Helper()
	out := map[string]any{}
	for key, value := range fields {
		out[key] = value
	}
	out["batch_plan"] = Plan(t, route, videos...)
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// Freeze writes what admission and inspection write for a directly seeded
// Release: the Release media fact snapshot through the shared admission writer
// (regex parser, TV season 1, no offset) with the batch plan of the
// videos (route skip), then one per-file row per video with the identity
// resolved from that snapshot's parser evidence.
// Prepare refuses a video without them (media_facts_missing). Every video
// must resolve; the helper fails the test otherwise.
func Freeze(t *testing.T, tx *sql.Tx, releaseID, title string, videos ...Video) {
	t.Helper()
	ctx := context.Background()
	evidence, err := acquisitiontarget.ResolveParserEvidence(ctx, acquisitiontarget.ParserSelectionInput{
		Kind: string(parser.ParserRegex), EpisodePatterns: acquisitiondomain.DefaultEpisodePatterns,
		MediaKind: "tv", Season: 1,
	}, title)
	if err != nil {
		t.Fatal(err)
	}
	identities := make([]episodeidentity.EpisodeIdentity, len(videos))
	planVideos := make([]PlanVideo, len(videos))
	for index, video := range videos {
		identity, _, err := acquisitiontarget.ResolveFileEpisode(ctx, evidence, video.Name)
		if err != nil {
			t.Fatal(err)
		}
		if identity.State != episodeidentity.StateResolved {
			t.Fatalf("fixture video %q did not resolve: %+v", video.Name, identity)
		}
		path := video.Path
		if path == "" {
			path = video.Name
		}
		identities[index] = identity
		planVideos[index] = PlanVideo{Path: path, Identity: identity}
	}
	plan := Plan(t, batchplan.RouteSkip, planVideos...)
	store := facts.NewStore(nil)
	scope := txscope.Wrap(tx)
	if err := acquisitiontarget.FreezeReleaseMediaSnapshotTx(ctx, scope, store, acquisitiontarget.ReleaseMediaSnapshotInput{
		ReleaseID: releaseID, Title: title, Evidence: evidence, Plan: &plan,
	}); err != nil {
		t.Fatal(err)
	}
	var snapshotID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM media_fact_snapshots WHERE release_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, releaseID).Scan(&snapshotID); err != nil {
		t.Fatal(err)
	}
	for index, video := range videos {
		identity := identities[index]
		if err := store.UpsertFileFactsTx(ctx, scope, facts.FileRow{
			SnapshotID: snapshotID, FileRef: video.FileRef, MediaKind: "video", SizeBytes: video.Size,
			FileEvidence:   facts.FileEvidence{Name: video.Name, MediaKind: "video", SizeBytes: video.Size},
			EffectiveFacts: facts.EffectiveFileFacts{Identity: identity, MappingVersion: facts.MappingVersion},
		}); err != nil {
			t.Fatal(err)
		}
	}
}
