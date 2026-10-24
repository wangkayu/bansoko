package naming

import (
	"errors"
	"testing"
)

// The unknown variant is an ordinary naming variant with a default like tv and
// movie: users see and edit it, and nothing else decides where an unrecognized
// video goes.
func TestDefaultUnknownTemplateKeepsTheOriginalNameUnderExtras(t *testing.T) {
	engine := NewEngine()
	compiled, err := engine.CompileRulesWithVars(DefaultNamingRules(), map[string]map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	facts := NamingContext{
		Anime: AnimeNamingFacts{Title: "Example", Year: intPtr(2024), TMDB: TMDBNamingFacts{MediaKind: "tv"}},
		File:  FileNamingFacts{OriginalName: "Show - SP1 [1080p].mkv", Basename: "Show - SP1 [1080p]", Extension: "mkv"},
	}
	facts.Derived.EffectiveMediaKind = "tv"
	directory, filename, err := compiled.RenderUnknown(engine, facts)
	if err != nil || directory != "/media/TV/Example (2024)/Extras" || filename != "Show - SP1 [1080p].mkv" {
		t.Fatalf("%q/%q %v", directory, filename, err)
	}
	if facts.Release.FrozenEpisode != nil || facts.Parsed.Episode.Display != "" {
		t.Fatal("unknown render fabricated identity")
	}
	// The name is kept verbatim: the default unknown variant does not rewrite
	// unsafe characters (cleanup is a separate task), and target validation
	// refuses a rendered filename that carries a separator.
	facts.Anime.Title = "Test/Anime"
	facts.File.OriginalName = "Bad/Name:*.mkv"
	facts.File.Basename, facts.File.Extension = "Bad/Name:*", "mkv"
	directory, filename, err = compiled.RenderUnknown(engine, facts)
	if err == nil {
		t.Fatalf("a filename with a separator must be refused: dir=%q file=%q", directory, filename)
	}
}

func TestEveryBuiltInNamingSchemeCarriesAnUnknownVariant(t *testing.T) {
	want := defaultUnknownVariant()
	if DefaultNamingRules().Unknown != want {
		t.Fatalf("default unknown variant = %+v, want %+v", DefaultNamingRules().Unknown, want)
	}
}

// An empty video filename template is refused at save for every variant;
// the unknown variant is not special-cased .
func TestEmptyVideoFilenameTemplateIsRefusedAtSave(t *testing.T) {
	if err := ValidateSavableRules(DefaultNamingRules()); err != nil {
		t.Fatalf("default rules refused: %v", err)
	}
	for _, field := range []string{"tv", "movie", "unknown"} {
		rules := DefaultNamingRules()
		switch field {
		case "tv":
			rules.TV.VideoFilenameTemplate = "  "
		case "movie":
			rules.Movie.VideoFilenameTemplate = ""
		case "unknown":
			rules.Unknown = NamingVariant{}
		}
		err := ValidateSavableRules(rules)
		var namingErr *Error
		if !errors.As(err, &namingErr) || namingErr.Code != ErrCodeNamingTargetInvalid || namingErr.Field != field {
			t.Fatalf("%s: err = %v", field, err)
		}
	}
}
