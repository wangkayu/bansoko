package application

import (
	"bansoko/internal/testutil/animetest"
	"context"
	"testing"
)

func TestSubgroupsOwnerProjectsPersistedAvailabilityAndGeneration(t *testing.T) {
	db := animetest.OpenTestDB(t)
	s := ownershipNewTestService(t, db, &ownershipFakeFetcher{})
	created := ownershipSubscribeTestDraft(t, s, "availability-read-key")
	animeID := created.ManagedAnime.Id
	var subscriptionID string
	if err := db.QueryRow(`SELECT id FROM subscriptions WHERE managed_anime_id=$1`, animeID).Scan(&subscriptionID); err != nil {
		t.Fatal(err)
	}
	// Auto observations have no manual intent; retired manual includes retain
	// the user's explicit intent even though availability blocks admission.
	if _, err := db.Exec(`INSERT INTO provider_group_selections(id,subscription_id,provider,group_ref,display_name,selection_mode,manual_selected,effective_selected,effective_score,count_in_limit,available,availability_generation,version) VALUES('availability-active',$1,'anibt','active','Active','auto',NULL,TRUE,80,TRUE,TRUE,17,9),('availability-retired',$1,'anibt','retired','Retired','manual',TRUE,TRUE,95,FALSE,FALSE,23,11)`, subscriptionID); err != nil {
		t.Fatal(err)
	}
	got, err := s.readSubgroupsOwnerApplication(context.Background(), animeID, subscriptionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.EffectiveSelection) != 2 {
		t.Fatalf("persisted rows=%+v", got.EffectiveSelection)
	}
	for _, row := range got.EffectiveSelection {
		switch row.GroupRef {
		case "active":
			if !row.Available || row.AvailabilityGeneration != 17 || row.Version != 9 || !row.EffectiveSelected {
				t.Fatalf("active projection=%+v", row)
			}
			// An available auto group that won its slot says so, so the row can
			// explain "why is this one subscribed" as well as the opposite.
			if row.ReasonCode == nil || *row.ReasonCode != "ranked_within_max_rss" {
				t.Fatalf("active reason=%v, want ranked_within_max_rss", row.ReasonCode)
			}
		case "retired":
			if row.Available || row.AvailabilityGeneration != 23 || row.Version != 11 || row.EffectiveSelected {
				t.Fatalf("retired projection=%+v", row)
			}
			// A group the provider no longer lists is unavailable, not merely
			// outscored: an unselected retired manual row would otherwise be
			// explained as a policy verdict it never received.
			if row.ReasonCode == nil || *row.ReasonCode != "provider_group_not_observed" {
				t.Fatalf("retired reason=%v, want provider_group_not_observed", row.ReasonCode)
			}
		default:
			t.Fatalf("synthetic policy row leaked: %+v", row)
		}
	}
}
