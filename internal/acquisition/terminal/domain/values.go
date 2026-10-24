package domain

import (
	"strings"
	"unicode"
)

// maxIDBytes bounds an ID in UTF-8 bytes.
const maxIDBytes = 128

// ID is a non-empty identifier with no control characters and no surrounding
// whitespace, at most 128 UTF-8 bytes. It is an opaque reference, never a path.
type ID string

func (r reader) id(v Value) (ID, error) {
	if v.Kind != KindString {
		return "", r.fail(CodeTypeInvalid)
	}
	s := v.String
	if s == "" || utf8Len(s) > maxIDBytes || s != strings.TrimSpace(s) || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return "", r.fail(CodeTypeInvalid)
	}
	return ID(s), nil
}

// Digest is "sha256:" and 64 lower-case hex digits.
type DigestText string

func validDigest(s string) bool {
	const prefix = "sha256:"
	if len(s) != len(prefix)+64 || !strings.HasPrefix(s, prefix) {
		return false
	}
	for _, c := range s[len(prefix):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (r reader) digest(v Value) (DigestText, error) {
	if v.Kind != KindString || !validDigest(v.String) {
		return "", r.fail(CodeTypeInvalid)
	}
	return DigestText(v.String), nil
}

// Reason uses the historical upper-case safe-code grammar, plus the two
// exact lower-case evidence reasons: the
// work has no recorded tree top to route an unknown file under, or the
// deployment has no path mapping.
// Other lower-case reasons fail closed; never accept exception text or URLs.
type Reason string

// ValidReason reports whether s is a safe reason code.
func ValidReason(s string) bool { return validReason(s) }

func validReason(s string) bool {
	if s == "anime_root_unavailable" || s == "path_mapping_unconfigured" {
		return true
	}
	if s == "" || len(s) > 128 || !(s[0] >= 'A' && s[0] <= 'Z') {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

func (r reader) reason(v Value) (Reason, error) {
	if v.Kind != KindString || !validReason(v.String) {
		return "", r.fail(CodeTypeInvalid)
	}
	return Reason(v.String), nil
}

func (r reader) int64(v Value) (int64, error) {
	if v.Kind != KindInteger {
		return 0, r.fail(CodeTypeInvalid)
	}
	return v.Int, nil
}

// RefVersion is a reference with the version it was read at; version is positive.
// An unknown version is expressed by an enclosing FV, never as 0.
type RefVersion struct {
	Ref     ID
	Version int64
}

func (r reader) refVersion(v Value) (RefVersion, error) {
	m, err := r.closed(v, "ref", "version")
	if err != nil {
		return RefVersion{}, err
	}
	ref, err := r.at("ref").id(m["ref"])
	if err != nil {
		return RefVersion{}, err
	}
	version, err := r.at("version").int64(m["version"])
	if err != nil {
		return RefVersion{}, err
	}
	if version <= 0 {
		return RefVersion{}, r.at("version").fail(CodeConstraintViolated)
	}
	return RefVersion{Ref: ref, Version: version}, nil
}

func (x RefVersion) value() Value {
	return obj(map[string]Value{"ref": str(string(x.Ref)), "version": i64(x.Version)})
}

// FVState is the state of a frozen value.
type FVState string

const (
	// FVKnown carries a non-null value; a known empty string, 0 or false is a real value.
	FVKnown FVState = "known"
	// FVAbsent means the value does not exist for this input.
	FVAbsent FVState = "absent"
	// FVUnavailable means it exists but could not be obtained; Reason says why.
	FVUnavailable FVState = "unavailable"
)

// FV is a strict frozen value: exactly state, value and reason_code. known has a
// non-null value and no reason; absent has neither; unavailable has no value and
// a safe reason. The three states hash differently, so a legitimate 0 can never
// be mistaken for an absent value.
type FV[T any] struct {
	State  FVState
	Value  T
	Reason Reason
}

func Known[T any](v T) FV[T] { return FV[T]{State: FVKnown, Value: v} }
func Absent[T any]() FV[T]   { return FV[T]{State: FVAbsent} }
func Unavailable[T any](reason Reason) FV[T] {
	return FV[T]{State: FVUnavailable, Reason: reason}
}

// decodeFV reads a frozen value, decoding a known value with decode.
func decodeFV[T any](r reader, v Value, decode func(reader, Value) (T, error)) (FV[T], error) {
	m, err := r.closed(v, "state", "value", "reason_code")
	if err != nil {
		return FV[T]{}, err
	}
	state := m["state"]
	if state.Kind != KindString {
		return FV[T]{}, r.at("state").fail(CodeTypeInvalid)
	}
	switch FVState(state.String) {
	case FVKnown:
		if m["value"].Kind == KindNull {
			return FV[T]{}, r.at("value").fail(CodeNullNotAllowed)
		}
		if m["reason_code"].Kind != KindNull {
			return FV[T]{}, r.at("reason_code").fail(CodeConstraintViolated)
		}
		value, err := decode(r.at("value"), m["value"])
		if err != nil {
			return FV[T]{}, err
		}
		return Known(value), nil
	case FVAbsent:
		if m["value"].Kind != KindNull || m["reason_code"].Kind != KindNull {
			return FV[T]{}, r.fail(CodeConstraintViolated)
		}
		return Absent[T](), nil
	case FVUnavailable:
		if m["value"].Kind != KindNull {
			return FV[T]{}, r.at("value").fail(CodeConstraintViolated)
		}
		if m["reason_code"].Kind == KindNull {
			return FV[T]{}, r.at("reason_code").fail(CodeNullNotAllowed)
		}
		reason, err := r.at("reason_code").reason(m["reason_code"])
		if err != nil {
			return FV[T]{}, err
		}
		return Unavailable[T](reason), nil
	}
	return FV[T]{}, r.at("state").fail(CodeUnionBranchInvalid)
}

func (f FV[T]) value(encode func(T) Value) Value {
	m := map[string]Value{"state": str(string(f.State)), "value": null(), "reason_code": null()}
	switch f.State {
	case FVKnown:
		m["value"] = encode(f.Value)
	case FVUnavailable:
		m["reason_code"] = str(string(f.Reason))
	}
	return obj(m)
}
