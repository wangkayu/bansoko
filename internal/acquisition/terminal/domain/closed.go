package domain

import (
	"sort"
	"strconv"
)

// Type- and semantic-layer codes. They come after the parse layer in the error
// order: lexical and duplicate keys, then closed keys, then branches and types,
// then cross-field semantics.
const (
	// CodeTypeInvalid is a value of the wrong JSON kind or a malformed scalar
	// (a bad ID, digest or reason code).
	CodeTypeInvalid Code = "TYPE_INVALID"
	// CodeConstraintViolated is a well-typed value that breaks a cross-field rule.
	CodeConstraintViolated Code = "CONSTRAINT_VIOLATED"
)

// reader walks one decoded Value with a JSON Pointer, so every typed decoder
// reports the same first-error shape as the parse layer.
type reader struct {
	path []string
}

func (r reader) at(segment string) reader {
	return reader{path: append(append([]string(nil), r.path...), segment)}
}

func (r reader) fail(code Code) error { return newError(code, r.path) }

// closed requires v to be an object with exactly the keys in want: a missing key
// is MISSING_KEY, an extra one UNKNOWN_KEY, and an explicit null is not a missing
// key. Keys are checked in sorted order so the first error is deterministic.
func (r reader) closed(v Value, want ...string) (map[string]Value, error) {
	if v.Kind != KindObject {
		return nil, r.fail(CodeTypeInvalid)
	}
	allowed := make(map[string]struct{}, len(want))
	for _, k := range want {
		allowed[k] = struct{}{}
	}
	for _, k := range sortedKeys(v.Object) {
		if _, ok := allowed[k]; !ok {
			return nil, r.at(k).fail(CodeUnknownKey)
		}
	}
	sorted := append([]string(nil), want...)
	sort.Strings(sorted)
	for _, k := range sorted {
		if _, ok := v.Object[k]; !ok {
			return nil, r.at(k).fail(CodeMissingKey)
		}
	}
	return v.Object, nil
}

// branch reads a discriminated union: the discriminator first, then the exact key
// set of the branch it names. A key that belongs to another branch is a
// UNION_BRANCH_INVALID, a key that belongs to none is UNKNOWN_KEY, and a missing
// branch key is MISSING_KEY. branches maps each discriminator value to its keys
// (the discriminator itself is implied).
func (r reader) branch(v Value, discriminator string, branches map[string][]string) (string, map[string]Value, error) {
	if v.Kind != KindObject {
		return "", nil, r.fail(CodeTypeInvalid)
	}
	d, ok := v.Object[discriminator]
	if !ok {
		return "", nil, r.at(discriminator).fail(CodeMissingKey)
	}
	if d.Kind != KindString {
		return "", nil, r.at(discriminator).fail(CodeTypeInvalid)
	}
	keys, ok := branches[d.String]
	if !ok {
		return "", nil, r.at(discriminator).fail(CodeUnionBranchInvalid)
	}
	inBranch := map[string]struct{}{discriminator: {}}
	for _, k := range keys {
		inBranch[k] = struct{}{}
	}
	inAny := map[string]struct{}{discriminator: {}}
	for _, ks := range branches {
		for _, k := range ks {
			inAny[k] = struct{}{}
		}
	}
	for _, k := range sortedKeys(v.Object) {
		if _, ok := inBranch[k]; ok {
			continue
		}
		if _, ok := inAny[k]; ok {
			return "", nil, r.at(k).fail(CodeUnionBranchInvalid)
		}
		return "", nil, r.at(k).fail(CodeUnknownKey)
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for _, k := range sorted {
		if _, ok := v.Object[k]; !ok {
			return "", nil, r.at(k).fail(CodeMissingKey)
		}
	}
	return d.String, v.Object, nil
}

func sortedKeys(m map[string]Value) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// obj builds an object Value; arr builds an array; the small constructors keep
// the typed encoders short and make the encoded shape easy to read.
func obj(members map[string]Value) Value { return Value{Kind: KindObject, Object: members} }
func arr(items []Value) Value {
	if items == nil {
		items = []Value{}
	}
	return Value{Kind: KindArray, Array: items}
}
func str(s string) Value   { return Value{Kind: KindString, String: s} }
func i64(n int64) Value    { return Value{Kind: KindInteger, Int: n} }
func null() Value          { return Value{Kind: KindNull} }
func boolean(b bool) Value { return Value{Kind: KindBool, Bool: b} }

// utf8Len is the byte length used by the string limits.
func utf8Len(s string) int { return len(s) }

func itoa(i int) string { return strconv.Itoa(i) }
