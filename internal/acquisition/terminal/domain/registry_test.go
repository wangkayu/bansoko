package domain

import (
	"errors"
	"strings"
	"testing"
)

// Every design vector of the input groups must satisfy the registry that was
// transcribed from the contracts. The two were written separately, so a
// mismatch means one of them is wrong; this is the check that catches it.
func TestDesignVectorsSatisfyTheRegistry(t *testing.T) {
	types := map[string]string{
		"GV01": "SourceV1", "GV02": "SelectionV1", "GV03": "ConfigV1", "GV04": "MediaV1",
		"GV05": "DeliveryV1", "GV06": "LimitsV1", "GV07": "PlacementV1", "GV08": "AcquisitionInputV1",
		"GV09": "FileFactV1", "GV10": "ClosureV1",
	}
	g := loadGolden(t)
	defs := make(map[string]Value, len(g.Definitions))
	for name, raw := range g.Definitions {
		v, err := ParseStrict(raw)
		if err != nil {
			t.Fatal(err)
		}
		defs[name] = v
	}
	for _, vec := range g.Vectors {
		typ, ok := types[vec.ID]
		if !ok {
			continue
		}
		vec := vec
		t.Run(vec.ID+"_"+typ, func(t *testing.T) {
			expanded := expandFixture(t, defs, defs[vec.Definition], []string{vec.Definition})
			if err := Validate(typ, expanded); err != nil {
				t.Fatalf("%s does not satisfy %s: %v", vec.ID, typ, err)
			}
		})
	}
}

// clone deep-copies a Value so a mutation never leaks into the next case.
func clone(v Value) Value {
	switch v.Kind {
	case KindObject:
		out := Value{Kind: KindObject, Object: make(map[string]Value, len(v.Object))}
		for k, m := range v.Object {
			out.Object[k] = clone(m)
		}
		return out
	case KindArray:
		out := Value{Kind: KindArray, Array: make([]Value, len(v.Array))}
		for i, m := range v.Array {
			out.Array[i] = clone(m)
		}
		return out
	}
	return v
}

type site struct {
	path []string
	node Value
}

// walk lists every node with its path.
func walk(v Value, path []string, visit func(site)) {
	visit(site{path: path, node: v})
	switch v.Kind {
	case KindObject:
		for _, k := range sortedKeys(v.Object) {
			walk(v.Object[k], append(append([]string(nil), path...), k), visit)
		}
	case KindArray:
		for i, m := range v.Array {
			walk(m, append(append([]string(nil), path...), itoa(i)), visit)
		}
	}
}

// rebuild returns a copy of v with f applied to the node at path.
func rebuild(v Value, path []string, f func(Value) Value) Value {
	if len(path) == 0 {
		return f(clone(v))
	}
	switch v.Kind {
	case KindObject:
		out := Value{Kind: KindObject, Object: make(map[string]Value, len(v.Object))}
		for k, m := range v.Object {
			if k == path[0] {
				out.Object[k] = rebuild(m, path[1:], f)
			} else {
				out.Object[k] = clone(m)
			}
		}
		return out
	case KindArray:
		out := Value{Kind: KindArray, Array: make([]Value, len(v.Array))}
		for i, m := range v.Array {
			if itoa(i) == path[0] {
				out.Array[i] = rebuild(m, path[1:], f)
			} else {
				out.Array[i] = clone(m)
			}
		}
		return out
	}
	return v
}

func fullInput(t *testing.T) Value {
	t.Helper()
	g := loadGolden(t)
	defs := make(map[string]Value, len(g.Definitions))
	for name, raw := range g.Definitions {
		v, err := ParseStrict(raw)
		if err != nil {
			t.Fatal(err)
		}
		defs[name] = v
	}
	return expandFixture(t, defs, defs["input"], []string{"input"})
}

