package application

import (
	"reflect"
	"testing"
)

func TestNamingRecoveryChangeReasonCodes(t *testing.T) {
	cases := []struct {
		name   string
		change NamingRecoveryChange
		want   []string
	}{
		{"nothing", NamingRecoveryChange{}, nil},
		{"title", NamingRecoveryChange{Title: true}, []string{"naming_fact_missing_anime_title", "naming_fact_missing_anime_title_override"}},
		{"media kind", NamingRecoveryChange{MediaKind: true}, []string{"naming_fact_missing_anime_kind"}},
		{"year", NamingRecoveryChange{Year: true}, []string{"naming_fact_missing_anime_year"}},
		{"season", NamingRecoveryChange{Season: true}, []string{"naming_fact_missing_anime_season", "naming_fact_missing_anime_season_folder"}},
		{"season folder match", NamingRecoveryChange{MediaKind: true, Season: true}, []string{"naming_fact_missing_anime_kind", "naming_fact_missing_anime_season", "naming_fact_missing_anime_season_folder"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.change.ReasonCodes(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ReasonCodes() = %v, want %v", got, tc.want)
			}
		})
	}
}
