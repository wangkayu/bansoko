package domain

import (
	"sort"
	"strings"

	"bansoko/internal/postprocessing/naming"
)

// Registry of the FileFact (the minimal, self-sufficient fact about one delivered
// file that a later management step may rely on) and the effect closure.

// replayKeys are the catalog fact names a naming render reads. The dots and
// capitals are part of the names, not nested paths. The list is the naming
// package's single source of truth: the render snapshot writer, this registry
// and the database's source-relation validator must enumerate the same names.
var replayKeys = naming.ReplayFactPaths()

// closureStepOrder is the declared order of the steps a closure may cover.
var closureStepOrder = []string{
	"stage_temp", "reserve_version", "publish_target", "source_cleanup", "temp_cleanup", "qb_submit", "quarantine", "purge",
}

// pointer escapes one JSON Pointer segment.
func pointer(segment string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(segment)
}

func init() {
	fields := make([]field, 0, len(replayKeys))
	for _, k := range replayKeys {
		fields = append(fields, F(k, FVOf(Display)))
	}
	register("ReplayV1", Obj(fields...))
	register("BindingV1", Obj(
		F("media_kind", Enum("tv", "movie")), F("season", Int(0, maxI64)), F("season_bound", Bool),
		F("bgm_work_id", FVOf(IDType)), F("tmdb_work_id", FVOf(IDType)), F("effective_title", FVOf(Display)), F("extension", FVOf(Text(0, 64))),
	))
	register("VarsResultV1", Obj(
		F("program_digest", DigestOf), F("input_digest", DigestOf), F("output", Ref("VarsMapV1")),
		F("runtime_version", Text(1, 128)), F("evaluated_at", Time),
	))
	register("LocatorFactV1", Obj(
		F("final_object", FVOf(Ref("PathObjectV1"))), F("source_cleanup", Enum("not_required", "completed", "failed", "uncertain")),
	))
	register("ContentSourceV1", Obj(
		F("payload_digest", FVOf(DigestOf)), F("manifest_digest", FVOf(DigestOf)), F("infohash_v1", FVOf(HashV1)), F("infohash_v2", FVOf(HashV2)),
		F("file_ref", FVOf(IDType)), F("original_name", FVOf(Display)), F("relative_path", FVOf(Display)),
		F("media_kind", FVOf(Enum("video", "subtitle", "other"))), F("title", FVOf(Title)), F("group", FVOf(Display)),
		F("provider", FVOf(Text(1, 256))), F("locator_display", FVOf(Display)), F("release", FVOf(Ref("RefVersion"))), F("source", FVOf(Ref("RefVersion"))),
	))
	register("IdentityCoverageV1", Obj(
		F("identity", FVOf(Ref("IdentityV1"))), F("coverage_schema", Enum("episode-identity-v1", "episode-identity-v2")),
		F("coverage_keys", Array(Text(1, 256), 0, 16)),
	).Check(func(r reader, m map[string]Value) error {
		identity := m["identity"]
		want := []Value{}
		if fvState(identity) == string(FVKnown) {
			want = identity.Object["value"].Object["coverage_keys"].Array
		}
		got := m["coverage_keys"].Array
		if len(got) != len(want) {
			return constraint(r, "coverage_keys")
		}
		for i := range got {
			if got[i].String != want[i].String {
				return constraint(r, "coverage_keys")
			}
		}
		return nil
	}))
	register("RenderInputsV1", Obj(
		F("replay", Ref("ReplayV1")), F("binding", FVOf(Ref("BindingV1"))), F("vars", FVOf(Ref("VarsResultV1"))),
		F("naming_schema", FVOf(Text(1, 128))), F("naming_digest", FVOf(DigestOf)), F("render_program_version", FVOf(Text(1, 128))),
	))
	register("SourceOffsetV1", Obj(
		F("source", FVOf(Ref("RefVersion"))), F("override", FVOf(I64)), F("used_offset", FVOf(I64)), F("media_kind", FVOf(Text(1, 64))),
		F("season", FVOf(I64)), F("season_bound", FVOf(Bool)), F("video_count", FVOf(Int(0, maxI64))),
	))
	register("SelectionWitnessV1", Obj(
		// relation is a fixed allowlist of owner relations, never dynamic SQL; the
		// registry bounds it and the writer matches it against the real allowlist.
		F("relation", Text(1, 128)), F("row_ref", IDType), F("version", FVOf(I64)), F("digest", FVOf(DigestOf)),
		F("observed_at", Time),
		// There is no upgrade observation: no value is ever converted from an older shape.
		F("selection", Enum("recorded_reference", "frozen_owner_input")),
	))
	register("FactProvenanceV1", Obj(
		F("mode", Enum("native")), F("acquisition_ref", FVOf(IDType)), F("operation_ref", FVOf(IDType)), F("artifact_ref", FVOf(IDType)),
		F("artifact_file_ref", FVOf(IDType)), F("media_snapshot", FVOf(Ref("RefVersion"))), F("delivery_digest", FVOf(DigestOf)),
		F("exact_selection", FVOf(Ref("SelectionWitnessV1"))),
		F("missing_paths", Array(Text(1, 512), 0, 4096).Ordered(SortedUnique)),
	))
	register("FileFactV1", Obj(
		F("schema_version", Int(1, 1)), F("locator", Ref("LocatorFactV1")), F("content_source", Ref("ContentSourceV1")),
		F("identity_coverage", Ref("IdentityCoverageV1")), F("quality", FVOf(Ref("QualityV1"))), F("render_inputs", Ref("RenderInputsV1")),
		F("source_offset", Ref("SourceOffsetV1")), F("provenance", Ref("FactProvenanceV1")),
	).Check(checkMissingPaths))
	// FileFactV2 is the single-offset fact: the source_offset group (link,
	// override, used offset) becomes episode_offset with the one offset the
	// identity resolved with. FileFactV1 stays registered for stored handoffs.
	register("EpisodeOffsetV1", Obj(
		F("offset", FVOf(I64)), F("media_kind", FVOf(Text(1, 64))),
		F("season", FVOf(I64)), F("season_bound", FVOf(Bool)), F("video_count", FVOf(Int(0, maxI64))),
	))
	register("FileFactV2", Obj(
		F("schema_version", Int(2, 2)), F("locator", Ref("LocatorFactV1")), F("content_source", Ref("ContentSourceV1")),
		F("identity_coverage", Ref("IdentityCoverageV1")), F("quality", FVOf(Ref("QualityV1"))), F("render_inputs", Ref("RenderInputsV1")),
		F("episode_offset", Ref("EpisodeOffsetV1")), F("provenance", Ref("FactProvenanceV1")),
	).Check(checkMissingPaths))

	register("ClosureV1", Union("kind", map[string]*objectSchema{
		"no_dispatch": Obj(F("permit_id", IDType), F("gate_version", Int(1, maxI64)), F("closed_at", Time)),
		"local_join": Obj(
			F("executor_session", Text(1, 256)), F("boot_nonce", IDType), F("call_nonce", IDType),
			F("covered_steps", Array(Enum(closureStepOrder...), 1, len(closureStepOrder)).Ordered(stepsInDeclaredOrder)),
			F("joined_at", Time), F("observer_version", Text(1, 128)),
		),
		"process_terminated": Obj(
			F("executor_session", Text(1, 256)), F("boot_nonce", IDType), F("process_start_identity", Text(1, 256)),
			F("termination_observed_at", Time), F("observer_version", Text(1, 128)),
		),
		"remote_final": Obj(
			F("namespace_digest", DigestOf), F("operation_ref", Text(1, 512)), F("disposition", Enum("accepted", "rejected")),
			F("response_digest", DigestOf), F("observed_at", Time), F("adapter_version", Text(1, 128)),
		),
	}))
}

