package reltitletags

import "testing"

func TestWeakYearUsesStandaloneYearTokens(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  int
	}{
		{"Show S3 [2026]", 2026},
		{"New Doraemon [707][2022.05.21][AVC][1080P]", 0},
		{"Show S3 [2022.05.21] [2026] [1080P]", 2026},
		{"Show S3 [2026] [2025] [1080P]", 0},
		{"Show S3 [2026] [2026] [1080P]", 2026},
	} {
		t.Run(tc.title, func(t *testing.T) {
			got := WeakYear(tc.title)
			if tc.want == 0 && got != nil || tc.want != 0 && (got == nil || *got != tc.want) {
				t.Fatalf("WeakYear(%q) = %v, want %d (zero means absent)", tc.title, got, tc.want)
			}
		})
	}
}
