package redact

import "testing"

func TestApplyLibraryRelativeKeepsFoldersAndRedactsSecrets(t *testing.T) {
	for _, value := range []string{"Title 第二季 （2021）/Season 02/Title - S02E01.mkv", "T (2021) /Season 1/a.mkv", "[Group] T/S1/a.ass"} {
		if got, sensitive := ApplyLibraryRelative(value, 512, Reject, Redacted); sensitive || got != value {
			t.Errorf("ApplyLibraryRelative(%q) = %q, %v", value, got, sensitive)
		}
	}
	for _, value := range []string{"/media/anime/T/S1/a.mkv", "../T/a.mkv", "T/token=abc/a.mkv", "https://x.test/a.mkv"} {
		if _, sensitive := ApplyLibraryRelative(value, 512, Reject, Redacted); !sensitive {
			t.Errorf("ApplyLibraryRelative(%q) not sensitive", value)
		}
	}
}
