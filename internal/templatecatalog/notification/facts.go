package notification

import (
	"bytes"
	"encoding/json"
	"fmt"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/episodeidentity"
	"bansoko/internal/templatefacts"
)

// InputFactKeys are the facts every trigger projects from the frozen
// acquisition input snapshot. Payload keys are registry fact paths.
var InputFactKeys = []string{
	"anime.id", "release.src", "anime.group", "bgm.id", "tmdb.id", "tmdb.kind", "tmdb.season",
	"torrent.hash_v1", "torrent.hash_v2", "torrent.size", "torrent.file_count",
}

// EpisodeFactKeys are the facts projected from one frozen episode identity.
var EpisodeFactKeys = []string{"ep.raw", "ep.number", "ep.end", "ep.display", "ep.offset"}

// InputFacts projects InputFactKeys from an acquisition input snapshot. Only
// known frozen values are used; anything else is null. release.src reads the
// captured provenance channel and anime.group the configured group name.
// Neither guesses a value from another identity. The SQL twin is
// notification_input_facts (migration 139); a parity test keeps them equal.
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
	provenance, _ := source["provenance"].(map[string]any)
	switch provider := provenance["provider"]; provider {
	case "anibt", "mikan", "nyaa", "jackett", "custom":
		facts["release.src"] = provider
	}
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

// PositionFacts projects the shared episode position onto notification keys.
// Every key is present; absent values are null.
func PositionFacts(position episodeidentity.Position) map[string]any {
	facts := map[string]any{"ep.raw": nil, "ep.number": nil, "ep.end": nil, "ep.display": nil, "ep.offset": nil}
	if position.Number == "" {
		return facts
	}
	facts["ep.raw"] = TextValue(position.Raw)
	facts["ep.number"] = position.Number
	if position.End != "" && position.End != position.Number {
		facts["ep.end"] = position.End
	}
	facts["ep.display"] = position.Display()
	facts["ep.offset"] = position.Offset
	return facts
}

// EpisodeFacts projects EpisodeFactKeys from a registered identity value
// {state, value: identity}. Only a resolved identity with a known
// episode fills them. The SQL twin is notification_episode_facts (migration 162).
func EpisodeFacts(identity any) map[string]any {
	fv, _ := identity.(map[string]any)
	if fv["state"] != "known" {
		return PositionFacts(episodeidentity.Position{})
	}
	value, _ := fv["value"].(map[string]any)
	if value["state"] != "resolved" {
		return PositionFacts(episodeidentity.Position{})
	}
	episode := knownFact(value["episode"])
	if episode == nil {
		return PositionFacts(episodeidentity.Position{})
	}
	position := episodeidentity.Position{Number: fmt.Sprint(episode)}
	var offsetValue any
	if raw := knownFact(value["raw_episode"]); raw != nil {
		position.Raw = fmt.Sprint(raw)
	}
	if end := knownFact(value["episode_end"]); end != nil {
		position.End = fmt.Sprint(end)
	}
	switch offset := value["offset"].(type) {
	case json.Number:
		if parsed, err := offset.Int64(); err == nil {
			position.Offset = int(parsed)
			offsetValue = offset
		}
	case float64:
		position.Offset = int(offset)
		offsetValue = offset
	case int:
		position.Offset = offset
		offsetValue = offset
	case int64:
		position.Offset = int(offset)
		offsetValue = offset
	}
	facts := PositionFacts(position)
	facts["ep.offset"] = offsetValue
	return facts
}

// StoredEpisodeFacts reads the Go JSON shape persisted in
// media_fact_files.effective_facts->identity and
// media_fact_snapshots.release_identity. Its SQL twin is
// notification_stored_episode_facts (migration 162).
func StoredEpisodeFacts(raw []byte) map[string]any {
	var identity episodeidentity.EpisodeIdentity
	if len(raw) == 0 || json.Unmarshal(raw, &identity) != nil {
		return PositionFacts(episodeidentity.Position{})
	}
	position, ok := identity.Position()
	if !ok {
		return PositionFacts(episodeidentity.Position{})
	}
	return PositionFacts(position)
}

