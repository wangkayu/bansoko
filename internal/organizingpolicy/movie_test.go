package organizingpolicy

import (
	"reflect"
	"testing"
)

func size(n uint64) *uint64 { return &n }

func TestMovieMainIsLargestKnownSize(t *testing.T) {
	tests := []struct {
		name   string
		videos []Video
		want   FileRef
		reason string
	}{
		{"none", []Video{{Ref: "excluded", Eligible: false, Size: size(999)}}, "", "no_applicable_video"},
		{"unique missing size", []Video{{Ref: "one", Eligible: true}}, "one", "unique_eligible_video"},
		{"largest known", []Video{{Ref: "small", Eligible: true, Size: size(1)}, {Ref: "large", Eligible: true, Size: size(2)}}, "large", "largest_known_size_stable_order"},
		{"stable tie not slice order", []Video{{Ref: "later", Order: 8, Eligible: true, Size: size(2)}, {Ref: "first", Order: 1, Eligible: true, Size: size(2)}}, "first", "largest_known_size_stable_order"},
		{"known before unknown", []Video{{Ref: "unknown", Order: 0, Eligible: true}, {Ref: "known", Order: 1, Eligible: true, Size: size(0)}}, "known", "largest_known_size_stable_order"},
		{"all unknown", []Video{{Ref: "later", Order: 8, Eligible: true}, {Ref: "first", Order: 1, Eligible: true}}, "first", "unknown_size_stable_order"},
		{"no excluded revival", []Video{{Ref: "excluded", Eligible: false, Size: size(999)}, {Ref: "allowed", Eligible: true}}, "allowed", "unique_eligible_video"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := append([]Video(nil), tt.videos...)
			got := SelectMovie(tt.videos)
			if got.Main != tt.want || got.Reason != tt.reason || got.Algorithm != MovieAlgorithm {
				t.Fatalf("got %+v want %s/%s", got, tt.want, tt.reason)
			}
			if !reflect.DeepEqual(before, tt.videos) {
				t.Fatal("mutated inventory")
			}
		})
	}
}
