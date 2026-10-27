package domain

import "strings"

// Registry of the persisted acquisition input (AcquisitionInputV1) and the types
// it is built from. Each entry transcribes one row of the DTO / persistence /
// budget contracts; the design vectors validate the transcription from the other
// side. A type the contracts do not list is not here, and a key they do not name is
// refused.
//
// The registry has one schema per concept: the acceptance writes exactly this
// shape with schema_version 1, and there are no older sibling versions to read,
// because the baseline is reset before the first release (PRUNE-046 S4). A shape
// change is a change here, in the vectors and in the `AcquisitionInputV1` digest
// domain version, never a new V<n> type kept alive beside this one.

func fvState(v Value) string { return v.Object["state"].String }

func peekString(v Value, key string) (string, bool) {
	if v.Kind != KindObject {
		return "", false
	}
	m, ok := v.Object[key]
	if !ok || m.Kind != KindString {
		return "", false
	}
	return m.String, true
}

func constraint(r reader, key string) error { return r.at(key).fail(CodeConstraintViolated) }

func init() {
	// --- shared small types ---------------------------------------------------
	register("RefVersion", Custom(func(r reader, v Value) error { _, err := r.refVersion(v); return err }))
	register("MetadataV1", Obj(
		F("work_ref", IDType), F("kind", Enum("tv", "movie")), F("season", Int(0, maxI64)), F("version", Int(1, maxI64)),
	))
	register("MappingV1", Obj(F("remote_root", Path), F("application_root", Path), F("semantics_version", Text(1, 128))))
	register("NamespaceV1", Obj(
		F("endpoint", Custom(func(r reader, v Value) error {
			if v.Kind != KindString || len(v.String) == 0 || len(v.String) > 2048 || containsControl(v.String) {
				return r.fail(CodeTypeInvalid)
			}
			// A scheme://host[:port][/base] without credentials, query or fragment.
			if !strings.Contains(v.String, "://") || strings.ContainsAny(v.String, "@?#") {
				return r.fail(CodeTypeInvalid)
			}
			return nil
		})),
		F("instance_fingerprint", FVOf(DigestOf)),
		F("adapter_version", Text(1, 128)),
		F("path_mapping", Ref("MappingV1")),
	))
	register("ProviderBindingV1", Obj(
		F("connection", Ref("RefVersion")), F("namespace", Ref("NamespaceV1")), F("credential_ref", FVOf(Ref("RefVersion"))),
	))

	// --- SourceV1 ---------------------------------------------------------------
	register("SourceV1", Obj(
		F("kind", Enum("subscription_source", "subscription_direct", "one_shot_snapshot", "one_shot_seed", "automatic_source")),
		F("release", FVOf(Ref("RefVersion"))),
		F("stable_item_key", FVOf(Text(1, 512))),
		F("title", FVOf(Title)),
		F("published_at", FVOf(Time)),
		F("provider", FVOf(Text(1, 256))),
		F("group", FVOf(Display)),
		F("payload_digest", DigestOf),
		F("manifest_digest", DigestOf),
		F("infohash_v1", FVOf(HashV1)),
		F("infohash_v2", FVOf(HashV2)),
		F("locator_display", FVOf(Display)),
		F("locator_digest", FVOf(DigestOf)),
		F("download_provider", FVOf(Ref("ProviderBindingV1"))),
		// Torrent totals frozen from the accept-time manifest: templates read them
		// as torrent.size and torrent.file_count. The accept function re-derives both
		// from the manifest rows and refuses a mismatch.
		F("torrent_size", FVOf(Size)), F("torrent_file_count", FVOf(Int(1, maxI64))),
	).Check(func(r reader, m map[string]Value) error {
		if fvState(m["infohash_v1"]) != string(FVKnown) && fvState(m["infohash_v2"]) != string(FVKnown) {
			return constraint(r, "infohash_v1")
		}
		return nil
	}))

	// --- SelectionV1 / PlacementV1: hand-typed, registered for the input ------------
	register("SelectionV1", Custom(func(r reader, v Value) error { _, err := r.selection(v); return err }))
	register("PlacementV1", Custom(func(r reader, v Value) error { _, err := r.placement(v); return err }))

	registerConfig()
	registerMedia()
	registerDelivery()
	registerLimits()

	register("AcquisitionInputV1", Obj(
		F("schema_version", Int(1, 1)),
		F("source", Ref("SourceV1")),
		F("selection", Ref("SelectionV1")),
		F("config", Ref("ConfigV1")),
		F("media", Ref("MediaV1")),
		F("delivery", Ref("DeliveryV1")),
		F("limits", Ref("LimitsV1")),
		F("placement", Ref("PlacementV1")),
	).Check(func(r reader, m map[string]Value) error {
		// The naming section the delivery froze must use the Vars mode the config froze.
		configVars, _ := peekString(m["config"].Object["vars"], "mode")
		naming := m["delivery"].Object["naming"]
		if choice, _ := peekString(naming, "vars_choice"); choice != configVars {
			return r.at("delivery").at("naming").at("vars_choice").fail(CodeConstraintViolated)
		}
		return nil
	}))
}

