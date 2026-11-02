package application

import (
	"context"
	"database/sql"
	"testing"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/subscription/competition"
	"bansoko/internal/workitems"
)

// midFlightCompetitionDecider only has to be non-nil: the owner writer asks
// whether the competition owns admission, and never runs a round itself.
type midFlightCompetitionDecider struct{}

func (midFlightCompetitionDecider) CompetitionDecisions(context.Context, *sql.Tx, string, map[string]bool) (competition.Round, error) {
	return competition.Round{}, nil
}

// TestReleaseEvaluationAppliesMultiVideoToggleMidFlight pins the owner write
// against a subscription option that changed after the release snapshot was
// loaded. The competition path refreshes the snapshot inputs inside the owner
// transaction; when it forgets the ignore-multi-video option, the gate below
// reads the stale value and writes the verdict the option was just swept for:
// OFF→ON persists awaiting_competition (escaping the sweep) and ON→OFF
// persists filtered/multi_video_files.
func TestReleaseEvaluationAppliesMultiVideoToggleMidFlight(t *testing.T) {
	filtered := animedomain.PreviewReleaseResult{
		Verdict:    animedomain.ReleaseVerdict("filtered"),
		ReasonCode: ReasonMultiVideoFiles,
	}
	awaiting := animedomain.PreviewReleaseResult{
		Verdict:    animedomain.ReleaseVerdict(competition.VerdictAwaiting),
		ReasonCode: competition.VerdictAwaiting,
	}
	cases := []struct {
		name        string
		before      bool
		after       bool
		initial     animedomain.PreviewReleaseResult
		wantVerdict string
		wantReason  string
	}{
		{
			name: "off to on filters the waiting release",
			// The loaded snapshot admitted the release; the toggle landed
			// before the owner write, so the current option must filter it.
			before: false, after: true, initial: awaiting,
			wantVerdict: "filtered", wantReason: ReasonMultiVideoFiles,
		},
		{
			name: "on to off releases the filtered verdict",
			// The loaded snapshot filtered the release; the toggle landed
			// before the owner write, so the current option must admit it
			// again into the competition wait.
			before: true, after: false, initial: filtered,
			wantVerdict: competition.VerdictAwaiting, wantReason: competition.VerdictAwaiting,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openRSSDatabase(t)
			sourceID, subscriptionID := seedRSSOwner(t, db, false, true)
			if _, err := db.ExecContext(ctx, `
UPDATE subscriptions SET ignore_multi_video_releases = $2 WHERE id = $1`, subscriptionID, tc.before); err != nil {
				t.Fatal(err)
			}

			const releaseID = "release-toggle"
			insertReleaseForEvaluation(t, db, sourceID, releaseID, "[Group] Test Show - 01 1080p", "https://downloads.example.test/show-01.torrent")
			// The release carries three video files: one over the automatic
			// admission gate.
			item := multiVideoItem(3)
			item.GUID = releaseID + "-guid"
			item.Title = "[Group] Test Show - 01 1080p"
			item.Enclosure = "https://downloads.example.test/show-01.torrent"
			item.TorrentURL = item.Enclosure
			encoded, err := encodeInternalFeedFacts(item)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `
UPDATE releases
SET internal_facts = $2::jsonb, raw_facts = jsonb_build_object('title', $3::text)
WHERE id = $1`, releaseID, encoded, item.Title); err != nil {
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
			// A wired competition makes the active subscription's admission
			// stage the competition wait, exactly where the stale option
			// escaped the sweep.
			service.SetSubscriptionCompetition(midFlightCompetitionDecider{})

			// Freeze the inputs under the old option, the way the evaluation
			// handler loads them before the owner transaction.
			snapshot, err := service.loadReleaseSnapshot(ctx, releaseID)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.ignoreMultiVideoReleases != tc.before {
				t.Fatalf("snapshot option = %v, want %v", snapshot.ignoreMultiVideoReleases, tc.before)
			}
			snapshotItem, err := decodeFeedItemWithInternal(snapshot.rawFacts, snapshot.internalFacts, snapshot.sourceFacts, snapshot.parsedFacts, snapshot.stableItemKey)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(snapshotItem.ReleaseFiles); got != 3 {
				t.Fatalf("snapshot release files = %d, want 3", got)
			}

			// The toggle lands between the snapshot and the owner transaction.
			if _, err := db.ExecContext(ctx, `
UPDATE subscriptions SET ignore_multi_video_releases = $2 WHERE id = $1`, subscriptionID, tc.after); err != nil {
				t.Fatal(err)
			}

			claim := createRSSClaim(t, db, store, workitems.OwnerRef{Type: workitems.OwnerRelease, ID: releaseID}, workitems.KindReleaseEvaluate, currentReleaseVersion(t, db, releaseID))
			// acquisition is nil: this is the competition path, the one that
			// refreshes the snapshot inside the owner transaction.
			writer := service.releaseOwnerWriter(claim, snapshot, snapshotItem, tc.initial, nil, nil)
			if err := store.FinishWithResult(ctx, claim, workitems.StateSucceeded, workitems.OwnerResult{State: string(tc.initial.Verdict), ReasonCode: tc.initial.ReasonCode}, writer); err != nil {
				t.Fatal(err)
			}
			assertReleaseState(t, db, releaseID, tc.wantVerdict, tc.wantReason, false)
			var storedVerdict, storedReason string
			if err := db.QueryRowContext(ctx, `SELECT verdict, reason_code FROM releases WHERE id = $1`, releaseID).Scan(&storedVerdict, &storedReason); err != nil {
				t.Fatal(err)
			}
			if storedVerdict == competition.VerdictAwaiting && tc.wantVerdict != competition.VerdictAwaiting {
				t.Fatalf("the release escaped the sweep: verdict=%q reason=%q", storedVerdict, storedReason)
			}
		})
	}
}
