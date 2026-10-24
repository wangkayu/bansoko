package namingvars

import (
	"bansoko/internal/templatefacts"
)

// Sample is one built-in release the editor dry-runs a program over. Its
// evidence goes through BuildInput exactly like a real release, so a sample
// result and a frozen result differ only in the evidence, never in the input
// shape. A sample states only what it has: a value it lacks is absent (null
// to the program), never invented.
type Sample struct {
	Ref      string
	Evidence ReleaseEvidence
}

// Sample refs.
const (
	SampleTV      = "tv_full"
	SampleBare    = "title_only"
	SampleBatch   = "batch"
	SampleDefault = SampleTV
)

func sampleTags() templatefacts.Facts {
	rt := templatefacts.Values{}
	for _, field := range templatefacts.ReltitleFields {
		rt[field.Key] = field.SampleValue
	}
	anibt := templatefacts.Values{}
	for _, field := range templatefacts.AniBTTagFields {
		anibt[field.Key] = field.SampleValue
	}
	return templatefacts.Facts{Reltitle: rt, AniBTTags: anibt}
}

// Samples returns the built-in sample releases in display order.
func Samples() []Sample {
	season, year := int64(1), int64(2025)
	hash := "0123456789abcdef0123456789abcdef01234567"
	return []Sample{
		{Ref: SampleTV, Evidence: ReleaseEvidence{
			AnimeEvidence: AnimeEvidence{
				AnimeID: "sample-anime", AnimeTitle: "Example", AnimeYear: &year,
				BgmID: "100001", BgmTitle: "Example", BgmTitleCN: "示例",
				TmdbID: "200001", TmdbKind: "tv", TmdbTitleCN: "示例", TmdbOriginalTitle: "Example", TmdbSeason: &season,
			},
			ReleaseTitle: "[Sample] Example - 01 [1080p].mkv",
			Files:        []File{{Name: "Example - 01.mkv", Path: "Example - 01.mkv", Size: 734003200}},
			ReleaseID:    "sample-release",
			Episode:      &EpisodeEvidence{Raw: "01", Number: "1", Display: "01", Offset: 0},
			HashV1:       hash,
			Tags:         sampleTags(),
		}},
		{Ref: SampleBare, Evidence: ReleaseEvidence{
			ReleaseTitle: "[Sample] Example - 02 [720p].mkv",
			Files:        []File{{Name: "Example - 02.mkv", Path: "Example - 02.mkv", Size: 367001600}},
		}},
		{Ref: SampleBatch, Evidence: ReleaseEvidence{
			AnimeEvidence: AnimeEvidence{AnimeID: "sample-anime", AnimeTitle: "Example", BgmID: "100001", BgmTitle: "Example", BgmTitleCN: "示例"},
			ReleaseTitle:  "[Sample] Example [BDRip 1080p][Complete]",
			Files: []File{
				{Name: "Example - 01.mkv", Path: "Example/Example - 01.mkv", Size: 734003200},
				{Name: "Example - 02.mkv", Path: "Example/Example - 02.mkv", Size: 734003200},
				{Name: "Example - 03.mkv", Path: "Example/Example - 03.mkv", Size: 734003200},
			},
			ReleaseID: "sample-batch",
			HashV1:    hash,
		}},
	}
}

// SampleByRef returns the built-in sample with ref.
func SampleByRef(ref string) (Sample, bool) {
	for _, sample := range Samples() {
		if sample.Ref == ref {
			return sample, true
		}
	}
	return Sample{}, false
}

// DesignInput is the bounded design input used to discover a program's
// output keys and to validate a program before it is saved or started: the
// full sample release, so a program that reads input.anime.* or input.rt.*
// can be checked. It is a key-discovery sample only and never a frozen
// input.
func DesignInput() Input {
	sample, _ := SampleByRef(SampleDefault)
	return BuildInput(sample.Evidence)
}
