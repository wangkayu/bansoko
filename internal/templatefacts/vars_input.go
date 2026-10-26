package templatefacts

import "strings"

// varsInputSystemPaths are the system facts a naming-vars-v1 program reads
// under input.<root>: the release-level evidence every Vars call
// site can supply from the same frozen sources. Naming-resolver outputs that
// depend on the render configuration (anime.kind / season_folder,
// release.lang*, release.subtitle_mode, ep.source, ep.kind) are not evidence
// and stay out; so do the facts acceptance does not freeze
// (anime.title_source, release.published_at, torrent.name) — and the group
// leaves (retired release.group / group_source, never listed) are not Vars
// inputs either, just as the withdrawn src.* leaves never were. anime.year and
// anime.season are the owner-edited values
// (anime.season since naming-render-4; an input frozen before it lacks the
// member, see VarsInputOptionalMembers). Every rt.* and anibt.* field is added
// after these.
var varsInputSystemPaths = []string{
	"anime.id", "anime.title", "anime.title_override", "anime.provider_title", "anime.title_base",
	"anime.year", "anime.season",
	"bgm.id", "bgm.title", "bgm.title_cn",
	"tmdb.id", "tmdb.title_cn", "tmdb.original_title", "tmdb.kind", "tmdb.season",
	"release.id", "release.title",
	"ep.number", "ep.raw", "ep.display", "ep.offset",
	"torrent.size", "torrent.file_count", "torrent.hash_v1", "torrent.hash_v2",
}

// VarsInputOptionalMembers are Vars input members added after inputs without
// them were frozen: a stored input may lack them (it keeps its exact canonical
// form and digest), while every new input carries them. Keyed by root, then
// leaf.
var VarsInputOptionalMembers = map[string][]string{"anime": {"season"}, "rt": {"version", "content_rating"}}

// VarsInputFacts returns the registered facts of the Vars v2 input, in a
// stable order: the system facts, then rt.*, then anibt.*.
func VarsInputFacts() []Fact {
	out := make([]Fact, 0, len(varsInputSystemPaths)+len(ReltitleFields)+len(AniBTTagFields))
	for _, path := range varsInputSystemPaths {
		fact, ok := Lookup(path)
		if !ok {
			panic("templatefacts: vars input fact not registered: " + path)
		}
		out = append(out, fact)
	}
	for _, fact := range registry {
		if root := fact.Root(); root == RootRt || root == RootAnibt {
			out = append(out, fact)
		}
	}
	return out
}

// VarsInputObject is the nested fact object a naming-vars-v1 program reads
// (input.anime, input.bgm, ...). Every fact is present; a missing value is
// null, never a guessed one.
func VarsInputObject(fs *FactSet) map[string]any {
	out := map[string]any{}
	for _, fact := range VarsInputFacts() {
		segments := strings.Split(fact.Path, ".")
		node := out
		for _, segment := range segments[:len(segments)-1] {
			child, ok := node[segment].(map[string]any)
			if !ok {
				child = map[string]any{}
				node[segment] = child
			}
			node = child
		}
		node[segments[len(segments)-1]] = fact.Value(fs)
	}
	return out
}
