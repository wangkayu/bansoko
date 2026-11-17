package naming

import (
	acquisitiondomain "bansoko/internal/acquisition/domain"
	"reflect"
	"testing"
)

func TestPendingFactsDistinguishesPreparationFromValue(t *testing.T) {
	variant := CompiledVariant{Directory: CompiledTemplate{ReferencedFacts: []string{"frt.res", "media.resolution", "bgm.ep_title", "tmdb.ep_title"}}, VideoFilename: CompiledTemplate{ReferencedFacts: []string{"media.resolution"}}}
	ctx := NamingContext{}
	ctx.Anime.BGM.WorkID = "bgm"
	ctx.Anime.TMDB.WorkID = "tmdb"
	ctx.Anime.TMDB.MediaKind = "tv"
	ctx.Release.FrozenEpisode = &FrozenEpisodeNamingFacts{Number: "1"}
	want := []string{"bgm.ep_title", "frt.res", "media.resolution", "tmdb.ep_title"}
	if got := variant.PendingFacts(ctx, FactPreparation{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("pending=%v", got)
	}
	for _, state := range []PreparationState{PreparationComplete, PreparationFailed} {
		ready := FactPreparation{FileTitle: state, Media: state, BGMEpisodeTitle: state, TMDBEpisodeTitle: state}
		if got := variant.PendingFacts(ctx, ready); len(got) != 0 {
			t.Fatalf("terminal empty became pending: state=%v facts=%v", state, got)
		}
	}
	ctx.FileTitleTags = &acquisitiondomain.TitleTagSet{}
	ctx.Anime.BGM.WorkID = ""
	ctx.Anime.TMDB.MediaKind = "movie"
	if got := variant.PendingFacts(ctx, FactPreparation{Media: PreparationComplete}); len(got) != 0 {
		t.Fatalf("prepared empty/unbound/movie became pending: %v", got)
	}
	ctx.Anime.BGM.WorkID = "bgm"
	ctx.Anime.TMDB.MediaKind = "tv"
	ctx.Release.FrozenEpisode = nil
	if got := variant.PendingFacts(ctx, FactPreparation{Media: PreparationComplete}); len(got) != 0 {
		t.Fatalf("no episode queued lookup: %v", got)
	}
}