// The registry must be strict, not merely accepting the good vectors: on the full
// input every object loses each of its keys in turn, gains an unlisted key, and
// every value is replaced by a value of another kind. Each of those must be
// refused; if any is accepted, some type is quietly lenient.
func TestRegistryIsStrictOnTheFullInput(t *testing.T) {
	input := fullInput(t)
	if err := Validate("AcquisitionInputV1", input); err != nil {
		t.Fatalf("the unmodified input must be valid: %v", err)
	}
	var sites []site
	walk(input, nil, func(s site) { sites = append(sites, s) })

	var accepted []string
	accept := func(what string, path []string, mutated Value) {
		if Validate("AcquisitionInputV1", mutated) == nil {
			accepted = append(accepted, what+" at /"+strings.Join(path, "/"))
		}
	}
	checked := 0
	for _, s := range sites {
		if s.node.Kind == KindObject {
			for _, key := range sortedKeys(s.node.Object) {
				key := key
				accept("missing key "+key, s.path, rebuild(input, s.path, func(o Value) Value { delete(o.Object, key); return o }))
				checked++
			}
			accept("extra key", s.path, rebuild(input, s.path, func(o Value) Value { o.Object["zz_extra"] = null(); return o }))
			checked++
		}
		if len(s.path) == 0 {
			continue
		}
		// A value of another kind in place of this one.
		var other Value
		switch s.node.Kind {
		case KindString, KindBool, KindArray, KindObject:
			other = i64(7)
		case KindInteger:
			other = str("x")
		case KindNull:
			// A nullable string field legitimately takes a string, so a null is
			// replaced by an array, which no nullable field here accepts.
			other = arr([]Value{str("x")})
		}
		accept("wrong kind", s.path, rebuild(input, s.path, func(Value) Value { return other }))
		checked++
	}
	if len(accepted) > 0 {
		t.Fatalf("%d of %d mutations were accepted; the registry is lenient at:\n  %s", len(accepted), checked, strings.Join(accepted, "\n  "))
	}
	t.Logf("%d mutations of the full input, all refused", checked)
}

func with(input Value, path string, replace Value) Value {
	segs := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return rebuild(input, segs, func(Value) Value { return replace })
}

func assertRejected(t *testing.T, name string, typ string, v Value, code Code, path string) {
	t.Helper()
	err := Validate(typ, v)
	if err == nil {
		t.Errorf("%s: must be rejected", name)
		return
	}
	var e *Error
	if !errorsAs(err, &e) {
		t.Errorf("%s: not a *Error: %v", name, err)
		return
	}
	if e.Code != code || e.Path != path {
		t.Errorf("%s: got %s at %q, want %s at %q", name, e.Code, e.Path, code, path)
	}
}

func j(t *testing.T, s string) Value {
	t.Helper()
	return parse(t, s)
}

