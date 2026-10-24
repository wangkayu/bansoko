package application

import (
	"context"
	"testing"

	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/testutil/animetest"
)

// An item stored by a resume-baseline refresh reads as "present at resume":
// its verdict is the baseline one, it is not waiting on a re-evaluation even
// when the rules moved on, and its download action names the baseline reason.
func TestRSSListOwnerShowsResumeBaselineItem(t *testing.T) {
	db := animetest.OpenTestDB(t)
	service, root := readmodelNewTestServices(t, db, &readmodelFakeFetcher{})
	subscribed := readmodelSubscribeTestDraft(t, root, "rss-owner-baseline-key")
	sourceID, animeID := string(subscribed.RssSources[0].Id), string(subscribed.ManagedAnime.Id)
	insertReleaseForRulesPreview(t, db, sourceID, "rss-baseline-release", "[TestGroup] Test Show - 01 [1080p]", "https://downloads.example.test/01.torrent")
	if _, err := db.ExecContext(context.Background(), `
UPDATE releases SET verdict = 'resume_baseline', reason_code = 'resume_baseline' WHERE id = 'rss-baseline-release'`); err != nil {
		t.Fatal(err)
	}
	view, err := service.ReadAnimeRSSListView(context.Background(), ReadRSSListViewInput{InputSource: "owner", ManagedAnimeID: animeID})
	if err != nil {
		t.Fatalf("owner read: %v", err)
	}
	if len(view.Groups) != 1 || len(view.Groups[0].Items) != 1 {
		t.Fatalf("owner items = %+v", view.Groups)
	}
	item := view.Groups[0].Items[0]
	if item.Evaluation.Verdict != rssapplication.ReleaseVerdictResumeBaseline || item.Evaluation.State != RSSListEvaluationEvaluated {
		t.Fatalf("baseline evaluation = %+v", item.Evaluation)
	}
	if item.DownloadAction.Allowed || item.DownloadAction.ReasonCode != rssapplication.RetryReasonPresentAtResume {
		t.Fatalf("baseline download action = %+v", item.DownloadAction)
	}
}
