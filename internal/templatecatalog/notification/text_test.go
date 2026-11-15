package notification

import "testing"

// Path keeps an absolute placement path verbatim, while the free-text Text
// projection still rejects it. A legitimate name may contain "=" or a word
// such as "token"; only the structural safety rules apply to paths.
func TestPathKeepsAbsoluteOutputAndKeepsSafetyRules(t *testing.T) {
	for _, value := range []string{
		"/media/library/TV/Show (2023)/Season 01/Show (2) S01E07.mkv",
		"/media/library/葬送的芙莉莲/字幕 01.zh-Hans.ass",
		"/media/library/Show/token=final episode.mkv",
		"/media/library/Show/credential=abc.mkv",
		"/file.mkv",
	} {
		if got := Path(value); got != value {
			t.Errorf("Path(%q) = %#v", value, got)
		}
		if Text(value) == value {
			t.Errorf("Text(%q) must keep treating an absolute path as private free text", value)
		}
	}
	for _, value := range []string{
		"",
		"relative/Show/ep.mkv",
		"/media/bad\nname.mkv",
		"/media/bad\x7fname.mkv",
		"/media/https://example.invalid/ep.mkv",
	} {
		if got := Path(value); got != nil {
			t.Errorf("Path(%q) = %#v, want nil", value, got)
		}
	}
	for _, value := range []string{
		"Show token=final episode.mkv",
		"Show (2) S01E07.mkv",
		"字幕 01.zh-Hans.ass",
	} {
		if got := Name(value); got != value {
			t.Errorf("Name(%q) = %#v", value, got)
		}
	}
	for _, value := range []string{
		"",
		"Show/ep.mkv",
		"bad\nname.mkv",
		"bad\x7fname.mkv",
		"https://example.invalid/ep.mkv",
	} {
		if got := Name(value); got != nil {
			t.Errorf("Name(%q) = %#v, want nil", value, got)
		}
	}
}
