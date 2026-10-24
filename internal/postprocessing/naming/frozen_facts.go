package naming

import (
	"strings"

	providerdomain "bansoko/internal/provider/domain"
)

// ReplayFactPaths is the canonical, ordered name of every replay fact. It is the
// single source of truth for the set: the render snapshot writer, the FileFact
// projection and the database's source-relation validator must enumerate exactly
// these names, or a re-render can read a fact the effect never froze (and the
// validator would accept a forged one).
func ReplayFactPaths() []string {
	return []string{
		"File.Media.Resolution", "File.Media.VideoCodec", "File.Media.Container",
		"Parsed.Language.Code", "Parsed.Episode.Display",
		"Source.GroupName", "Source.Group",
		"Release.OriginalTitle", "Release.FrozenEpisode.Display",
		"Anime.BGM.WorkID", "Anime.TMDB.WorkID",
	}
}

// ReplayFactValues returns the catalog facts a maintenance re-render has to be
// able to reproduce, each with the value this context resolved for it.
//
// The set is deliberately bounded to facts whose evidence can move between the
// acquisition render and a later maintenance render: the per-file media facts,
// the language and episode the resolver produced, the group and its provider
// identity, the release title, and the metadata work ids. Always-present derived
// values (the effective title,
// the extension) are not replay facts: the resolver fills them from the file
// itself, so "empty" there is a template or fixture error rather than lost
// frozen evidence.
//
// The same map is written into the operation's frozen render snapshot by the
// file effect and read back by the maintenance render, which is what lets the
// guard tell "the effect never had this fact" (render on) from "the effect had
// it and the evidence no longer does" (refuse the render).
func ReplayFactValues(ctx NamingContext) map[string]string {
	frozenEpisode := ""
	if ctx.Release.FrozenEpisode != nil {
		frozenEpisode = ctx.Release.FrozenEpisode.Display
	}
	values := map[string]string{
		"File.Media.Resolution":         strings.TrimSpace(ctx.File.Media.Resolution),
		"File.Media.VideoCodec":         strings.TrimSpace(ctx.File.Media.VideoCodec),
		"File.Media.Container":          strings.TrimSpace(ctx.File.Media.Container),
		"Parsed.Language.Code":          strings.TrimSpace(ctx.Parsed.Language.Code),
		"Parsed.Episode.Display":        strings.TrimSpace(ctx.Parsed.Episode.Display),
		"Source.GroupName":              strings.TrimSpace(ctx.Source.GroupName),
		"Source.Group":                  sourceGroupKey(ctx.Source.Group),
		"Release.OriginalTitle":         strings.TrimSpace(ctx.Release.OriginalTitle),
		"Release.FrozenEpisode.Display": strings.TrimSpace(frozenEpisode),
		"Anime.BGM.WorkID":              strings.TrimSpace(ctx.Anime.BGM.WorkID),
		"Anime.TMDB.WorkID":             strings.TrimSpace(ctx.Anime.TMDB.WorkID),
	}
	// Guard the two enumerations against drift: a path in ReplayFactPaths with no
	// value here would silently drop out of the snapshot.
	if len(values) != len(ReplayFactPaths()) {
		panic("naming: ReplayFactValues does not cover every ReplayFactPaths entry")
	}
	return values
}

// sourceGroupKey is the provider-namespaced external group identity of the
// origin RSS source (mikan:<id> / anibt:<slug>), or "" when the source records
// none (a custom RSS source, or a provider_group source without provenance).
func sourceGroupKey(group providerdomain.GroupRef) string {
	if group.Provider == "" || group.ExternalID == "" {
		return ""
	}
	return group.Key()
}
