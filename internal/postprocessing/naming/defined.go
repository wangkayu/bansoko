package naming

import (
	"fmt"
	"strings"

	"bansoko/internal/templatefacts"
)

// ErrCodeNamingFactUndefined: the template reads a variable that is not
// defined for this render — its namespace has no source here (no Bangumi or
// TMDB binding, no release, no episode identity, a release without that tag
// namespace, another provider's group). It names the variables (Missing) and
// fails whatever the rendered path would be: an undefined variable is a
// configuration or evidence problem, never an empty value.
const ErrCodeNamingFactUndefined = "NAMING_FACT_UNDEFINED"

const namingFactUndefinedReasonPrefix = "naming_fact_undefined_"

// IsFactError reports whether code is a naming refusal that names facts in
// Error.Missing (undefined variables, or empty values that left the path
// invalid).
func IsFactError(code string) bool {
	return code == ErrCodeNamingFactMissing || code == ErrCodeNamingFactUndefined
}

// undefinedFacts returns the facts among empty (sorted, facts the template
// read that have no value) whose namespace has no source in this render. The
// rule is per root and reads only what the render was built from:
//
//   - anime.* and file.* are always defined. anime.year is the owner-edited
//     year and is empty when unset; a file fact the file does not carry (a
//     subtitle flag on a video, a codec not yet read) is designed empty.
//   - bgm.* / tmdb.* are defined when the anime is bound to that provider;
//     a bound work with no recorded title is empty.
//   - release.*, torrent.* and rt.* are defined when the render has a
//     release (every release carries a title-tag block); a tag the title does
//     not carry is empty.
//   - ep.* is defined when the file has an episode identity; ep.number_end is
//     then empty for a single episode.
//   - anibt.* is defined for an AniBT release or source; mikan.* when the
//     source is Mikan.
//
// Every other root (vars has its own NAMING_VARS_UNKNOWN check) is defined.
func undefinedFacts(empty []string, facts *templatefacts.FactSet) []string {
	var undefined []string
	for _, path := range empty {
		if !factDefined(path, facts) {
			undefined = append(undefined, path)
		}
	}
	return undefined
}

func factDefined(path string, facts *templatefacts.FactSet) bool {
	root, _, _ := strings.Cut(path, ".")
	release := strings.TrimSpace(facts.Release.Title) != "" || strings.TrimSpace(facts.Release.ID) != ""
	switch root {
	case "bgm":
		return strings.TrimSpace(facts.Bgm.ID) != ""
	case "tmdb":
		return strings.TrimSpace(facts.Tmdb.ID) != ""
	case "release", "torrent", templatefacts.RootRt:
		// Every release carries a title-tag block (derived when none was
		// stored), so rt.* is defined exactly when there is a release.
		return release
	case "ep":
		return strings.TrimSpace(facts.Ep.Number) != "" || strings.TrimSpace(facts.Ep.Raw) != "" || strings.TrimSpace(facts.Ep.Display) != ""
	case templatefacts.RootAnibt:
		return facts.Anibt != nil
	case "mikan":
		return facts.Mikan != nil
	}
	return true
}

func undefinedFactsError(kind TemplateKind, undefined []string) *Error {
	return &Error{
		Code:    ErrCodeNamingFactUndefined,
		Field:   string(kind),
		Message: fmt.Sprintf("template reads variables that are not defined for this render: %s", strings.Join(undefined, ", ")),
		Missing: append([]string(nil), undefined...),
	}
}
