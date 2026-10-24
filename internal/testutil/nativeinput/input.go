// Package nativeinput builds complete registry-valid acceptance test inputs.
// It expands independent golden vectors for fixtures only, never production.
package nativeinput

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	protocol "bansoko/internal/acquisition/terminal/domain"
)

// Canonical returns the canonical bytes of the golden input vector with the
// payload and config digests bound to this fixture. There is one input schema
// (schema_version 1), so there is no version parameter.
func Canonical(t *testing.T, payloadDigest, configDigest string) string {
	t.Helper()
	input := goldenInput(t)
	input.Object["source"].Object["payload_digest"] = protocol.Value{Kind: protocol.KindString, String: payloadDigest}
	input.Object["config"].Object["effective_digest"] = protocol.Value{Kind: protocol.KindString, String: configDigest}
	if err := protocol.Validate("AcquisitionInputV1", input); err != nil {
		t.Fatal(err)
	}
	return string(protocol.Canonical(input))
}

// CanonicalBytes is Canonical without a *testing.T, so a fixture command can
// print the very bytes the Go fixtures bind their admission digest to instead
// of restating the input schema in shell.
func CanonicalBytes(payloadDigest, configDigest string) (string, error) {
	input, err := GoldenInput()
	if err != nil {
		return "", err
	}
	input.Object["source"].Object["payload_digest"] = protocol.Value{Kind: protocol.KindString, String: payloadDigest}
	input.Object["config"].Object["effective_digest"] = protocol.Value{Kind: protocol.KindString, String: configDigest}
	if err := protocol.Validate("AcquisitionInputV1", input); err != nil {
		return "", err
	}
	return string(protocol.Canonical(input)), nil
}

// Media returns the golden input vector's media object with its $fixture
// references expanded: the evidence a seeded acceptance input carries. A
// fixture that must resolve naming facts BEFORE acceptance (for example the
// replacement binding an overwrite delivery is admitted with) overlays its own
// fixture_media_values on this object exactly as the seeding helper does,
// instead of restating the golden values and drifting from the accepted input.
func Media(t *testing.T) map[string]any {
	t.Helper()
	var media map[string]any
	if err := json.Unmarshal(protocol.Canonical(goldenInput(t).Object["media"]), &media); err != nil {
		t.Fatal(err)
	}
	return media
}

// goldenInput returns the expanded golden input vector. Callers own the
// returned value and may bind their own digests.
func goldenInput(t *testing.T) protocol.Value {
	t.Helper()
	v, err := GoldenInput()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// GoldenInput returns the expanded golden input vector without a *testing.T.
func GoldenInput() (protocol.Value, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return protocol.Value{}, errors.New("fixture source unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../acquisition/terminal/domain/testdata/dto-golden-vectors.json"))
	if err != nil {
		return protocol.Value{}, err
	}
	var fixture struct {
		Definitions map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		return protocol.Value{}, err
	}
	defs := map[string]protocol.Value{}
	for name, raw := range fixture.Definitions {
		v, err := protocol.ParseStrict(raw)
		if err != nil {
			return protocol.Value{}, err
		}
		defs[name] = v
	}
	var expand func(protocol.Value) protocol.Value
	expand = func(v protocol.Value) protocol.Value {
		if v.Kind == protocol.KindObject {
			if ref, ok := v.Object["$fixture"]; ok {
				return expand(defs[ref.String])
			}
			out := protocol.Value{Kind: protocol.KindObject, Object: map[string]protocol.Value{}}
			for k, x := range v.Object {
				out.Object[k] = expand(x)
			}
			return out
		}
		if v.Kind == protocol.KindArray {
			out := protocol.Value{Kind: protocol.KindArray}
			for _, x := range v.Array {
				out.Array = append(out.Array, expand(x))
			}
			return out
		}
		return v
	}
	return expand(defs["input"]), nil
}
