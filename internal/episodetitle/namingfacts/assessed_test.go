package namingfacts

import "testing"

// A render with no template to assess (a subtitle with an empty suffix
// template) keeps the zero Resolution: it is not assessed and writes no
// episode_titles block, never a block of empty states the render snapshot
// validator refuses.
func TestZeroResolutionWritesNoBlock(t *testing.T) {
	var zero Resolution
	if zero.Assessed() || zero.EpisodeTitlesBlock() != nil {
		t.Fatalf("the zero resolution was assessed: block %v", zero.EpisodeTitlesBlock())
	}
	notRequested := Resolution{BGM: SourceState{State: ReasonNotRequest}, TMDB: SourceState{State: ReasonNotRequest}}
	if notRequested.Assessed() {
		t.Fatal("a not_requested resolution was assessed")
	}
	referenced := Resolution{BGM: SourceState{State: ReasonNotBound}, TMDB: SourceState{State: ReasonNotRequest}}
	if !referenced.Assessed() || referenced.EpisodeTitlesBlock() == nil {
		t.Fatal("a referenced source did not write its block")
	}
}
