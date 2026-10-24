package acquisitionrecord

import (
	"testing"
	"time"

	readmodelapplication "bansoko/internal/anime/readmodel/application"
	"bansoko/internal/transport/httpapi/generated"
)

func summaryFixture(entryKind string, oneShotID *string) readmodelapplication.AcquisitionRecordSummary {
	return readmodelapplication.AcquisitionRecordSummary{
		AcquisitionID: "root-1", EntryKind: entryKind, State: "running", RecordDetail: "full",
		AcceptedAt: time.Unix(5, 0).UTC(), OneShotID: oneShotID,
	}
}

// TestMapRecordSurfacesOneShotMarkerHandle pins that the marker handle reaches
// both the list row and the detail only for a one-shot record; every other
// entry kind maps it to null.
func TestMapRecordSurfacesOneShotMarkerHandle(t *testing.T) {
	marker := "osa-1"
	for _, tc := range []struct {
		name      string
		entryKind string
		oneShotID *string
		want      *string
	}{
		{"one shot carries its marker", "one_shot", &marker, &marker},
		{"automatic has no marker", "automatic", nil, nil},
		{"subscription add has no marker", "subscription_add", nil, nil},
		{"one shot without a resolved marker stays null", "one_shot", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !idIs(MapSummary(summaryFixture(tc.entryKind, tc.oneShotID)).OneShotId, tc.want) {
				t.Fatalf("summary one_shot_id = %v, want %v", MapSummary(summaryFixture(tc.entryKind, tc.oneShotID)).OneShotId, tc.want)
			}
			detail := MapDetail(readmodelapplication.AcquisitionRecordDetail{
				AcquisitionRecordSummary: summaryFixture(tc.entryKind, tc.oneShotID),
				ExecutionBranch:          "download",
			})
			if !idIs(detail.OneShotId, tc.want) {
				t.Fatalf("detail one_shot_id = %v, want %v", detail.OneShotId, tc.want)
			}
		})
	}
}

func idIs(got *generated.ID, want *string) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return string(*got) == *want
}

// TestMapRecordNamesReleaseEpisodesTransferAndPlacedPath pins that the
// identifying release, the frozen episodes, the persisted transfer and a
// file's placed path reach the contract unchanged, and that absent values stay
// absent (an empty episode list never becomes an empty array).
func TestMapRecordNamesReleaseEpisodesTransferAndPlacedPath(t *testing.T) {
	title := "[Group] Show - 03"
	speed := int64(2048)
	observed := time.Unix(9, 0).UTC()
	item := summaryFixture("automatic", nil)
	item.Release = &readmodelapplication.AcquisitionReleaseLink{ReleaseID: "rel-1", Title: readmodelapplication.AcquisitionDisplayValue{State: "visible", Value: &title}}
	item.Episodes = []int{3, 4}
	item.Transfer = &readmodelapplication.AcquisitionTransfer{CompletedBytes: 10, TotalBytes: 40, DownloadSpeed: &speed, ObservedAt: observed}

	summary := MapSummary(item)
	if summary.Release == nil || summary.Release.ReleaseId != "rel-1" || summary.Release.Title.Value == nil || *summary.Release.Title.Value != title {
		t.Fatalf("summary release = %+v", summary.Release)
	}
	if summary.Episodes == nil || len(*summary.Episodes) != 2 || (*summary.Episodes)[0] != 3 {
		t.Fatalf("summary episodes = %v", summary.Episodes)
	}
	if summary.Transfer == nil || summary.Transfer.CompletedBytes != 10 || summary.Transfer.TotalBytes != 40 || *summary.Transfer.DownloadSpeed != speed || !summary.Transfer.ObservedAt.Equal(observed) {
		t.Fatalf("summary transfer = %+v", summary.Transfer)
	}
	detail := MapDetail(readmodelapplication.AcquisitionRecordDetail{AcquisitionRecordSummary: item, ExecutionBranch: "download"})
	if detail.Release == nil || detail.Episodes == nil || detail.Transfer == nil {
		t.Fatalf("detail must carry release, episodes and transfer: %+v", detail)
	}

	bare := MapSummary(summaryFixture("automatic", nil))
	if bare.Release != nil || bare.Episodes != nil || bare.Transfer != nil {
		t.Fatalf("absent values must stay absent: %+v", bare)
	}
	if MapSummary(readmodelapplication.AcquisitionRecordSummary{Episodes: []int{}}).Episodes != nil {
		t.Fatal("an empty episode list must map to absent")
	}

	path := "/library/Show/Season 01/Show - S01E03.mkv"
	file := MapFile(readmodelapplication.AcquisitionFile{FileRef: "f", MediaKind: "video", Requirement: "required",
		PlacedPath: &readmodelapplication.AcquisitionDisplayValue{State: "visible", Value: &path}})
	if file.PlacedPath == nil || file.PlacedPath.Value == nil || *file.PlacedPath.Value != path {
		t.Fatalf("file placed path = %+v", file.PlacedPath)
	}
	if MapFile(readmodelapplication.AcquisitionFile{FileRef: "f"}).PlacedPath != nil {
		t.Fatal("an undelivered file has no placed path")
	}
}