// --- ConfigV1 --------------------------------------------------------------------

var sectionSchemas = map[string]string{
	"file_effect":   "file-effect-v1",
	"file_rules":    "file-rules-v1",
	"naming_rules":  "naming-rules-v1",
	"parser":        "parser-section-v1",
	"release_rules": "release-rules-v1",
	"subgroups":     "subgroups-v1",
}

func sectionKeys() []string {
	keys := make([]string, 0, len(sectionSchemas))
	for k := range sectionSchemas {
		keys = append(keys, k)
	}
	// Fixed ascending order: the sections are an ordered, complete set.
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func registerConfig() {
	keyword := Obj(F("keyword", Text(1, 256)), F("score", Int(-100, 100)))
	keywords := Obj(F("anibt", Nullable(Array(keyword, 0, 256))), F("mikan", Nullable(Array(keyword, 0, 256))))
	// pinned_scores is live site policy that a frozen
	// copy of the Global subgroups section may carry; the frozen decision never
	// reads it. It is optional so every value frozen before it existed keeps its
	// exact canonical form and digest.
	pinnedScore := Obj(F("group_ref", Text(1, 256)), F("score", Int(1, 100)))
	groupDefault := Obj(
		F("groups", Array(Text(1, 256), 0, 512)), F("mode", Enum("blacklist", "whitelist")), F("score", Int(1, 100)),
		F("pinned_scores", Array(pinnedScore, 0, 512)),
	).Optional("pinned_scores")
	providerDefaults := Obj(F("anibt", Nullable(groupDefault)), F("mikan", Nullable(groupDefault)))
	groupEntry := Obj(
		F("display_name", Text(1, 256)), F("effective_selected", Bool), F("group_ref", Text(1, 256)),
		F("manual_selected", Nullable(Bool)), F("provider", Enum("anibt", "mikan")), F("score", I64),
		F("selection_mode", Enum("auto", "manual")),
	).Check(func(r reader, m map[string]Value) error {
		auto := m["selection_mode"].String == "auto"
		if auto {
			if s := m["score"].Int; s < 1 || s > 100 {
				return constraint(r, "score")
			}
			if m["manual_selected"].Kind != KindNull {
				return constraint(r, "manual_selected")
			}
		} else if m["manual_selected"].Kind == KindNull {
			return r.at("manual_selected").fail(CodeNullNotAllowed)
		}
		return nil
	})
	subgroups := Obj(
		F("entries", Array(groupEntry, 0, 4096)), F("max_rss", Int(0, 100)),
		F("name_keywords", Nullable(keywords)), F("provider_defaults", Nullable(providerDefaults)),
	)
	patterns := Array(Text(0, 512), 0, 64)
	// parser-section-v1 carries only episode_patterns: the one offset lives on the
	// RSS link (or the one-shot input) and is frozen in media.identity.offset.
	parserSection := Obj(F("episode_patterns", patterns))
	regexSection := Obj(F("must_contain", Text(0, 4096)), F("not_in", Text(0, 4096)))
	fileEffect := Obj(F("collision_policy", Enum("fail", "versioned", "overwrite")), F("operation", Enum("copy", "move", "hardlink")))
	register("SubgroupsSectionV1", subgroups)
	register("ParserSectionV1", parserSection)
	register("RegexSectionV1", regexSection)
	register("FileEffectSectionV1", fileEffect)
	register("VariantV1", Obj(F("directory_template", Text(0, 8192)), F("video_filename_template", Text(0, 8192))))
	register("NamingSectionV1", Obj(
		F("language", Enum("js-naming-1")), F("movie", Ref("VariantV1")), F("unknown", Ref("VariantV1")), F("tv", Ref("VariantV1")),
		F("preset", Nullable(Text(0, 256))), F("subtitle_suffix_template", Text(0, 8192)),
	))
	valueSchema := map[string]string{
		"file_effect": "FileEffectSectionV1", "file_rules": "RegexSectionV1", "naming_rules": "NamingSectionV1",
		"parser": "ParserSectionV1", "release_rules": "RegexSectionV1", "subgroups": "SubgroupsSectionV1",
	}
	register("SectionEntryV1", Custom(func(r reader, v Value) error {
		m, err := r.closed(v, "section_key", "schema_version", "config_version", "value")
		if err != nil {
			return err
		}
		key := m["section_key"]
		if key.Kind != KindString {
			return r.at("section_key").fail(CodeTypeInvalid)
		}
		want, ok := sectionSchemas[key.String]
		if !ok {
			return r.at("section_key").fail(CodeUnionBranchInvalid)
		}
		if sv := m["schema_version"]; sv.Kind != KindString || sv.String != want {
			return r.at("schema_version").fail(CodeConstraintViolated)
		}
		// config_version 0 is a complete candidate override; >0 a Global/default source.
		if err := Int(0, maxI64).validate(r.at("config_version"), m["config_version"]); err != nil {
			return err
		}
		return Ref(valueSchema[key.String]).validate(r.at("value"), m["value"])
	}))
	register("ConfigIdentityV1", Union("kind", map[string]*objectSchema{
		"saved_revision": Obj(F("candidate_ref", IDType), F("revision", Int(1, maxI64)), F("managed_anime_ref", FVOf(IDType))),
		"submitted":      Obj(F("intent_digest", DigestOf), F("managed_anime_ref", FVOf(IDType))),
		"one_shot":       Obj(F("intent_digest", DigestOf)),
	}))
	register("ParserProfileV1", Obj(F("episode_patterns", patterns)))
	register("ParserSelectionV1", Obj(
		F("kind", registeredParserKind), F("config", Nullable(Ref("ParserProfileV1"))), F("config_fingerprint", DigestOf),
		F("config_version", Text(1, 128)), F("implementation_version", Text(1, 128)), F("output_contract_version", Text(1, 128)),
		F("normalizer_version", Text(1, 128)), F("resolver_version", Text(1, 128)), F("offset", Int(-100000, 100000)),
	))
	// The parser provenance has no second offset to override and no inheritance to
	// report: the offset is frozen once, in media.identity.offset and
	// config.parser.selection.offset.
	register("ParserProvenanceV1", Obj(
		F("selection", Ref("ParserSelectionV1")),
		F("source_version", FVOf(I64)),
		F("policy_version", FVOf(I64)), F("profile_version", FVOf(I64)), F("rules_owner_version", FVOf(I64)),
		F("input_digest", FVOf(DigestOf)), F("result_digest", FVOf(DigestOf)), F("media_kind", FVOf(Text(1, 64))),
		F("season", FVOf(I64)), F("season_bound", FVOf(Bool)), F("parse_status", FVOf(Text(1, 64))), F("parse_shape", FVOf(Text(1, 64))),
	))
	register("ParserEnvelopeV1", Obj(
		F("selection", FVOf(Ref("ParserSelectionV1"))), F("provenance", FVOf(Ref("ParserProvenanceV1"))),
		F("runtime_version", Text(1, 128)),
	))
	registerVars()
	register("ConfigV1", Obj(
		F("identity", Ref("ConfigIdentityV1")), F("effective_digest", DigestOf),
		F("sections", Array(Ref("SectionEntryV1"), 6, 6).Ordered(func(r reader, items []Value) error {
			for i, key := range sectionKeys() {
				if items[i].Object["section_key"].String != key {
					return r.at(itoa(i)).at("section_key").fail(CodeConstraintViolated)
				}
			}
			return nil
		})),
		F("parser", Ref("ParserEnvelopeV1")), F("vars", Ref("VarsChoiceV1")), F("rules_version", FVOf(Int(1, maxI64))),
	).Check(func(r reader, m map[string]Value) error {
		// rules_version is known exactly when the parser resolved a rule owner; an
		// unavailable one is not a missing one.
		fact := FVOf(Int(1, maxI64))
		if err := fact.validate(r.at("rules_version"), m["rules_version"]); err != nil {
			return err
		}
		if fvState(m["rules_version"]) == "unavailable" {
			return constraint(r, "rules_version")
		}
		p := m["parser"].Object["provenance"]
		rules := m["rules_version"]
		if fvState(p) != "known" {
			if fvState(p) != "absent" || fvState(rules) != "absent" {
				return r.at("rules_version").fail(CodeConstraintViolated)
			}
		} else if string(Canonical(rules)) != string(Canonical(p.Object["value"].Object["rules_owner_version"])) {
			return r.at("rules_version").fail(CodeConstraintViolated)
		}
		return nil
	}))
}

const varsRuntime = "naming-vars-v1"

func varsRuntimeVersion(v string) bool { return v == varsRuntime }

func registerVars() {
	register("VarsProgramV1", Obj(
		F("schema_version", Int(1, 1)), F("source", Text(0, 64*1024)), F("digest", DigestOf), F("version", Text(1, 256)),
		F("runtime_version", Text(1, 128)), F("timeout_ms", Int(1, 10000)), F("max_input_bytes", Int(1, 256*1024)),
		F("max_output_bytes", Int(1, 8192)), F("max_collection_len", Int(1, 256)), F("max_string_bytes", Int(1, 8192)),
	).Check(func(r reader, m map[string]Value) error {
		if !varsRuntimeVersion(m["runtime_version"].String) {
			return constraint(r, "runtime_version")
		}
		if m["version"].String != m["runtime_version"].String+"@"+m["digest"].String {
			return constraint(r, "version")
		}
		return nil
	}))
	register("VarsFileV1", Obj(F("name", Text(0, 4096)), F("path", Text(0, 4096)), F("size", Size)))
	registerVarsFacts()
	// VarsMap is the one object with dynamic keys: two levels of string -> string.
	level := Custom(func(r reader, v Value) error {
		if v.Kind != KindObject {
			return r.fail(CodeTypeInvalid)
		}
		if len(v.Object) > 256 {
			return r.fail(CodeConstraintViolated)
		}
		for _, k := range sortedKeys(v.Object) {
			if k == "" || len(k) > 128 {
				return r.at(k).fail(CodeTypeInvalid)
			}
			if v.Object[k].Kind != KindString {
				return r.at(k).fail(CodeTypeInvalid)
			}
			if len(v.Object[k].String) > 8192 {
				return r.at(k).fail(CodeConstraintViolated)
			}
		}
		return nil
	})
	register("VarsMapV1", Custom(func(r reader, v Value) error {
		if v.Kind != KindObject {
			return r.fail(CodeTypeInvalid)
		}
		if len(v.Object) > 256 {
			return r.fail(CodeConstraintViolated)
		}
		for _, k := range sortedKeys(v.Object) {
			if k == "" || len(k) > 128 {
				return r.at(k).fail(CodeTypeInvalid)
			}
			if err := level.validate(r.at(k), v.Object[k]); err != nil {
				return err
			}
		}
		return nil
	}))
	disabled := Obj(F("runtime_version", Text(1, 128)))
	frozen := Obj(
		F("program", Ref("VarsProgramV1")), F("known_inputs", Ref("VarsInputV1")), F("output", Nullable(Ref("VarsMapV1"))),
		F("status", Enum("ok", "error")), F("reason_code", Nullable(ReasonOf)),
	).Check(func(r reader, m map[string]Value) error {
		if m["status"].String == "ok" {
			if m["output"].Kind == KindNull {
				return r.at("output").fail(CodeNullNotAllowed)
			}
			if m["reason_code"].Kind != KindNull {
				return constraint(r, "reason_code")
			}
		} else {
			if m["output"].Kind != KindNull {
				return constraint(r, "output")
			}
			if m["reason_code"].Kind == KindNull {
				return r.at("reason_code").fail(CodeNullNotAllowed)
			}
		}
		return nil
	})
	union := Union("mode", map[string]*objectSchema{"disabled": disabled, "frozen": frozen})
	register("VarsChoiceV1", Custom(func(r reader, v Value) error {
		if mode, _ := peekString(v, "mode"); mode == "late_file" {
			// Late Vars (file facts observed after acceptance) is a future design.
			return r.at("mode").fail(CodeInputSchemaUnsupported)
		}
		return union.validate(r, v)
	}))
}

// --- MediaV1 ---------------------------------------------------------------------

func qualityField(value Schema) Schema {
	return Obj(
		F("state", Enum("value", "missing", "unknown", "invalid", "conflict")),
		F("value", Nullable(value)), F("reason_code", Nullable(ReasonOf)), F("raw_digest", Nullable(DigestOf)),
	).Check(func(r reader, m map[string]Value) error {
		switch m["state"].String {
		case "value":
			if m["value"].Kind == KindNull {
				return r.at("value").fail(CodeNullNotAllowed)
			}
			if m["reason_code"].Kind != KindNull {
				return constraint(r, "reason_code")
			}
		case "missing":
			if m["value"].Kind != KindNull || m["reason_code"].Kind != KindNull || m["raw_digest"].Kind != KindNull {
				return r.fail(CodeConstraintViolated)
			}
		default:
			if m["value"].Kind != KindNull {
				return constraint(r, "value")
			}
			if m["reason_code"].Kind == KindNull {
				return r.at("reason_code").fail(CodeNullNotAllowed)
			}
		}
		return nil
	})
}

func registerMedia() {
	register("AlternativeV1", Obj(
		F("source", Enum("file_parser", "release_parser", "movie")), F("value", Text(0, 512)), F("domain_reason", Text(0, 512)),
	))
	register("ConflictV1", Obj(F("source", Enum("file_parser", "release_parser", "movie")), F("value", Text(0, 512))))
	register("IdentityV1", Obj(
		F("state", Enum("resolved", "unresolved", "invalid", "unsupported", "conflict")),
		F("kind", Enum("tv_episode", "movie")),
		F("shape", Enum("single", "range", "list", "season_only", "special", "unknown")),
		F("raw_episode", FVOf(Episode)), F("episode", FVOf(Episode)), F("offset", Int(-100000, 100000)),
		F("selected_source", FVOf(Text(1, 64))), F("coverage_keys", Array(Text(1, 256), 0, 16)),
		F("alternatives", Array(Ref("AlternativeV1"), 0, 64)), F("conflicts", Array(Ref("ConflictV1"), 0, 64)),
		F("failure_code", Nullable(Text(1, 128))), F("resolver_version", Text(1, 128)), F("input_digest", DigestOf),
	).Check(func(r reader, m map[string]Value) error {
		coverage := m["coverage_keys"].Array
		if m["state"].String != "resolved" {
			if len(coverage) != 0 {
				return constraint(r, "coverage_keys")
			}
			if m["failure_code"].Kind == KindNull {
				return r.at("failure_code").fail(CodeNullNotAllowed)
			}
			return nil
		}
		if m["failure_code"].Kind != KindNull {
			return constraint(r, "failure_code")
		}
		raw, eff := m["raw_episode"], m["episode"]
		source := m["selected_source"]
		if m["kind"].String == "movie" {
			if fvState(raw) != "known" || fvState(eff) != "known" || raw.Object["value"].String != "0" || eff.Object["value"].String != "0" {
				return constraint(r, "episode")
			}
			if m["offset"].Int != 0 {
				return constraint(r, "offset")
			}
			if fvState(source) != "known" || source.Object["value"].String != "movie" {
				return constraint(r, "selected_source")
			}
			if len(coverage) != 1 || coverage[0].String != "movie:0" {
				return constraint(r, "coverage_keys")
			}
			return nil
		}
		if m["shape"].String != "single" {
			return constraint(r, "shape")
		}
		if fvState(raw) != "known" || fvState(eff) != "known" {
			return constraint(r, "episode")
		}
		if fvState(source) != "known" || (source.Object["value"].String != "file_parser" && source.Object["value"].String != "release_parser") {
			return constraint(r, "selected_source")
		}
		if len(coverage) != 1 || !strings.HasPrefix(coverage[0].String, "tv:s") || !strings.HasSuffix(coverage[0].String, ":e"+eff.Object["value"].String) {
			return constraint(r, "coverage_keys")
		}
		return nil
	}))
	register("QualityV1", Obj(
		F("group", qualityField(Display)),
		F("languages", qualityField(Array(Text(1, 64), 0, 64).Ordered(SortedUnique))),
		F("resolution", qualityField(Display)), F("source", qualityField(Display)), F("revision", qualityField(Display)),
		F("size", qualityField(Size)), F("mapping_version", FVOf(Text(1, 128))),
		F("parser_provenance", FVOf(Ref("ParserProvenanceV1"))), F("profile", FVOf(Ref("RefVersion"))), F("rules_version", FVOf(I64)),
	))
	title := Nullable(Text(1, 512))
	// year is the provider's first air/release year, frozen beside the titles
	// (null when the provider gave none; an unobserved work has no titles
	// object at all).
	year := Nullable(I64)
	register("BGMTitlesV1", Obj(F("title", title), F("title_cn", title), F("year", year)))
	// overview is the TMDB work summary recorded beside the titles. Optional
	// so inputs frozen before it existed keep their exact canonical form and
	// digest.
	register("TMDBTitlesV1", Obj(F("title_cn", title), F("original_title", title), F("overview", Nullable(Text(1, 2048))), F("year", year)).Optional("overview"))
	register("MediaV1", Obj(
		F("title", FVOf(Display)), F("title_override", FVOf(Display)), F("provider_title", FVOf(Display)),
		F("bgm", FVOf(Ref("RefVersion"))),
		F("tmdb", FVOf(Ref("MetadataV1"))), F("group", FVOf(Display)),
		F("identity", FVOf(Ref("IdentityV1"))), F("quality", FVOf(Ref("QualityV1"))),
		// The bound works' recorded native titles and provider year, frozen at
		// accept with the other binding facts: media.bgm_titles
		// {title, title_cn, year} and media.tmdb_titles
		// {title_cn, original_title, year, overview?}. Each member is the
		// provider's own value (null when the provider gave none); title_cn is
		// the title the TMDB request language returned (zh-CN); overview is
		// the TMDB work summary (null when never observed). The field is
		// absent when the work is unbound or was never observed.
		F("bgm_titles", FVOf(Ref("BGMTitlesV1"))), F("tmdb_titles", FVOf(Ref("TMDBTitlesV1"))),
	))
}

// --- DeliveryV1 -------------------------------------------------------------------

func registerDelivery() {
	// A known configuration from the frozen sections; a missing filesystem fact is
	// reported as unavailable with its reason, never invented as a path.
	routeFact := func(schema Schema, reason string) Schema {
		base := FVOf(schema)
		return Custom(func(r reader, v Value) error {
			if err := base.validate(r, v); err != nil {
				return err
			}
			state := fvState(v)
			if state != "known" && !(state == "unavailable" && v.Object["reason_code"].String == reason) {
				return r.fail(CodeConstraintViolated)
			}
			return nil
		})
	}
	register("UnclassifiedRouteV1", Obj(F("root", routeFact(Path, "anime_root_unavailable")), F("path_mapping", routeFact(Ref("MappingV1"), "path_mapping_unconfigured"))))
	register("RouteV1", Union("mode", map[string]*objectSchema{
		"classified":   Obj(F("root", routeFact(Path, "anime_root_unavailable")), F("path_mapping", routeFact(Ref("MappingV1"), "path_mapping_unconfigured")), F("unclassified_fallback", FVOf(Ref("UnclassifiedRouteV1")))),
		"unclassified": Obj(F("root", routeFact(Path, "anime_root_unavailable")), F("path_mapping", routeFact(Ref("MappingV1"), "path_mapping_unconfigured"))),
	}))
	register("NamingV1", Obj(
		F("schema_version", Enum("naming-rules-v1")), F("templates", Ref("NamingSectionV1")),
		F("vars_choice", Enum("disabled", "frozen", "late_file")), F("runtime_version", Text(1, 128)),
	))
	register("SourceCleanupV1", Union("mode", map[string]*objectSchema{"none": Obj(), "remove_source_after_publish": Obj()}))
	registerObjects()
	register("ComparisonV1", Obj(
		F("policy", Ref("RefVersion")), F("candidate_quality", Ref("QualityV1")), F("existing_quality", Ref("QualityV1")),
		F("compared_dimensions", Array(Enum("group", "languages", "resolution", "revision", "size", "source"), 0, 6).Ordered(SortedUnique)),
		F("verdict", Enum("replace")), F("evaluated_at", Time),
	))
	register("ReplacementV1", Obj(
		F("placement", Ref("RefVersion")), F("expected_object", Ref("PathObjectV1")), F("comparison", Ref("ComparisonV1")),
	))
	const maxReplacements = 64
	replacements := Array(Ref("ReplacementV1"), 0, maxReplacements).Ordered(func(r reader, items []Value) error {
		for i := 1; i < len(items); i++ {
			if replacementPath(items[i]) <= replacementPath(items[i-1]) {
				return r.at(itoa(i)).fail(CodeConstraintViolated)
			}
		}
		return nil
	})
	// A batch release replaces per file, so one acquisition may overwrite several
	// approved incumbents. `replacements` is 0..64 approvals ordered by, and unique
	// in, the canonical path of the object each one conditions on. Approvals always
	// mean intent replace. An approved replacement is atomic whatever the global
	// collision policy: the approved target is overwritten, while `collision`
	// stays the global policy for every other target (block and version
	// included). An overwrite delivery without approvals (intent ensure_present)
	// replaces only what the file plan finds no approval for.
	register("DeliveryV1", Obj(
		F("operation", Enum("move", "copy", "hardlink", "noop")), F("intent", Enum("ensure_present", "replace", "explicit")),
		F("collision", Enum("block", "version", "overwrite")), F("route", Ref("RouteV1")), F("naming", Ref("NamingV1")),
		F("source_cleanup", Ref("SourceCleanupV1")), F("replacements", replacements),
	).Check(func(r reader, m map[string]Value) error {
		cleanup, _ := peekString(m["source_cleanup"], "mode")
		if m["operation"].String == "move" && cleanup != "remove_source_after_publish" {
			return constraint(r, "source_cleanup")
		}
		if m["operation"].String != "move" && cleanup != "none" {
			return constraint(r, "source_cleanup")
		}
		approved := len(m["replacements"].Array) > 0
		if (m["intent"].String == "replace") != approved {
			return constraint(r, "intent")
		}
		return nil
	}))
}

func replacementPath(v Value) string {
	return v.Object["expected_object"].Object["location"].Object["canonical_path"].String
}

// Path-level object evidence: a regular file is identified by its
// location and size. Device/inode are recorded for diagnostics and for the
// same-invocation hardlink check; they never carry across runs. There is no
// content proof: the downloader's piece verification is the content proof.
func registerObjects() {
	register("PathIdentityV1", Obj(F("kind", Enum("local_fs")), F("device", U64Text), F("inode", positiveU64Text)))
	register("LocationV1", Obj(F("namespace_digest", DigestOf), F("root", Path), F("relative_path", Path), F("canonical_path", Path)))
	register("PathObjectV1", Obj(
		F("location", Ref("LocationV1")), F("file_type", Enum("regular")), F("size", Size),
		F("identity", Ref("PathIdentityV1")), F("observed_at", Time),
	))
	register("PathSatisfactionV1", Obj(
		F("schema_version", Int(2, 2)), F("basis", Enum("path_size")),
		F("acquisition_id", IDType), F("plan_id", IDType), F("artifact_id", IDType), F("artifact_version", Int(1, maxI64)),
		F("file_ref", IDType), F("input_digest", DigestOf), F("spec_digest", DigestOf),
		F("expected_size", Size), F("observed_object", Ref("PathObjectV1")),
	))
}

// --- LimitsV1 ---------------------------------------------------------------------

const (
	day  = int64(86400)
	week = 7 * day
)

func stageBudget() Schema {
	return Obj(F("max_attempts", Int(1, 100)), F("attempt_timeout_seconds", Int(1, day)), F("wall_seconds", Int(1, week))).Check(
		func(r reader, m map[string]Value) error {
			if m["wall_seconds"].Int < m["attempt_timeout_seconds"].Int {
				return constraint(r, "wall_seconds")
			}
			return nil
		})
}

func registerLimits() {
	register("StageBudgetV1", stageBudget())
	register("SyncBudgetV1", Obj(
		F("poll_interval_seconds", Int(10, 3600)), F("attempt_timeout_seconds", Int(1, day)),
		F("max_consecutive_errors", Int(1, 100)), F("max_total_errors", Int(1, 100)), F("max_polls", Int(1, 259201)),
	))
	register("ReconcileBudgetV1", Obj(F("wall_seconds", Int(1, week)), F("attempt_timeout_seconds", Int(1, day)), F("max_lookups", Int(1, 100))))
	register("SettlementBudgetV1", Obj(
		F("max_errors", Int(5, 5)), F("attempt_timeout_seconds", Int(1, day)), F("lease_seconds", Int(1, day)), F("heartbeat_seconds", Int(1, day)),
	).Check(func(r reader, m map[string]Value) error {
		if !(m["heartbeat_seconds"].Int < m["lease_seconds"].Int && m["lease_seconds"].Int <= m["attempt_timeout_seconds"].Int) {
			return constraint(r, "heartbeat_seconds")
		}
		return nil
	}))
	const month, quarter = 30 * 86400, 90 * 86400
	register("RetentionLimitsV1", Obj(
		F("detail_seconds", Int(month, quarter)), F("receipt_after_accept_seconds", Int(month, quarter)),
		F("receipt_after_terminal_seconds", Int(month, quarter)),
	))
	register("LimitsV1", Obj(
		F("policy_version", Int(1, 1)), F("prepare_timeout_seconds", Int(1, day)), F("download_wait_seconds", Int(1, 30*day)),
		F("stall_seconds", Int(1, 30*day)), F("submit", Ref("StageBudgetV1")), F("sync", Ref("SyncBudgetV1")),
		F("inspect", Ref("StageBudgetV1")), F("plan", Ref("StageBudgetV1")), F("file", Ref("StageBudgetV1")),
		F("reconcile", Ref("ReconcileBudgetV1")), F("settlement", Ref("SettlementBudgetV1")), F("handoff", Ref("StageBudgetV1")),
		F("retention", Ref("RetentionLimitsV1")),
	).Check(func(r reader, m map[string]Value) error {
		sync := m["sync"].Object
		if sync["max_total_errors"].Int < sync["max_consecutive_errors"].Int {
			return r.at("sync").at("max_total_errors").fail(CodeConstraintViolated)
		}
		// max_polls is fixed at acceptance: ceil(download_wait / interval) + 1.
		wait, interval := m["download_wait_seconds"].Int, sync["poll_interval_seconds"].Int
		if want := (wait+interval-1)/interval + 1; sync["max_polls"].Int != want {
			return r.at("sync").at("max_polls").fail(CodeConstraintViolated)
		}
		return nil
	}))
}
