package domain

import (
	"strconv"
	"strings"

	"bansoko/internal/episodeidentity"
)

// IdentityV2 is IdentityV1 plus multi-episode files: a resolved TV
// identity of shape range or list occupies 2..MaxEpisodesPerFile consecutive
// coverage keys, from episode to episode_end, all of one key shape (the keys of
// a sealed v1 identity all name one season). A single
// episode, a movie and every non-resolved identity read exactly as in V1, with
// episode_end absent. IdentityV1 stays frozen.
func init() {
	register("IdentityV2", Obj(
		F("state", Enum("resolved", "unresolved", "invalid", "unsupported", "conflict")),
		F("kind", Enum("tv_episode", "movie")),
		F("shape", Enum("single", "range", "list", "season_only", "special", "unknown")),
		F("raw_episode", FVOf(Episode)), F("episode", FVOf(Episode)), F("episode_end", FVOf(Episode)),
		F("offset", Int(-100000, 100000)),
		F("selected_source", FVOf(Text(1, 64))), F("coverage_keys", Array(Text(1, 256), 0, 16)),
		F("alternatives", Array(Ref("AlternativeV1"), 0, 64)), F("conflicts", Array(Ref("ConflictV1"), 0, 64)),
		F("failure_code", Nullable(Text(1, 128))), F("resolver_version", Text(1, 128)), F("input_digest", DigestOf),
	).Check(func(r reader, m map[string]Value) error {
		shape := m["shape"].String
		multi := m["state"].String == "resolved" && m["kind"].String == "tv_episode" && (shape == "range" || shape == "list")
		if !multi {
			if fvState(m["episode_end"]) != "absent" {
				return constraint(r, "episode_end")
			}
			// Everything that is not a multi-episode file is a V1 identity.
			v1 := make(map[string]Value, len(m)-1)
			for key, value := range m {
				if key != "episode_end" {
					v1[key] = value
				}
			}
			return Validate("IdentityV1", obj(v1))
		}
		if m["failure_code"].Kind != KindNull {
			return constraint(r, "failure_code")
		}
		raw, eff, end := m["raw_episode"], m["episode"], m["episode_end"]
		if fvState(raw) != "known" || fvState(eff) != "known" || fvState(end) != "known" {
			return constraint(r, "episode")
		}
		// Only a file's own name holds several episodes; a release title
		// names the batch and never resolves to a range.
		if source := m["selected_source"]; fvState(source) != "known" || source.Object["value"].String != "file_parser" {
			return constraint(r, "selected_source")
		}
		first, errFirst := strconv.Atoi(eff.Object["value"].String)
		last, errLast := strconv.Atoi(end.Object["value"].String)
		if errFirst != nil || errLast != nil || last <= first {
			return constraint(r, "episode_end")
		}
		coverage := m["coverage_keys"].Array
		if len(coverage) != last-first+1 || len(coverage) < 2 || len(coverage) > episodeidentity.MaxEpisodesPerFile {
			return constraint(r, "coverage_keys")
		}
		var firstKey episodeidentity.TVCoverageKey
		for index, key := range coverage {
			parsed, ok := episodeidentity.DecomposeCoverageKey(key.String)
			if !ok || parsed.Episode != strconv.Itoa(first+index) {
				return constraint(r, "coverage_keys")
			}
			if index == 0 {
				firstKey = parsed
				continue
			}
			if (parsed.Season == nil) != (firstKey.Season == nil) || (parsed.Season != nil && *parsed.Season != *firstKey.Season) {
				return constraint(r, "coverage_keys")
			}
		}
		return nil
	}))
}

// IdentitySchema names the registered schema a frozen identity is checked
// against: IdentityV2 for a multi-episode file, IdentityV1 otherwise.
func IdentitySchema(identity episodeidentity.EpisodeIdentity) string {
	if len(identity.CoverageKeys) > 1 || identity.EpisodeEnd != nil {
		return "IdentityV2"
	}
	return "IdentityV1"
}

// IdentityValue encodes a resolver identity as the registry's identity object
// (IdentityV1, plus episode_end for IdentityV2). An identity frozen without a
// shape (the canonical movie identity) reads as shape unknown; the resolver
// never records alternatives or conflicts.
func IdentityValue(identity episodeidentity.EpisodeIdentity) Value {
	episodeFV := func(value *episodeidentity.EpisodeNumber) Value {
		if value == nil {
			return Absent[string]().value(str)
		}
		return Known(string(*value)).value(str)
	}
	source := Absent[string]().value(str)
	if identity.SelectedSource != nil {
		source = Known(string(*identity.SelectedSource)).value(str)
	}
	keys := []Value{}
	if identity.State == episodeidentity.StateResolved {
		for _, key := range identity.Keys() {
			keys = append(keys, str(key))
		}
	}
	failure := null()
	if identity.FailureCode != nil {
		failure = str(*identity.FailureCode)
	}
	shape := identity.Shape
	if strings.TrimSpace(shape) == "" {
		shape = episodeidentity.ShapeUnknown
	}
	members := map[string]Value{
		"state": str(identity.State), "kind": str(identity.Kind), "shape": str(shape),
		"raw_episode": episodeFV(identity.RawEpisode), "episode": episodeFV(identity.Episode),
		"offset": i64(int64(identity.Offset)), "selected_source": source, "coverage_keys": arr(keys),
		"alternatives": arr(nil), "conflicts": arr(nil), "failure_code": failure,
		"resolver_version": str(identity.ResolverVersion), "input_digest": str("sha256:" + identity.InputDigest),
	}
	if IdentitySchema(identity) == "IdentityV2" {
		members["episode_end"] = episodeFV(identity.EpisodeEnd)
	}
	return obj(members)
}

// ValidateResolvedIdentity checks a resolved identity against its registered
// schema before it is frozen: one key ending in its episode for a single
// episode or a movie, 2..MaxEpisodesPerFile consecutive keys for a
// multi-episode file. A non-resolved identity carries no coverage and is not
// checked here.
func ValidateResolvedIdentity(identity episodeidentity.EpisodeIdentity) error {
	if identity.State != episodeidentity.StateResolved {
		return nil
	}
	return Validate(IdentitySchema(identity), IdentityValue(identity))
}