// ValidateFileFact validates one FileFact against the schema its own
// schema_version names: 2 is the current single-offset fact, 1 a fact frozen
// before it (kept valid for stored handoffs). Any other version is rejected.
func ValidateFileFact(v Value) error {
	if v.Kind == KindObject {
		if sv := v.Object["schema_version"]; sv.Kind == KindInteger {
			switch sv.Int {
			case 1:
				return Validate("FileFactV1", v)
			case 2:
				return Validate("FileFactV2", v)
			}
		}
	}
	return Validate("FileFactV2", v)
}

// stepsInDeclaredOrder requires covered steps unique and in the declared order, so
// the same set of steps always has one encoding.
func stepsInDeclaredOrder(r reader, items []Value) error {
	position := make(map[string]int, len(closureStepOrder))
	for i, s := range closureStepOrder {
		position[s] = i
	}
	for i := 1; i < len(items); i++ {
		if position[items[i].String] <= position[items[i-1].String] {
			return r.fail(CodeConstraintViolated)
		}
	}
	return nil
}

// checkMissingPaths ties provenance.missing_paths to the fact it summarises. Every
// listed path must point at a frozen value that is really unavailable, and every
// frozen value that is unavailable as a direct member of one of the fact's groups
// must be listed. Deeper unavailable values (a missing handle inside an object
// identity, say) stay visible in place and are not repeated in the summary. A
// known empty string never counts as missing.
func checkMissingPaths(r reader, m map[string]Value) error {
	listed := map[string]bool{}
	for _, p := range m["provenance"].Object["missing_paths"].Array {
		listed[p.String] = true
	}
	for p := range listed {
		node, ok := resolvePointer(Value{Kind: KindObject, Object: m}, p)
		if !ok || node.Kind != KindObject || fvState(node) != string(FVUnavailable) {
			return r.at("provenance").at("missing_paths").fail(CodeConstraintViolated)
		}
	}
	groups := make([]string, 0, len(m))
	for g := range m {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		group := m[g]
		if group.Kind != KindObject {
			continue
		}
		for _, k := range sortedKeys(group.Object) {
			member := group.Object[k]
			if member.Kind == KindObject && isFVShape(member) && fvState(member) == string(FVUnavailable) {
				if !listed["/"+pointer(g)+"/"+pointer(k)] {
					return r.at("provenance").at("missing_paths").fail(CodeConstraintViolated)
				}
			}
		}
	}
	return nil
}

func isFVShape(v Value) bool {
	_, hasState := v.Object["state"]
	_, hasValue := v.Object["value"]
	_, hasReason := v.Object["reason_code"]
	return len(v.Object) == 3 && hasState && hasValue && hasReason
}

// resolvePointer follows a JSON Pointer through objects and arrays.
func resolvePointer(root Value, ptr string) (Value, bool) {
	if ptr == "" {
		return root, true
	}
	if !strings.HasPrefix(ptr, "/") {
		return Value{}, false
	}
	cur := root
	for _, raw := range strings.Split(ptr[1:], "/") {
		seg := strings.NewReplacer("~1", "/", "~0", "~").Replace(raw)
		switch cur.Kind {
		case KindObject:
			next, ok := cur.Object[seg]
			if !ok {
				return Value{}, false
			}
			cur = next
		case KindArray:
			idx := -1
			for i := range cur.Array {
				if itoa(i) == seg {
					idx = i
				}
			}
			if idx < 0 {
				return Value{}, false
			}
			cur = cur.Array[idx]
		default:
			return Value{}, false
		}
	}
	return cur, true
}
