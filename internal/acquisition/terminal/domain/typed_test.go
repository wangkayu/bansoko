package domain

import (
	"errors"
	"strings"
	"testing"
)

func parse(t *testing.T, s string) Value {
	t.Helper()
	v, err := ParseStrict([]byte(s))
	if err != nil {
		t.Fatalf("ParseStrict(%s): %v", s, err)
	}
	return v
}

func codeOf(t *testing.T, err error) (Code, string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("expected a *Error, got %v", err)
	}
	return e.Code, e.Path
}

// goldenValue expands one design definition the way the vector test does.
func goldenValue(t *testing.T, definition string) (Value, goldenVector) {
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
	for _, vec := range g.Vectors {
		if vec.Definition == definition {
			return expandFixture(t, defs, defs[definition], []string{definition}), vec
		}
	}
	t.Fatalf("no vector for definition %q", definition)
	return Value{}, goldenVector{}
}

// A typed value decoded from the design vector and encoded again is the same
// bytes and the same digest: the typed layer neither loses nor invents anything.
func TestTypedValuesRoundTripTheirGoldenVectors(t *testing.T) {
	t.Run("PlacementV1", func(t *testing.T) {
		in, vec := goldenValue(t, "placement")
		p, err := DecodePlacement(in)
		if err != nil {
			t.Fatalf("DecodePlacement: %v", err)
		}
		if p.Mode != PlacementObserveForInstance || p.ManagedAnimeRef != "anime-1" || p.ExpectedPlacement.State != FVAbsent {
			t.Fatalf("decoded placement = %+v", p)
		}
		assertVector(t, p.Value(), vec)
	})
	t.Run("SelectionV1", func(t *testing.T) {
		in, vec := goldenValue(t, "selection")
		s, err := DecodeSelection(in)
		if err != nil {
			t.Fatalf("DecodeSelection: %v", err)
		}
		if s.Mode != SelectionRules || s.RulesDigest == nil || len(s.ExplicitRef) != 0 {
			t.Fatalf("decoded selection = %+v", s)
		}
		assertVector(t, s.Value(), vec)
	})
}

func assertVector(t *testing.T, v Value, vec goldenVector) {
	t.Helper()
	canonical := Canonical(v)
	if len(canonical) != vec.ExpectedBytes {
		t.Fatalf("length = %d, want %d", len(canonical), vec.ExpectedBytes)
	}
	if got := Digest(vec.Domain, canonical); got != "sha256:"+vec.ExpectedSHA256 {
		t.Fatalf("digest = %s, want sha256:%s", got, vec.ExpectedSHA256)
	}
}

// The negative vectors of the closed-object, union and null layers, with the
// first-error code and the path where it is found.
func TestGoldenNegativeVectorsOfTheTypeLayer(t *testing.T) {
	g := loadGolden(t)
	byID := map[string]goldenNegative{}
	for _, n := range g.NegativeVectors {
		byID[n.ID] = n
	}
	check := func(id string, decode func(Value) error, wantPath string) {
		t.Helper()
		neg, ok := byID[id]
		if !ok {
			t.Fatalf("negative vector %s missing", id)
		}
		err := decode(parse(t, neg.Raw))
		if err == nil {
			t.Fatalf("%s must be rejected", id)
		}
		code, path := codeOf(t, err)
		if string(code) != neg.ExpectedReason {
			t.Errorf("%s: code = %s, want %s", id, code, neg.ExpectedReason)
		}
		if path != wantPath {
			t.Errorf("%s: path = %q, want %q", id, path, wantPath)
		}
	}
	placement := func(v Value) error { _, err := DecodePlacement(v); return err }
	frozen := func(v Value) error {
		_, err := decodeFV(reader{}, v, func(r reader, v Value) (string, error) { return v.String, nil })
		return err
	}
	check("NV02", placement, "/mode")               // {} : the discriminator is missing
	check("NV03", frozen, "/extra")                 // a key beyond state/value/reason_code
	check("NV07", placement, "/expected_placement") // none carrying instance keys; the first one in key order
}

