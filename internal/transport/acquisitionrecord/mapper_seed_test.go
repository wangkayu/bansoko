package acquisitionrecord

import (
	"testing"

	readmodelapplication "bansoko/internal/anime/readmodel/application"
)

// TestMapRecordSeedIdentityAndReplacement pins that the seed identity and the
// replacement flag reach list row and detail, and that absent values stay
// absent (no empty tag arrays, no false flag).
func TestMapRecordSeedIdentityAndReplacement(t *testing.T) {
	group, provider, hash := "NEST", "mikan", "abc"
	size := int64(1500)
	summary := summaryFixture("automatic", nil)
	summary.Seed = &readmodelapplication.AcquisitionSeedIdentity{GroupName: &group, Provider: &provider, SizeBytes: &size, InfoHash: &hash,
		TitleTags: []string{"resolution:1080p"}}
	summary.ReplacesExisting = true

	row := MapSummary(summary)
	if row.Seed == nil || *row.Seed.GroupName != "NEST" || string(*row.Seed.Provider) != "mikan" || *row.Seed.SizeBytes != 1500 ||
		*row.Seed.InfoHash != "abc" || row.Seed.TitleTags == nil || len(*row.Seed.TitleTags) != 1 || row.Seed.AnibtTags != nil {
		t.Fatalf("summary seed = %+v", row.Seed)
	}
	if row.ReplacesExisting == nil || !*row.ReplacesExisting {
		t.Fatalf("summary replaces_existing = %v", row.ReplacesExisting)
	}
	detail := MapDetail(readmodelapplication.AcquisitionRecordDetail{AcquisitionRecordSummary: summary, ExecutionBranch: "download"})
	if detail.Seed == nil || *detail.Seed.GroupName != "NEST" || detail.ReplacesExisting == nil || !*detail.ReplacesExisting {
		t.Fatalf("detail seed = %+v", detail.Seed)
	}

	plain := MapSummary(summaryFixture("one_shot", nil))
	if plain.Seed != nil || plain.ReplacesExisting != nil {
		t.Fatalf("absent identity must stay absent: %+v %v", plain.Seed, plain.ReplacesExisting)
	}
}
