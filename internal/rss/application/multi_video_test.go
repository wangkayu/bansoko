package application

import (
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/filekinds"
)

func acceptedPreviewResult() animedomain.PreviewReleaseResult {
	return animedomain.PreviewReleaseResult{Verdict: animedomain.ReleaseVerdict("accepted_pending_download")}
}

func multiVideoItem(videoCount int) acquisitiondomain.FeedItem {
	item := acquisitiondomain.FeedItem{}
	for i := 0; i < videoCount; i++ {
		item.ReleaseFiles = append(item.ReleaseFiles, acquisitiondomain.ReleaseFile{
			Filename:  "Show - 01.mkv",
			MediaKind: filekinds.KindVideo,
		})
	}
	return item
}

func TestApplyMultiVideoIgnore(t *testing.T) {
	cases := []struct {
		name        string
		enabled     bool
		manualRetry bool
		item        acquisitiondomain.FeedItem
		wantVerdict string
		wantReason  string
	}{
		{"option off keeps the admission", false, false, multiVideoItem(3), "accepted_pending_download", ""},
		{"two video files are admitted", true, false, multiVideoItem(2), "accepted_pending_download", ""},
		{"three video files are filtered", true, false, multiVideoItem(3), "filtered", ReasonMultiVideoFiles},
		{"an explicit retry skips the gate", true, true, multiVideoItem(3), "accepted_pending_download", ""},
	}
	for _, tc := range cases {
		result := acceptedPreviewResult()
		applyMultiVideoIgnore(&result, tc.item, tc.enabled, tc.manualRetry)
		if string(result.Verdict) != tc.wantVerdict {
			t.Errorf("%s: verdict = %s, want %s", tc.name, result.Verdict, tc.wantVerdict)
		}
		if result.ReasonCode != tc.wantReason {
			t.Errorf("%s: reason = %s, want %s", tc.name, result.ReasonCode, tc.wantReason)
		}
	}
}

func TestApplyMultiVideoIgnoreCountsEveryDepth(t *testing.T) {
	item := acquisitiondomain.FeedItem{ReleaseFiles: []acquisitiondomain.ReleaseFile{
		{Filename: "Show - 01.mkv", MediaKind: filekinds.KindVideo},
		{Filename: "Extras/disc2/Show - 02.mkv", MediaKind: filekinds.KindVideo},
		{Filename: "Extras/disc3/Show - 03.mkv", MediaKind: filekinds.KindVideo},
		{Filename: "Show - 01.ass", MediaKind: "subtitle"},
	}}
	result := acceptedPreviewResult()
	applyMultiVideoIgnore(&result, item, true, false)
	if result.Verdict != animedomain.ReleaseVerdict("filtered") || result.ReasonCode != ReasonMultiVideoFiles {
		t.Fatalf("deep videos must count: verdict=%s reason=%s", result.Verdict, result.ReasonCode)
	}
	if result.ReasonDetail == nil || *result.ReasonDetail != "video files: 3" {
		t.Fatalf("reason detail = %v, want the video count", result.ReasonDetail)
	}
}

func TestApplyMultiVideoIgnoreKeepsEarlierFilterReasons(t *testing.T) {
	detail := "no candidate episode"
	result := animedomain.PreviewReleaseResult{Verdict: animedomain.ReleaseVerdict("filtered"), ReasonCode: "no_episode", ReasonDetail: &detail}
	applyMultiVideoIgnore(&result, multiVideoItem(5), true, false)
	if result.Verdict != animedomain.ReleaseVerdict("filtered") || result.ReasonCode != "no_episode" || *result.ReasonDetail != detail {
		t.Fatalf("an earlier filter reason must keep its verdict: %+v", result)
	}
}

func TestCountVideoReleaseFiles(t *testing.T) {
	if got := countVideoReleaseFiles(nil); got != 0 {
		t.Fatalf("no files = %d, want 0", got)
	}
	if got := countVideoReleaseFiles(multiVideoItem(4).ReleaseFiles); got != 4 {
		t.Fatalf("count = %d, want 4", got)
	}
}