func TestCrossFieldRulesOfTheInput(t *testing.T) {
	in := fullInput(t)
	a := "sha256:" + strings.Repeat("a", 64)

	// Delivery: the cleanup must match the operation, and an overwrite needs the
	// object the acceptance conditioned on.
	assertRejected(t, "move without source cleanup", "AcquisitionInputV1", with(in, "/delivery/operation", str("move")),
		CodeConstraintViolated, "/delivery/source_cleanup")
	assertRejected(t, "copy that removes its source", "AcquisitionInputV1",
		with(in, "/delivery/source_cleanup", j(t, `{"mode":"remove_source_after_publish"}`)), CodeConstraintViolated, "/delivery/source_cleanup")
	// An overwrite without approvals replaces only unmanaged files and stays
	// ensure_present; replace needs an approval.
	if err := Validate("AcquisitionInputV1", with(in, "/delivery/collision", str("overwrite"))); err != nil {
		t.Errorf("unmanaged overwrite delivery rejected: %v", err)
	}
	assertRejected(t, "replace intent with no replacement evidence", "AcquisitionInputV1",
		with(with(in, "/delivery/collision", str("overwrite")), "/delivery/intent", str("replace")), CodeConstraintViolated, "/delivery/intent")
	assertRejected(t, "naming vars choice differing from the config's", "AcquisitionInputV1", with(in, "/delivery/naming/vars_choice", str("frozen")),
		CodeConstraintViolated, "/delivery/naming/vars_choice")

	// Source: at least one verified info-hash.
	assertRejected(t, "source with no info-hash", "AcquisitionInputV1",
		with(in, "/source/infohash_v1", j(t, `{"state":"absent","value":null,"reason_code":null}`)), CodeConstraintViolated, "/source/infohash_v1")
	assertRejected(t, "uppercase info-hash", "AcquisitionInputV1",
		with(in, "/source/infohash_v1", j(t, `{"state":"known","value":"`+strings.Repeat("A", 40)+`","reason_code":null}`)), CodeTypeInvalid, "/source/infohash_v1/value")
	assertRejected(t, "a SHA-256 where a v1 info-hash belongs", "AcquisitionInputV1",
		with(in, "/source/infohash_v1", j(t, `{"state":"known","value":"`+strings.Repeat("a", 64)+`","reason_code":null}`)), CodeTypeInvalid, "/source/infohash_v1/value")
	assertRejected(t, "published_at with a trailing fractional zero", "AcquisitionInputV1",
		with(in, "/source/published_at", j(t, `{"state":"known","value":"2026-10-03T01:02:03.120000Z","reason_code":null}`)), CodeTypeInvalid, "/source/published_at/value")
	assertRejected(t, "unknown source kind", "AcquisitionInputV1", with(in, "/source/kind", str("latest")), CodeUnionBranchInvalid, "/source/kind")
	assertRejected(t, "endpoint with credentials", "AcquisitionInputV1",
		with(in, "/source/download_provider/value/namespace/endpoint", str("https://user:pw@qb.example.invalid/")), CodeTypeInvalid,
		"/source/download_provider/value/namespace/endpoint")

	// Config: the six sections are an ordered, complete set with their schema versions.
	assertRejected(t, "sections in the wrong order", "AcquisitionInputV1",
		rebuild(in, []string{"config", "sections"}, func(v Value) Value { v.Array[0], v.Array[1] = v.Array[1], v.Array[0]; return v }),
		CodeConstraintViolated, "/config/sections/0/section_key")
	assertRejected(t, "a section with the wrong schema version", "AcquisitionInputV1",
		with(in, "/config/sections/0/schema_version", str("file-effect-v9")), CodeConstraintViolated, "/config/sections/0/schema_version")
	assertRejected(t, "a negative config_version", "AcquisitionInputV1", with(in, "/config/sections/0/config_version", i64(-1)),
		CodeConstraintViolated, "/config/sections/0/config_version")
	assertRejected(t, "rules_version 0", "AcquisitionInputV1", with(in, "/config/rules_version", j(t, `{"state":"known","value":0,"reason_code":null}`)), CodeConstraintViolated, "/config/rules_version/value")

	// Vars: late_file is a future design and is refused, not converted.
	assertRejected(t, "late_file vars", "AcquisitionInputV1", with(in, "/config/vars", j(t, `{"mode":"late_file"}`)),
		CodeInputSchemaUnsupported, "/config/vars/mode")

	// Limits: the fixed formula for max_polls, and the budget relations.
	assertRejected(t, "max_polls off the fixed formula", "AcquisitionInputV1", with(in, "/limits/sync/max_polls", i64(10082)),
		CodeConstraintViolated, "/limits/sync/max_polls")
	assertRejected(t, "total errors below consecutive", "AcquisitionInputV1", with(in, "/limits/sync/max_total_errors", i64(4)),
		CodeConstraintViolated, "/limits/sync/max_total_errors")
	assertRejected(t, "stage wall below one attempt", "AcquisitionInputV1", with(in, "/limits/submit/wall_seconds", i64(10)),
		CodeConstraintViolated, "/limits/submit/wall_seconds")
	assertRejected(t, "settlement heartbeat not below lease", "AcquisitionInputV1", with(in, "/limits/settlement/heartbeat_seconds", i64(60)),
		CodeConstraintViolated, "/limits/settlement/heartbeat_seconds")
	assertRejected(t, "settlement max_errors other than 5", "AcquisitionInputV1", with(in, "/limits/settlement/max_errors", i64(6)),
		CodeConstraintViolated, "/limits/settlement/max_errors")
	assertRejected(t, "zero attempts", "AcquisitionInputV1", with(in, "/limits/file/max_attempts", i64(0)),
		CodeConstraintViolated, "/limits/file/max_attempts")
	assertRejected(t, "retention below the approved window", "AcquisitionInputV1", with(in, "/limits/retention/detail_seconds", i64(86400)),
		CodeConstraintViolated, "/limits/retention/detail_seconds")
	assertRejected(t, "download wait beyond 30 days", "AcquisitionInputV1", with(in, "/limits/download_wait_seconds", i64(31*86400)),
		CodeConstraintViolated, "/limits/download_wait_seconds")
	assertRejected(t, "schema_version 2", "AcquisitionInputV1", with(in, "/schema_version", i64(2)), CodeConstraintViolated, "/schema_version")
	_ = a
}

