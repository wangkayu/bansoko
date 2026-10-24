package application

import (
	artifactapplication "bansoko/internal/artifact/application"
	"bansoko/internal/persistence/txscope"
	"context"
	"testing"
)

// TestPGTypedPlanFrozenValuesUnavailable requires an unavailable frozen media
// value to stay unavailable instead of being defaulted into the plan. The
// delivery route root is deliberately not in this list: naming targets are
// absolute, so the frozen route root never reaches the plan.
func TestPGTypedPlanFrozenValuesUnavailable(t *testing.T) {
	for _, field := range []string{"title", "title_override", "bgm", "tmdb"} {
		t.Run(field, func(t *testing.T) {
			db, f := openFileOperationDBWithPolicy(t, func(policy map[string]any) {
				policy["fixture_media_values"].(map[string]any)[field] = map[string]any{"state": "unavailable", "value": nil, "reason_code": "MISSING_METADATA"}
			})
			ctx := context.Background()
			s := newFixtureService(t, db, f)
			tx, err := ownerTestDB(t).BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := seedCommittedInspectionPlanTx(ctx, txscope.Wrap(tx), artifactapplication.VerifiedArtifactRequest{ArtifactID: f.artifactID}, nil); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			c := claimPlanWorkItem(t, db, "unavailable-plan")
			plan, err := s.loadPlan(ctx, c.Owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := planGraphSnapshot(t, db)
			b, err := s.captureArtifactBatch(ctx, plan)
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "title":
				if b.artifact.animeTitle != "" {
					t.Fatal("unknown title defaulted")
				}
			case "title_override":
				if b.artifact.titleOverride.Valid {
					t.Fatal("unknown override defaulted")
				}
			case "bgm":
				if b.artifact.metadataBGM.Valid {
					t.Fatal("unknown BGM defaulted")
				}
			case "tmdb":
				if b.artifact.tmdbWorkID.Valid || b.artifact.tmdbMediaKind.Valid || b.artifact.tmdbSeason.Valid {
					t.Fatal("unknown TMDB defaulted")
				}
			}
			if after := planGraphSnapshot(t, db); after != before {
				t.Fatal("capture wrote graph")
			}
			if _, err := s.PostProcessingPlanHandler().Handle(ctx, c); err != nil {
				t.Fatal(err)
			}
			var state string
			if err := db.QueryRow(`SELECT state FROM work_item_history WHERE id=$1`, c.ID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			if state != "succeeded" {
				t.Fatalf("planning did not finish: %s", state)
			}
		})
	}
}
