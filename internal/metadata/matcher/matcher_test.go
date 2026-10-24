package matcher

import (
	"context"
	"errors"
	"testing"

	configurationdomain "bansoko/internal/configuration/domain"
	observabilityport "bansoko/internal/observability/port"
)

func TestMatcherUsesBangumiDataForMikanAndLeavesTVSeasonUnresolved(t *testing.T) {
	index := &BangumiDataIndex{
		byMikanID: map[string]*DataItem{
			"3967": {BgmID: "1234", TmdbID: "tv/5678", Titles: []string{"Show"}},
		},
		byTitle: map[string]*DataItem{},
	}
	result, err := NewMatcher(index, nil, nil, observabilityport.NopRuntimeLogger{}).Match(context.Background(), "mikan", "3967", "Show", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsComplete() || result.Status != StatusPartial || result.BgmID == nil || *result.BgmID != "1234" || result.TmdbWorkID == nil || *result.TmdbWorkID != "5678" || result.TmdbKind == nil || *result.TmdbKind != "tv" || result.TmdbSeason != nil {
		t.Fatalf("bangumi-data TV match must not invent a season: %+v", result)
	}
}

func TestMatcherCompletesBangumiDataMovieWithSeasonZero(t *testing.T) {
	index := NewBangumiDataIndex([]DataItem{{BgmID: "1234", TmdbID: "movie/5678", Titles: []string{"Movie"}}})
	result, err := NewMatcher(index, nil, nil, nil).Match(context.Background(), "custom", "", "Movie", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsComplete() || result.Status != StatusComplete || result.TmdbSeason == nil || *result.TmdbSeason != 0 {
		t.Fatalf("bangumi-data movie match must use season zero: %+v", result)
	}
}

func TestMatcherReportsPartialBangumiDataResult(t *testing.T) {
	result, err := NewMatcher(NewBangumiDataIndex([]DataItem{{BgmID: "1234", Titles: []string{"Only BGM"}}}), nil, nil, nil).Match(context.Background(), "custom", "", "Only BGM", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.HasAny() || result.IsComplete() || result.Status != StatusPartial {
		t.Fatalf("partial metadata result should be explicit: %+v", result)
	}
}

func TestMatcherLeavesUnmatchedMetadataEmptyForNextCycle(t *testing.T) {
	result, err := NewMatcher(&BangumiDataIndex{byMikanID: map[string]*DataItem{}, byTitle: map[string]*DataItem{}}, nil, nil, nil).Match(context.Background(), "custom", "", "Unknown", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || result.HasAny() || result.IsComplete() || result.Status != StatusNoResult {
		t.Fatalf("unmatched metadata should remain unresolved: %+v", result)
	}
}

type matcherErrorConnections struct {
	err error
}

func (c matcherErrorConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return nil, c.err
}

func TestTMDBMatcherReportsConfigurationError(t *testing.T) {
	_, err := (&TMDBMatcher{}).Match(context.Background(), "Show")
	if !IsMatchErrorKind(err, ErrorConfig) {
		t.Fatalf("expected config error, got %v", err)
	}
	var matchErr *MatchError
	if !errors.As(err, &matchErr) || matchErr.Layer != "tmdb" {
		t.Fatalf("expected typed TMDB error, got %v", err)
	}
}

func TestTMDBMatcherReportsConnectionProviderError(t *testing.T) {
	_, err := (&TMDBMatcher{Connections: matcherErrorConnections{err: errors.New("connection source unavailable")}}).Match(context.Background(), "Show")
	if !IsMatchErrorKind(err, ErrorProvider) {
		t.Fatalf("expected provider error, got %v", err)
	}
}
