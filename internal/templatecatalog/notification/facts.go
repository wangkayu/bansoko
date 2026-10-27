package notification

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"bansoko/internal/templatefacts"
)

// InputFactKeys are the facts every trigger projects from the frozen
// acquisition input snapshot. Payload keys are registry fact paths.
var InputFactKeys = []string{
	"anime.id", "release.src", "anime.group", "bgm.id", "tmdb.id", "tmdb.kind", "tmdb.season",
	"torrent.hash_v1", "torrent.hash_v2", "torrent.size", "torrent.file_count",
}

// EpisodeFactKeys are the facts projected from one frozen episode identity.
var EpisodeFactKeys = []string{"ep.raw", "ep.number", "ep.display", "ep.offset"}

// InputFacts projects InputFactKeys from an acquisition input snapshot. Only
// known frozen values are used; anything else is null. release.src reads the
// accepted release's provider (anibt|mikan; a custom source has none) and
// anime.group the source's configured group name; neither has a fallback of
// any kind. The SQL twin is notification_input_facts (migration 054); a
// parity test keeps them equal. torrent.size / torrent.file_count exist from
// input schema 4 on and read null for older inputs.
func InputFacts(snapshot []byte) map[string]any {
	facts := make(map[string]any, len(InputFactKeys))
	for _, key := range InputFactKeys {
		facts[key] = nil
	}
	input, ok := decodeObject(snapshot)
	if !ok {
		return facts
	}
	source, _ := input["source"].(map[string]any)
	media, _ := input["media"].(map[string]any)
	config, _ := input["config"].(map[string]any)
	identity, _ := config["identity"].(map[string]any)
	facts["anime.id"] = knownFact(identity["managed_anime_ref"])
	facts["release.src"] = knownFact(source["provider"])
	facts["anime.group"] = knownFact(source["group"])
	facts["bgm.id"] = knownFact(media["bgm"], "ref")
	facts["tmdb.id"] = knownFact(media["tmdb"], "work_ref")
	facts["tmdb.kind"] = knownFact(media["tmdb"], "kind")
	facts["tmdb.season"] = knownFact(media["tmdb"], "season")
	facts["torrent.hash_v1"] = knownFact(source["infohash_v1"])
	facts["torrent.hash_v2"] = knownFact(source["infohash_v2"])
	facts["torrent.size"] = knownFact(source["torrent_size"])
	facts["torrent.file_count"] = knownFact(source["torrent_file_count"])
	return facts
}

// EpisodeFacts projects EpisodeFactKeys from a frozen identity value
// {state, value: IdentityV1}. Only a resolved identity with a known episode
// fills them. The SQL twin is notification_episode_facts (migration 054).
func EpisodeFacts(identity any) map[string]any {
	facts := map[string]any{"ep.raw": nil, "ep.number": nil, "ep.display": nil, "ep.offset": nil}
	fv, _ := identity.(map[string]any)
	if fv["state"] != "known" {
		return facts
	}
	value, _ := fv["value"].(map[string]any)
	if value["state"] != "resolved" {
		return facts
	}
	episode := knownFact(value["episode"])
	if episode == nil {
		return facts
	}
	facts["ep.raw"] = knownFact(value["raw_episode"])
	facts["ep.number"] = episode
	facts["ep.display"] = episode
	switch offset := value["offset"].(type) {
	case json.Number, float64, int, int64:
		facts["ep.offset"] = offset
	}
	return facts
}

// DecodeFacts decodes a JSON object keeping numbers exact.
func DecodeFacts(raw []byte) (map[string]any, bool) {
	return decodeObject(raw)
}

func decodeObject(raw []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, false
	}
	return object, true
}

// knownFact returns the value (or the member at path inside it) of a frozen
// value whose state is known, when that is a non-empty string or a number.
func knownFact(frozen any, path ...string) any {
	fv, ok := frozen.(map[string]any)
	if !ok || fv["state"] != "known" {
		return nil
	}
	value := fv["value"]
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil
		}
		return v
	case json.Number, float64:
		return v
	}
	return nil
}

// ValueFactKeys are the rt.* and anibt.* payload keys, in registry order.
var ValueFactKeys = func() []string {
	keys := []string{}
	for _, field := range templatefacts.ReltitleFields {
		keys = append(keys, templatefacts.RootRt+"."+field.Name)
	}
	for _, field := range templatefacts.AniBTTagFields {
		keys = append(keys, templatefacts.RootAnibt+"."+field.Name)
	}
	return keys
}()

// unsafePayloadKey matches the key names a file notification payload may not
// carry (file_notification_safe, migration 175). A Vars key that matches
// is left out of every payload.
var unsafePayloadKey = regexp.MustCompile(`(secret|token|password|credential|url)`)

