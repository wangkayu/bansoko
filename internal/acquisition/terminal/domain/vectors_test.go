package domain

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// The design vectors (testdata/dto-golden-vectors.json) were computed in Python
// when the contracts were written. Reproducing their byte lengths and digests here
// checks this encoder against an implementation that shares none of its code
// (DV01: not only Python self-consistent).

type goldenFile struct {
	FixtureSchemaVersion int                        `json:"fixture_schema_version"`
	CanonicalVersion     string                     `json:"canonical_version"`
	Definitions          map[string]json.RawMessage `json:"definitions"`
	Vectors              []goldenVector             `json:"vectors"`
	NegativeVectors      []goldenNegative           `json:"negative_vectors"`
}

type goldenVector struct {
	ID             string          `json:"id"`
	Type           string          `json:"type"`
	Domain         string          `json:"domain"`
	Definition     string          `json:"definition"`
	ExpectedSHA256 string          `json:"expected_sha256"`
	ExpectedBytes  int             `json:"expected_bytes"`
	Raw            json.RawMessage `json:"-"`
}

type goldenNegative struct {
	ID             string `json:"id"`
	Type           string `json:"type"`
	Raw            string `json:"raw"`
	ExpectedReason string `json:"expected_reason"`
}

func loadGolden(t *testing.T) goldenFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/dto-golden-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture file itself must satisfy the strict protocol.
	if _, err := ParseStrict(raw); err != nil {
		t.Fatalf("the fixture file is not strict JSON: %v", err)
	}
	var g goldenFile
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// expandFixture replaces {"$fixture":"name"} with that definition. The reference
// exists only inside the fixture file: depth <= 16, no cycle, and nothing may be
// left unexpanded. It is not a runtime macro and the production decoder never
// sees one.
func expandFixture(t *testing.T, defs map[string]Value, v Value, stack []string) Value {
	t.Helper()
	if v.Kind == KindObject && len(v.Object) == 1 {
		if ref, ok := v.Object["$fixture"]; ok {
			if ref.Kind != KindString {
				t.Fatalf("$fixture must name a definition")
			}
			if len(stack) >= 16 {
				t.Fatalf("fixture reference depth exceeds 16: %v", stack)
			}
			for _, seen := range stack {
				if seen == ref.String {
					t.Fatalf("fixture reference cycle: %v -> %s", stack, ref.String)
				}
			}
			def, ok := defs[ref.String]
			if !ok {
				t.Fatalf("unknown fixture definition %q", ref.String)
			}
			return expandFixture(t, defs, def, append(append([]string(nil), stack...), ref.String))
		}
	}
	switch v.Kind {
	case KindObject:
		out := Value{Kind: KindObject, Object: make(map[string]Value, len(v.Object))}
		for k, member := range v.Object {
			out.Object[k] = expandFixture(t, defs, member, stack)
		}
		return out
	case KindArray:
		out := Value{Kind: KindArray, Array: make([]Value, len(v.Array))}
		for i, item := range v.Array {
			out.Array[i] = expandFixture(t, defs, item, stack)
		}
		return out
	}
	return v
}

func assertNoFixtureLeft(t *testing.T, v Value) {
	t.Helper()
	if v.Kind == KindObject {
		if _, ok := v.Object["$fixture"]; ok {
			t.Fatal("an expanded value still contains $fixture")
		}
		for _, member := range v.Object {
			assertNoFixtureLeft(t, member)
		}
	}
	if v.Kind == KindArray {
		for _, item := range v.Array {
			assertNoFixtureLeft(t, item)
		}
	}
}

func TestGoldenVectorsReproduceByteForByte(t *testing.T) {
	g := loadGolden(t)
	if g.FixtureSchemaVersion != 1 || g.CanonicalVersion != CanonicalVersion {
		t.Fatalf("fixture header = %d/%s, want 1/%s", g.FixtureSchemaVersion, g.CanonicalVersion, CanonicalVersion)
	}
	defs := make(map[string]Value, len(g.Definitions))
	for name, raw := range g.Definitions {
		v, err := ParseStrict(raw)
		if err != nil {
			t.Fatalf("definition %s: %v", name, err)
		}
		defs[name] = v
	}
	if len(g.Vectors) != 10 {
		t.Fatalf("expected the ten design vectors, got %d", len(g.Vectors))
	}
	for _, vec := range g.Vectors {
		vec := vec
		t.Run(vec.ID+"_"+vec.Type, func(t *testing.T) {
			def, ok := defs[vec.Definition]
			if !ok {
				t.Fatalf("definition %q missing", vec.Definition)
			}
			expanded := expandFixture(t, defs, def, []string{vec.Definition})
			assertNoFixtureLeft(t, expanded)
			canonical := Canonical(expanded)
			if len(canonical) != vec.ExpectedBytes {
				t.Fatalf("canonical length = %d, want %d", len(canonical), vec.ExpectedBytes)
			}
			if got := Digest(vec.Domain, canonical); got != "sha256:"+vec.ExpectedSHA256 {
				t.Fatalf("digest = %s, want sha256:%s", got, vec.ExpectedSHA256)
			}
			// What was written is also a fixed point of the strict decoder.
			again, err := ParseStrict(canonical)
			if err != nil {
				t.Fatalf("canonical bytes do not re-parse: %v", err)
			}
			if string(Canonical(again)) != string(canonical) {
				t.Fatal("canonical bytes are not stable through parse and re-encode")
			}
		})
	}
}

// The negative vectors that fail in the lexical and duplicate-key layer, which is
// all the generic decoder can see. The closed-object, union and null cases need
// the typed decoders and are covered where those types live.
func TestGoldenNegativeVectorsOfTheParseLayer(t *testing.T) {
	g := loadGolden(t)
	parseLayer := map[string]bool{
		"DUPLICATE_KEY": true, "TRAILING_TOKEN": true, "INTEGER_TOKEN_INVALID": true, "NUL_FORBIDDEN": true,
	}
	checked := 0
	for _, neg := range g.NegativeVectors {
		if !parseLayer[neg.ExpectedReason] {
			continue
		}
		checked++
		_, err := ParseStrict([]byte(neg.Raw))
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("%s (%s): %q must be rejected, got %v", neg.ID, neg.Type, neg.Raw, err)
		}
		if string(e.Code) != neg.ExpectedReason {
			t.Errorf("%s: code = %s, want %s", neg.ID, e.Code, neg.ExpectedReason)
		}
	}
	if checked != 4 {
		t.Fatalf("expected 4 parse-layer negative vectors, checked %d", checked)
	}
}
