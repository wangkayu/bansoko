package application

import (
	anime "bansoko/internal/anime/domain"
	catalog "bansoko/internal/catalog/domain"
	"context"
	"testing"
)

func TestMetadataHoldEntryAndBindingMatrix(t *testing.T) {
	for _, branch := range []string{"owner", "saved_revision", "submitted"} {
		for _, binding := range []string{"missing", "empty_bgm", "missing_tmdb", "complete"} {
			for _, mode := range []string{"automatic_source", "subscription_direct", "one_shot_seed", "one_shot_snapshot"} {
				t.Run(branch+"/"+binding+"/"+mode, func(t *testing.T) {
					m := qualificationFixture(t, mode)
					m.config.kind = branch
					m.config.candidateID = "candidate-1"
					m.config.revision = 1
					m.config.revisionDigest = catalog.DigestBytes([]byte("revision"))
					if branch != "saved_revision" {
						m.config.candidateID = ""
						m.config.revision = 0
					}
					m.programs.varsSource = "vars.actual = {title: input.release_title};"
					m.catalog.feed.Seeds[0].Title = "[Group] Show - 01 [1080p]"
					m.bgm = &anime.BGMBinding{WorkId: "123"}
					m.tmdb = &anime.TMDBBinding{WorkId: "456", MediaKind: "tv", Season: 1}
					switch binding {
					case "missing":
						m.bgm = nil
						m.tmdb = nil
					case "empty_bgm":
						m.bgm.WorkId = " "
					case "missing_tmdb":
						m.tmdb = nil
					}
					var err error
					m.catalog.feed.ContentHash, err = catalog.CanonicalFeedSnapshotContentHash(m.catalog.feed.Seeds)
					if err != nil {
						t.Fatal(err)
					}
					c, err := constructQualification(m)
					if err != nil {
						t.Fatal(err)
					}
					intent := serviceIntentFixture(t, c)
					intent.entryMode = mode
					if m.bgm != nil {
						intent.media.Object["bgm"], err = valueOf(frozenValue("known", map[string]any{"ref": m.bgm.WorkId, "version": 1}))
						if err != nil {
							t.Fatal(err)
						}
					}
					if m.tmdb != nil {
						intent.media.Object["tmdb"], err = valueOf(frozenValue("known", map[string]any{"work_ref": m.tmdb.WorkId, "kind": m.tmdb.MediaKind, "season": m.tmdb.Season, "version": 1}))
						if err != nil {
							t.Fatal(err)
						}
					}
					_, err = evaluateAdmission(context.Background(), c, intent)
					if binding != "complete" && (mode == "automatic_source" || mode == "subscription_direct") {
						assertAcceptanceClass(t, err, 422, "ADMISSION_REJECTED")
					} else if err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}