// ValueFacts projects the value namespaces onto payload keys:
// vars.<group>.<name> from the frozen Vars output, rt.<field> and
// anibt.<field> from the release's frozen source_facts. text bounds and
// sanitizes each value (nil drops it to null). Every rt/anibt key is present
// (null when absent; rt.uncensored is a boolean); a Vars key is present only when the output has it.
// Undecodable source facts yield no rt/anibt values. The SQL twin is
// notification_value_facts (migration 283); a parity test keeps them equal.
func ValueFacts(vars map[string]any, sourceFacts []byte, text func(string) any) map[string]any {
	facts := make(map[string]any, len(ValueFactKeys)+len(vars))
	for _, key := range ValueFactKeys {
		facts[key] = nil
	}
	for group, raw := range vars {
		values, _ := raw.(map[string]any)
		for name, value := range values {
			key := templatefacts.RootVars + "." + group + "." + name
			raw, ok := value.(string)
			if !ok || unsafePayloadKey.MatchString(strings.ToLower(key)) {
				continue
			}
			facts[key] = text(raw)
		}
	}
	namespaces, err := templatefacts.FromSourceFacts(sourceFacts)
	if err != nil {
		return facts
	}
	for _, field := range templatefacts.ReltitleFields {
		if value, ok := namespaces.Reltitle[field.Key]; ok {
			if field.Type == templatefacts.TypeBoolean {
				facts[templatefacts.RootRt+"."+field.Name] = value == "true"
				continue
			}
			facts[templatefacts.RootRt+"."+field.Name] = text(value)
		}
	}
	for _, field := range templatefacts.AniBTTagFields {
		if value, ok := namespaces.AniBTTags[field.Key]; ok {
			facts[templatefacts.RootAnibt+"."+field.Name] = text(value)
		}
	}
	return facts
}

// InputVarsOutput returns the frozen Vars output of an acquisition input
// snapshot (config.vars.output), or nil.
func InputVarsOutput(snapshot []byte) map[string]any {
	input, ok := decodeObject(snapshot)
	if !ok {
		return nil
	}
	config, _ := input["config"].(map[string]any)
	vars, _ := config["vars"].(map[string]any)
	output, _ := vars["output"].(map[string]any)
	return output
}

// varsPathPattern is a vars.<group>.<name> reference (namingvars key rules).
var varsPathPattern = regexp.MustCompile(`^vars\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// IsVarsPath reports whether name is a well-formed vars.<group>.<name>
// reference. Templates may read any such key: the catalog
// cannot list them because the keys are the user's Vars output.
func IsVarsPath(name string) bool {
	return varsPathPattern.MatchString(name) && !strings.HasPrefix(name, "vars.input.")
}

// TitleFactKeys are the native work titles and the TMDB work summary frozen in
// the acquisition input (schema 5 media.bgm_titles / media.tmdb_titles; the
// overview member is schema-optional: inputs frozen before it existed have no
// summary).
var TitleFactKeys = []string{"bgm.title", "bgm.title_cn", "tmdb.title_cn", "tmdb.original_title", "tmdb.summary"}

// TitleFacts projects TitleFactKeys from an acquisition input snapshot; text
// bounds and sanitizes each value. Inputs before schema 5, unbound works and
// works whose titles were never recorded read null; the overview reads null
// when it was never observed. The SQL twin is notification_title_facts
// (re-created by migration 047); a parity test keeps them equal.
func TitleFacts(snapshot []byte, text func(string) any) map[string]any {
	facts := map[string]any{"bgm.title": nil, "bgm.title_cn": nil, "tmdb.title_cn": nil, "tmdb.original_title": nil, "tmdb.summary": nil}
	input, ok := decodeObject(snapshot)
	if !ok {
		return facts
	}
	media, _ := input["media"].(map[string]any)
	set := func(key string, frozen any, member string) {
		if value, ok := knownFact(frozen, member).(string); ok {
			facts[key] = text(value)
		}
	}
	set("bgm.title", media["bgm_titles"], "title")
	set("bgm.title_cn", media["bgm_titles"], "title_cn")
	set("tmdb.title_cn", media["tmdb_titles"], "title_cn")
	set("tmdb.original_title", media["tmdb_titles"], "original_title")
	set("tmdb.summary", media["tmdb_titles"], "overview")
	return facts
}

// EpisodeTitleFactKeys are the provider episode titles a whitelisted render
// resolved and froze into its render snapshot's episode_titles block.
var EpisodeTitleFactKeys = []string{"bgm.ep_title", "tmdb.ep_title"}

// EpisodeTitleFacts projects EpisodeTitleFactKeys from a frozen render
// snapshot; text bounds and sanitizes each value. Only a known resolved title
// contributes a value: an absent provider answer, an unbound source or an
// unavailable state read null, and a snapshot without the block (the block is
// withheld when no template referenced a fact) contributes nothing. The SQL
// twin is notification_episode_title_facts; a parity test keeps them equal.
func EpisodeTitleFacts(render []byte, text func(string) any) map[string]any {
	facts := map[string]any{"bgm.ep_title": nil, "tmdb.ep_title": nil}
	renderObject, ok := decodeObject(render)
	if !ok {
		return facts
	}
	block, _ := renderObject["episode_titles"].(map[string]any)
	if block == nil {
		return facts
	}
	set := func(key, source string) {
		node, _ := block[source].(map[string]any)
		if node == nil {
			return
		}
		if state, _ := node["state"].(string); state == "known" {
			if value, ok := node["value"].(string); ok {
				facts[key] = text(value)
			}
		}
	}
	set("bgm.ep_title", "bgm")
	set("tmdb.ep_title", "tmdb")
	return facts
}
