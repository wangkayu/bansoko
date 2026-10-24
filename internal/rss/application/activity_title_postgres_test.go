package application

import (
	"context"
	"strings"
	"testing"

	"bansoko/internal/redact"
	facts "bansoko/internal/release/facts"
	mediapersistence "bansoko/internal/release/facts/adapter/persistence/media"
	"bansoko/internal/workitems"
)

// TestReleaseEvaluateCompletesForUpstreamTitlesWithSlashesAndSecrets proves a
// Release whose upstream title carries an alias slash, a URL, an absolute path
// or a credential-looking token still completes its evaluation. Before the fix
// the reject-only Activity writer failed the whole owner transaction for such
// titles, so the Release was never evaluated.
func TestReleaseEvaluateCompletesForUpstreamTitlesWithSlashesAndSecrets(t *testing.T) {
	cases := []struct {
		name, title, wantActivityTitle, secret string
	}{
		{"real AniBT alias slash", "[7³ACG] 【我推的孩子】/Oshi no Ko S01 [01-11][1080p][简繁英字幕]", "[7³ACG] 【我推的孩子】/Oshi no Ko S01 [01-11][1080p][简繁英字幕]", ""},
		{"accepted alias slash", "[Group] 【测试番】/Test Show - 01 1080p", "[Group] 【测试番】/Test Show - 01 1080p", ""},
		{"url in title", "[Group] Test Show - 01 1080p http://tracker.example/announce", "[Group] Test Show - 01 1080p " + redact.Redacted, "tracker.example"},
		{"home path in title", "[Group] Test Show - 01 1080p /home/alice/show.mkv", "[Group] Test Show - 01 1080p " + redact.Redacted, "alice"},
		{"token in title", "[Group] Test Show - 01 1080p token=0123456789abcdef", "[Group] Test Show - 01 1080p " + redact.Redacted, "0123456789abcdef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openRSSDatabase(t)
			sourceID, _ := seedRSSOwner(t, db, false, true)
			releaseID := "release-title"
			insertReleaseForEvaluation(t, db, sourceID, releaseID, tc.title, "https://downloads.example.test/show-01.torrent")
			// seedNativeTorrent also copies the title into raw_facts, which is
			// what the Activity title is read from.
			seedNativeTorrent(t, db, "torrent-title", releaseID)

			store, err := workitems.NewStore(db, 0, nil)
			if err != nil {
				t.Fatal(err)
			}
			service, err := newRSSService(t, db, store, &rssFakeSourceFetcher{})
			if err != nil {
				t.Fatal(err)
			}
			service.SetMediaFactsStore(facts.NewStoreWithRepository(mediapersistence.NewRepository(db)))
			claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: releaseID}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, releaseID))
			finishReleaseClaim(t, store, service, claim)

			ctx := context.Background()
			var verdict string
			var evaluationVersion *string
			if err := db.QueryRowContext(ctx, `SELECT verdict, evaluation_version FROM releases WHERE id = $1`, releaseID).Scan(&verdict, &evaluationVersion); err != nil {
				t.Fatal(err)
			}
			if verdict == "discovered" || evaluationVersion == nil || *evaluationVersion == "" {
				t.Fatalf("release was not evaluated: verdict=%q evaluation_version=%v", verdict, evaluationVersion)
			}
			if tc.name == "accepted alias slash" {
				assertReleaseState(t, db, releaseID, "accepted_pending_download", "accepted", true)
			}
			var activityTitle string
			if err := db.QueryRowContext(ctx, `
SELECT title FROM subscription_log_entries
WHERE kind = 'release_processed' AND subject_type = 'release' AND subject_id = $1`, releaseID).Scan(&activityTitle); err != nil {
				t.Fatalf("release_processed activity missing: %v", err)
			}
			if activityTitle != tc.wantActivityTitle {
				t.Fatalf("activity title=%q, want %q", activityTitle, tc.wantActivityTitle)
			}
			if tc.secret != "" && strings.Contains(activityTitle, tc.secret) {
				t.Fatalf("activity title leaked %q: %q", tc.secret, activityTitle)
			}
		})
	}
}