// MediaFileEpisodeFacts reads the file's own identity. The release identity is
// a compatibility fallback only when the legacy snapshot has no file row; a
// present but unresolved file identity deliberately yields null episode facts.
func MediaFileEpisodeFacts(fileIdentity, releaseIdentity []byte) map[string]any {
	if len(fileIdentity) == 0 {
		return StoredEpisodeFacts(releaseIdentity)
	}
	return StoredEpisodeFacts(fileIdentity)
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

// ValueFacts projects rt.<field> and anibt.<field> from the release's frozen
// source_facts. text bounds and sanitizes each value (nil drops it to null).
// Every key is present (null when absent; rt.uncensored is a boolean).
// Undecodable source facts yield no values. The SQL twin is
// notification_value_facts (last re-created by migration 163).
func ValueFacts(sourceFacts []byte, text func(string) any) map[string]any {
	facts := make(map[string]any, len(ValueFactKeys))
	for _, key := range ValueFactKeys {
		facts[key] = nil
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

// FileReltitleFactKeys are the frt.* payload keys under one prefix (frt for
// the file an event is about, taker.frt for the displacing file). A side with
// no frozen parse keeps every key null.
func FileReltitleFactKeys(prefix string) []string {
	keys := make([]string, 0, len(templatefacts.ReltitleFields))
	for _, field := range templatefacts.ReltitleFields {
		keys = append(keys, prefix+"."+field.Name)
	}
	return keys
}

// FileReltitleFacts projects FileReltitleFactKeys from the frozen
// file_title_tags member of a file operation render snapshot. The block was
// parsed once from the original download file name when the operation was
// prepared; a missing block (an old snapshot, a side without a file
// operation) or an undecodable one reads null everywhere and is never
// rebuilt here, and the release namespace is never a fallback. The SQL twin is
// notification_file_reltitle_facts (migration 135); a parity test keeps them
// equal. A caller holding the block itself (a placement's frozen copy) calls
// FileReltitleFactsFromTitleTags directly.
func FileReltitleFacts(render []byte, prefix string, text func(string) any) map[string]any {
	renderObject, ok := decodeObject(render)
	if !ok {
		return FileReltitleFactsFromTitleTags(nil, prefix, text)
	}
	raw, present := renderObject["file_title_tags"]
	if !present || raw == nil {
		return FileReltitleFactsFromTitleTags(nil, prefix, text)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return FileReltitleFactsFromTitleTags(nil, prefix, text)
	}
	return FileReltitleFactsFromTitleTags(encoded, prefix, text)
}

// FileReltitleFactsFromTitleTags projects one already-frozen file_title_tags
// block (the JSON object itself, as stored in a render snapshot or copied
// onto a placement). It is the reader the displaced notification uses: the
// placement keeps its own copy, so an operation compacted away after the fact
// no longer takes the original file-name parse with it. An empty, null or
// malformed block reads null everywhere and is never rebuilt from a library
// name; the SQL twin is notification_file_reltitle_facts.
func FileReltitleFactsFromTitleTags(block []byte, prefix string, text func(string) any) map[string]any {
	facts := make(map[string]any, len(templatefacts.ReltitleFields))
	for _, key := range FileReltitleFactKeys(prefix) {
		facts[key] = nil
	}
	if len(block) == 0 || string(block) == "null" {
		return facts
	}
	var stored acquisitiondomain.StoredTitleTags
	if err := json.Unmarshal(block, &stored); err != nil {
		return facts
	}
	values := templatefacts.ReltitleValues(acquisitiondomain.TitleTagSetFromStored(&stored))
	for _, field := range templatefacts.ReltitleFields {
		value, ok := values[field.Key]
		if !ok {
			continue
		}
		key := prefix + "." + field.Name
		if field.Type == templatefacts.TypeBoolean {
			facts[key] = value == "true"
			continue
		}
		facts[key] = text(value)
	}
	return facts
}

// InputSourceTags returns only native tags frozen in the accepted input.
// Historical absence is not permission to read the current release row.
func InputSourceTags(snapshot []byte) json.RawMessage {
	var input struct {
		Source struct {
			Tags json.RawMessage `json:"tag_evidence"`
		} `json:"source"`
	}
	if json.Unmarshal(snapshot, &input) != nil {
		return nil
	}
	return input.Source.Tags
}

// TitleFactKeys are the native work titles and the TMDB work summary frozen in
// the acquisition input (schema 5 media.bgm_titles / media.tmdb_titles; the
// overview member is schema-optional: inputs frozen before it existed have no
// summary).
var TitleFactKeys = []string{"bgm.title", "bgm.title_cn", "tmdb.title_cn", "tmdb.title", "tmdb.summary"}

// TitleFacts projects TitleFactKeys from an acquisition input snapshot; text
// bounds and sanitizes each value. Inputs before schema 5, unbound works and
// works whose titles were never recorded read null; the overview reads null
// when it was never observed. The SQL twin is notification_title_facts
// (last re-created by migration 132); a parity test keeps them equal.
func TitleFacts(snapshot []byte, text func(string) any) map[string]any {
	facts := map[string]any{"bgm.title": nil, "bgm.title_cn": nil, "tmdb.title_cn": nil, "tmdb.title": nil, "tmdb.summary": nil}
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
	set("tmdb.title", media["tmdb_titles"], "original_title")
	set("tmdb.summary", media["tmdb_titles"], "overview")
	return facts
}

// EpisodeTitleFactKeys are provider episode titles. A naming render may
// already have frozen them; notification admission fills a referenced title
// independently through the shared persistent cache before delivery creation.
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
