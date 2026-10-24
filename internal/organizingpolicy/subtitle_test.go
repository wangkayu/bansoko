package organizingpolicy

import "testing"

func TestSubtitlesRequireFilteredSetAndFinalVideo(t *testing.T) {
	subtitles := []Subtitle{{"allowed", true}, {"filtered", false}, {"other-version", true}, {"ambiguous", true}, {"missing", true}, {"duplicate", true}}
	bindings := []Binding{{"allowed", "final", Bound}, {"filtered", "final", Bound}, {"other-version", "excluded-video", Bound}, {"ambiguous", "final", Ambiguous}, {"duplicate", "final", Bound}, {"duplicate", "final", Bound}}
	got := AdmitSubtitles(subtitles, []FileRef{"final"}, bindings)
	expected := []string{"bound_final_video", "filtered", "video_not_final", "binding_ambiguous", "binding_missing", "binding_ambiguous"}
	for i, d := range got {
		if d.Reason != expected[i] || d.Allowed != (i == 0) {
			t.Fatalf("%d: %+v expected %s", i, d, expected[i])
		}
		if i != 0 && d.Video != "" {
			t.Fatal("rejected subtitle has target")
		}
	}
}
func TestSubtitleUsesFileRefNotBasenameAndNeverAddsFilteredInput(t *testing.T) {
	got := AdmitSubtitles([]Subtitle{{"folder-a/ep.ass", true}}, []FileRef{"folder-a/ep.mkv"}, []Binding{{"folder-a/ep.ass", "folder-b/ep.mkv", Bound}, {"not-in-filtered-set", "folder-a/ep.mkv", Bound}})
	if len(got) != 1 || got[0].Allowed || got[0].Reason != "video_not_final" {
		t.Fatalf("identity boundary=%+v", got)
	}
}