func TestFVStatesAreStrictAndDistinct(t *testing.T) {
	str := func(r reader, v Value) (string, error) {
		if v.Kind != KindString {
			return "", r.fail(CodeTypeInvalid)
		}
		return v.String, nil
	}
	for _, tc := range []struct {
		name, in string
		code     Code
		path     string
	}{
		{"known with a null value", `{"state":"known","value":null,"reason_code":null}`, CodeNullNotAllowed, "/value"},
		{"known with a reason", `{"state":"known","value":"x","reason_code":"WHY"}`, CodeConstraintViolated, "/reason_code"},
		{"absent with a value", `{"state":"absent","value":"x","reason_code":null}`, CodeConstraintViolated, ""},
		{"unavailable with no reason", `{"state":"unavailable","value":null,"reason_code":null}`, CodeNullNotAllowed, "/reason_code"},
		{"unavailable with a value", `{"state":"unavailable","value":"x","reason_code":"WHY"}`, CodeConstraintViolated, "/value"},
		{"unavailable with a free-text reason", `{"state":"unavailable","value":null,"reason_code":"because I said so"}`, CodeTypeInvalid, "/reason_code"},
		{"unknown state", `{"state":"maybe","value":null,"reason_code":null}`, CodeUnionBranchInvalid, "/state"},
		{"missing key", `{"state":"absent","value":null}`, CodeMissingKey, "/reason_code"},
		{"unknown key", `{"state":"absent","value":null,"reason_code":null,"x":1}`, CodeUnknownKey, "/x"},
		{"not an object", `[]`, CodeTypeInvalid, ""},
	} {
		_, err := decodeFV(reader{}, parse(t, tc.in), str)
		if err == nil {
			t.Errorf("%s must be rejected", tc.name)
			continue
		}
		if code, path := codeOf(t, err); code != tc.code || path != tc.path {
			t.Errorf("%s: got %s at %q, want %s at %q", tc.name, code, path, tc.code, tc.path)
		}
	}
	// A known zero, a known empty string and an absent value are three different
	// facts and hash differently, so a legitimate 0 can never read as "absent".
	known0 := FV[int64]{State: FVKnown, Value: 0}.value(i64)
	absent := Absent[int64]().value(i64)
	unavailable := Unavailable[int64]("MISSING_METADATA").value(i64)
	hashes := map[string]bool{}
	for _, v := range []Value{known0, absent, unavailable} {
		hashes[Digest("acquisition-input:v1", Canonical(v))] = true
	}
	if len(hashes) != 3 {
		t.Fatalf("known 0, absent and unavailable must hash differently, got %d distinct", len(hashes))
	}
}

func TestPrimitivesAreStrict(t *testing.T) {
	r := reader{}
	long := strings.Repeat("a", 129)
	for _, tc := range []struct {
		name string
		in   string
		ok   bool
	}{
		{"plain id", `"anime-1"`, true},
		{"empty id", `""`, false},
		{"id with surrounding space", `" a"`, false},
		{"id with a control character", `"a\u0007b"`, false},
		{"id of 128 bytes", `"` + strings.Repeat("a", 128) + `"`, true},
		{"id of 129 bytes", `"` + long + `"`, false},
		{"id that is not a string", `1`, false},
		{"multi-byte id within the byte limit", `"` + strings.Repeat("猫", 42) + `"`, true},
		{"multi-byte id over the byte limit", `"` + strings.Repeat("猫", 43) + `"`, false},
	} {
		_, err := r.id(parse(t, tc.in))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%t", tc.name, err, tc.ok)
		}
	}
	digest := "sha256:" + strings.Repeat("0", 64)
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{`"` + digest + `"`, true},
		{`"` + strings.ToUpper(digest) + `"`, false},
		{`"sha256:` + strings.Repeat("A", 64) + `"`, false},
		{`"sha256:` + strings.Repeat("0", 63) + `"`, false},
		{`"md5:` + strings.Repeat("0", 64) + `"`, false},
		{`null`, false},
	} {
		_, err := r.digest(parse(t, tc.in))
		if (err == nil) != tc.ok {
			t.Errorf("digest %s: err = %v, want ok=%t", tc.in, err, tc.ok)
		}
	}
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{`{"ref":"r","version":1}`, true},
		{`{"ref":"r","version":0}`, false},
		{`{"ref":"r","version":-3}`, false},
		{`{"ref":"r"}`, false},
		{`{"ref":"r","version":1,"x":1}`, false},
	} {
		_, err := r.refVersion(parse(t, tc.in))
		if (err == nil) != tc.ok {
			t.Errorf("RefVersion %s: err = %v, want ok=%t", tc.in, err, tc.ok)
		}
	}
}