// Identity: a resolved TV episode is a single, known episode with its coverage key;
// anything else carries no coverage and a failure code.
func TestIdentityRules(t *testing.T) {
	in := fullInput(t)
	base := in.Object["media"].Object["identity"] // an unavailable FV in the vector
	_ = base
	g := loadGolden(t)
	defs := map[string]Value{}
	for n, raw := range g.Definitions {
		v, _ := ParseStrict(raw)
		defs[n] = v
	}
	identity := expandFixture(t, defs, defs["identity"], []string{"identity"})
	if err := Validate("IdentityV1", identity); err != nil {
		t.Fatalf("the design identity must be valid: %v", err)
	}
	assertRejected(t, "resolved without coverage", "IdentityV1", with(identity, "/coverage_keys", arr(nil)), CodeConstraintViolated, "/coverage_keys")
	assertRejected(t, "resolved with a range shape", "IdentityV1", with(identity, "/shape", str("range")), CodeConstraintViolated, "/shape")
	assertRejected(t, "resolved with a failure code", "IdentityV1", with(identity, "/failure_code", str("X")), CodeConstraintViolated, "/failure_code")
	assertRejected(t, "coverage for another episode", "IdentityV1", with(identity, "/coverage_keys", arr([]Value{str("tv:s0:e13")})), CodeConstraintViolated, "/coverage_keys")
	assertRejected(t, "non-canonical episode", "IdentityV1",
		with(identity, "/episode", j(t, `{"state":"known","value":"0012.50","reason_code":null}`)), CodeTypeInvalid, "/episode/value")
	unresolved := with(with(with(identity, "/state", str("unresolved")), "/coverage_keys", arr(nil)), "/failure_code", str("NO_EPISODE"))
	if err := Validate("IdentityV1", unresolved); err != nil {
		t.Errorf("an unresolved identity with no coverage and a failure is valid: %v", err)
	}
	assertRejected(t, "unresolved with coverage", "IdentityV1", with(unresolved, "/coverage_keys", arr([]Value{str("tv:s0:e1")})), CodeConstraintViolated, "/coverage_keys")
	assertRejected(t, "unresolved without a failure code", "IdentityV1", with(unresolved, "/failure_code", null()), CodeNullNotAllowed, "/failure_code")
}

// The negative vector of the quality field: a "value" state with a null value.
func TestGoldenNegativeVectorQualityField(t *testing.T) {
	g := loadGolden(t)
	for _, neg := range g.NegativeVectors {
		if neg.ID != "NV08" {
			continue
		}
		field := qualityField(Display)
		err := field.validate(reader{}, parse(t, neg.Raw))
		var e *Error
		if !errorsAs(err, &e) || string(e.Code) != neg.ExpectedReason || e.Path != "/value" {
			t.Fatalf("NV08: got %v, want %s at /value", err, neg.ExpectedReason)
		}
		return
	}
	t.Fatal("NV08 missing from the fixture")
}

func TestQualityFieldStates(t *testing.T) {
	field := qualityField(Display)
	for _, tc := range []struct {
		name, in string
		ok       bool
	}{
		{"value", `{"state":"value","value":"1080p","reason_code":null,"raw_digest":null}`, true},
		{"empty string is a real value", `{"state":"value","value":"","reason_code":null,"raw_digest":null}`, true},
		{"missing", `{"state":"missing","value":null,"reason_code":null,"raw_digest":null}`, true},
		{"missing with a reason", `{"state":"missing","value":null,"reason_code":"X","raw_digest":null}`, false},
		{"unknown with a reason", `{"state":"unknown","value":null,"reason_code":"QUALITY_UNKNOWN","raw_digest":null}`, true},
		{"unknown without a reason", `{"state":"unknown","value":null,"reason_code":null,"raw_digest":null}`, false},
		{"conflict carrying a value", `{"state":"conflict","value":"x","reason_code":"QUALITY_CONFLICT","raw_digest":null}`, false},
		{"value with a reason", `{"state":"value","value":"x","reason_code":"WHY","raw_digest":null}`, false},
	} {
		err := field.validate(reader{}, parse(t, tc.in))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%t", tc.name, err, tc.ok)
		}
	}
}

func errorsAs(err error, target **Error) bool { return errors.As(err, target) }

func expandedDefinition(t *testing.T, name string) Value {
	t.Helper()
	g := loadGolden(t)
	defs := make(map[string]Value, len(g.Definitions))
	for n, raw := range g.Definitions {
		v, err := ParseStrict(raw)
		if err != nil {
			t.Fatal(err)
		}
		defs[n] = v
	}
	return expandFixture(t, defs, defs[name], []string{name})
}

// The same strictness check as for the input, on the FileFact (the largest value)
// and on the closure: no mutation of a design vector may be accepted.
func TestRegistryIsStrictOnTheFileFactAndTheClosure(t *testing.T) {
	for _, tc := range []struct{ typ, definition string }{{"FileFactV1", "file_fact"}, {"ClosureV1", "closure"}} {
		root := expandedDefinition(t, tc.definition)
		if err := Validate(tc.typ, root); err != nil {
			t.Fatalf("%s: the unmodified vector must be valid: %v", tc.typ, err)
		}
		var sites []site
		walk(root, nil, func(s site) { sites = append(sites, s) })
		var accepted []string
		count := 0
		try := func(what string, path []string, mutated Value) {
			count++
			if Validate(tc.typ, mutated) == nil {
				accepted = append(accepted, what+" at /"+strings.Join(path, "/"))
			}
		}
		for _, s := range sites {
			if s.node.Kind == KindObject {
				// The replay object has fixed keys too, so the same rule applies to it.
				for _, key := range sortedKeys(s.node.Object) {
					key := key
					try("missing key "+key, s.path, rebuild(root, s.path, func(o Value) Value { delete(o.Object, key); return o }))
				}
				try("extra key", s.path, rebuild(root, s.path, func(o Value) Value { o.Object["zz_extra"] = null(); return o }))
			}
			if len(s.path) == 0 {
				continue
			}
			var other Value
			switch s.node.Kind {
			case KindString, KindBool, KindArray, KindObject:
				other = i64(7)
			case KindInteger:
				other = str("x")
			case KindNull:
				other = arr([]Value{str("x")})
			}
			try("wrong kind", s.path, rebuild(root, s.path, func(Value) Value { return other }))
		}
		if len(accepted) > 0 {
			t.Fatalf("%s: %d of %d mutations were accepted:\n  %s", tc.typ, len(accepted), count, strings.Join(accepted, "\n  "))
		}
		t.Logf("%s: %d mutations, all refused", tc.typ, count)
	}
}

func TestFileFactCrossFieldRules(t *testing.T) {
	ff := expandedDefinition(t, "file_fact")
	// coverage_keys must be exactly the known identity's coverage.
	assertRejected(t, "coverage that differs from the identity's", "FileFactV1",
		with(ff, "/identity_coverage/coverage_keys", arr([]Value{str("tv:s0:e13")})), CodeConstraintViolated, "/identity_coverage/coverage_keys")
	// missing_paths: sound (each names a really unavailable value) ...
	assertRejected(t, "a listed path that is not unavailable", "FileFactV1",
		with(ff, "/provenance/missing_paths", arr([]Value{str("/content_source/group"), str("/content_source/title")})),
		CodeConstraintViolated, "/provenance/missing_paths")
	assertRejected(t, "a listed path that does not exist", "FileFactV1",
		with(ff, "/provenance/missing_paths", arr([]Value{str("/content_source/nope"), str("/content_source/group")})),
		CodeConstraintViolated, "/provenance/missing_paths")
	// ... and complete at the group level: an unavailable member that is not listed is refused.
	assertRejected(t, "a group-level unavailable member that is not listed", "FileFactV1",
		with(ff, "/provenance/missing_paths", arr(nil)), CodeConstraintViolated, "/provenance/missing_paths")
	assertRejected(t, "missing_paths unsorted", "FileFactV1",
		with(with(ff, "/content_source/provider", j(t, `{"state":"unavailable","value":null,"reason_code":"NO_PROVIDER"}`)), "/provenance/missing_paths",
			arr([]Value{str("/content_source/provider"), str("/content_source/group")})), CodeConstraintViolated, "/provenance/missing_paths")
	// A known empty string is a real value: it needs no listing.
	if err := Validate("FileFactV1", with(ff, "/content_source/group", j(t, `{"state":"known","value":"","reason_code":null}`))); err == nil {
		t.Error("with group now known, listing /content_source/group is unsound and must be refused")
	}
	known := with(with(ff, "/content_source/group", j(t, `{"state":"known","value":"","reason_code":null}`)), "/provenance/missing_paths", arr(nil))
	if err := Validate("FileFactV1", known); err != nil {
		t.Errorf("a known empty string needs no listing: %v", err)
	}
	// There is no upgrade observation: nothing is converted from an older shape.
	assertRejected(t, "an upgrade-observation witness", "FileFactV1",
		with(ff, "/provenance/exact_selection", j(t, `{"state":"known","value":{"relation":"acquisition_preparation_witness_v1","row_ref":"w","version":{"state":"absent","value":null,"reason_code":null},"digest":{"state":"absent","value":null,"reason_code":null},"observed_at":"2026-10-03T01:02:03Z","selection":"upgrade_observation"},"reason_code":null}`)),
		CodeUnionBranchInvalid, "/provenance/exact_selection/value/selection")
	// The provenance mode is the single value native.
	assertRejected(t, "another provenance mode", "FileFactV1", with(ff, "/provenance/mode", str("converted")), CodeUnionBranchInvalid, "/provenance/mode")
}