func TestSelectionRulesAreEnforced(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, in string
		code     Code
		path     string
	}{
		{"rules without a digest", `{"mode":"rules","rules_digest":null,"explicit_refs":[]}`, CodeNullNotAllowed, "/rules_digest"},
		{"rules carrying refs", `{"mode":"rules","rules_digest":"` + digest + `","explicit_refs":["a"]}`, CodeConstraintViolated, "/explicit_refs"},
		{"explicit with no refs", `{"mode":"explicit_refs","rules_digest":null,"explicit_refs":[]}`, CodeConstraintViolated, "/explicit_refs"},
		{"explicit carrying a digest", `{"mode":"explicit_refs","rules_digest":"` + digest + `","explicit_refs":["a"]}`, CodeConstraintViolated, "/rules_digest"},
		{"explicit refs unsorted", `{"mode":"explicit_refs","rules_digest":null,"explicit_refs":["b","a"]}`, CodeConstraintViolated, "/explicit_refs"},
		{"explicit refs duplicated", `{"mode":"explicit_refs","rules_digest":null,"explicit_refs":["a","a"]}`, CodeConstraintViolated, "/explicit_refs"},
		{"unknown mode", `{"mode":"latest","rules_digest":null,"explicit_refs":[]}`, CodeUnionBranchInvalid, "/mode"},
		{"a ref that is not an ID", `{"mode":"explicit_refs","rules_digest":null,"explicit_refs":[""]}`, CodeTypeInvalid, "/explicit_refs/0"},
	} {
		_, err := DecodeSelection(parse(t, tc.in))
		if err == nil {
			t.Errorf("%s must be rejected", tc.name)
			continue
		}
		if code, path := codeOf(t, err); code != tc.code || path != tc.path {
			t.Errorf("%s: got %s at %q, want %s at %q", tc.name, code, path, tc.code, tc.path)
		}
	}
	ok := parse(t, `{"mode":"explicit_refs","rules_digest":null,"explicit_refs":["a","b"]}`)
	s, err := DecodeSelection(ok)
	if err != nil || len(s.ExplicitRef) != 2 {
		t.Fatalf("valid explicit selection: %+v %v", s, err)
	}
	if string(Canonical(s.Value())) != string(Canonical(ok)) {
		t.Fatal("a valid selection must re-encode to the same canonical bytes")
	}
}

// The first error follows the documented order: closed keys before the type of a
// member, so a value is never judged before its object is known to be well-formed.
func TestErrorOrderIsKeysThenBranchThenType(t *testing.T) {
	_, err := DecodePlacement(parse(t, `{"mode":"observe_for_instance","managed_anime_ref":7,"expected_placement":{},"extra":1}`))
	if code, path := codeOf(t, err); code != CodeUnknownKey || path != "/extra" {
		t.Fatalf("an unknown key is reported before a wrong member type, got %s at %q", code, path)
	}
	_, err = DecodePlacement(parse(t, `{"mode":"observe_for_instance","managed_anime_ref":7}`))
	if code, path := codeOf(t, err); code != CodeMissingKey || path != "/expected_placement" {
		t.Fatalf("a missing key is reported before a wrong member type, got %s at %q", code, path)
	}
}

func TestPlacementNoneRefusesInstanceKeysInsteadOfIgnoringThem(t *testing.T) {
	if _, err := DecodePlacement(parse(t, `{"mode":"none"}`)); err != nil {
		t.Fatalf("a plain none placement is valid: %v", err)
	}
	_, err := DecodePlacement(parse(t, `{"mode":"none","managed_anime_ref":"a"}`))
	if code, _ := codeOf(t, err); code != CodeUnionBranchInvalid {
		t.Fatalf("none with an instance key must be UNION_BRANCH_INVALID, got %v", err)
	}
	_, err = DecodePlacement(parse(t, `{"mode":"none","zzz":1}`))
	if code, _ := codeOf(t, err); code != CodeUnknownKey {
		t.Fatalf("a key that belongs to no branch is UNKNOWN_KEY, got %v", err)
	}
}

// The persisted time form: RFC 3339 UTC, microsecond precision at most, no
// superfluous trailing zeros. Anything else is refused rather than normalised.
func TestTimeFormat(t *testing.T) {
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"2026-10-03T01:02:03Z", true},
		{"2026-10-03T01:02:03.1Z", true},
		{"2026-10-03T01:02:03.123456Z", true},
		{"2026-10-03T01:02:03.120000Z", false},  // trailing zeros
		{"2026-10-03T01:02:03.0Z", false},       // a zero fraction is not written
		{"2026-10-03T01:02:03.1234567Z", false}, // finer than a microsecond
		{"2026-10-03T01:02:03+00:00", false},
		{"2026-10-03T01:02:03+08:00", false},
		{"2026-10-03t01:02:03z", false},
		{"2026-10-03 01:02:03Z", false},
		{"2026-13-03T01:02:03Z", false},
		{"2026-10-03", false},
		{"", false},
	} {
		err := Time.validate(reader{}, Value{Kind: KindString, String: tc.in})
		if (err == nil) != tc.ok {
			t.Errorf("Time(%q): err = %v, want ok=%t", tc.in, err, tc.ok)
		}
	}
	if err := Time.validate(reader{}, Value{Kind: KindInteger, Int: 1}); err == nil {
		t.Error("a time must be a string")
	}
}