func TestClosureBranches(t *testing.T) {
	a := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, in string
		ok       bool
	}{
		{"no_dispatch", `{"kind":"no_dispatch","permit_id":"p","gate_version":1,"closed_at":"2026-10-03T01:02:03Z"}`, true},
		{"no_dispatch gate 0", `{"kind":"no_dispatch","permit_id":"p","gate_version":0,"closed_at":"2026-10-03T01:02:03Z"}`, false},
		{"local_join in order", `{"kind":"local_join","executor_session":"s","boot_nonce":"b","call_nonce":"c","covered_steps":["stage_temp","publish_target","temp_cleanup"],"joined_at":"2026-10-03T01:02:03Z","observer_version":"v"}`, true},
		{"local_join out of order", `{"kind":"local_join","executor_session":"s","boot_nonce":"b","call_nonce":"c","covered_steps":["publish_target","stage_temp"],"joined_at":"2026-10-03T01:02:03Z","observer_version":"v"}`, false},
		{"local_join repeating a step", `{"kind":"local_join","executor_session":"s","boot_nonce":"b","call_nonce":"c","covered_steps":["stage_temp","stage_temp"],"joined_at":"2026-10-03T01:02:03Z","observer_version":"v"}`, false},
		{"local_join covering nothing", `{"kind":"local_join","executor_session":"s","boot_nonce":"b","call_nonce":"c","covered_steps":[],"joined_at":"2026-10-03T01:02:03Z","observer_version":"v"}`, false},
		{"local_join with an unknown step", `{"kind":"local_join","executor_session":"s","boot_nonce":"b","call_nonce":"c","covered_steps":["delete_everything"],"joined_at":"2026-10-03T01:02:03Z","observer_version":"v"}`, false},
		{"process_terminated", `{"kind":"process_terminated","executor_session":"s","boot_nonce":"b","process_start_identity":"1234:99","termination_observed_at":"2026-10-03T01:02:03Z","observer_version":"v"}`, true},
		{"remote_final", `{"kind":"remote_final","namespace_digest":"` + a + `","operation_ref":"op","disposition":"accepted","response_digest":"` + a + `","observed_at":"2026-10-03T01:02:03Z","adapter_version":"v"}`, true},
		{"remote_final with another disposition", `{"kind":"remote_final","namespace_digest":"` + a + `","operation_ref":"op","disposition":"maybe","response_digest":"` + a + `","observed_at":"2026-10-03T01:02:03Z","adapter_version":"v"}`, false},
		{"unknown closure kind", `{"kind":"hope"}`, false},
		{"a local_join key on no_dispatch", `{"kind":"no_dispatch","permit_id":"p","gate_version":1,"closed_at":"2026-10-03T01:02:03Z","boot_nonce":"b"}`, false},
	} {
		err := Validate("ClosureV1", parse(t, tc.in))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%t", tc.name, err, tc.ok)
		}
	}
}
