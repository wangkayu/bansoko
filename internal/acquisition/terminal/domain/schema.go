package domain

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// The strict registry is data: every persisted type is a Schema built from a few
// constructors, mirroring one row of the DTO and persistence contracts. One
// generic validator interprets them, so there is exactly one place that decides
// what "closed object", "union branch", "frozen value" and "limit" mean, and no
// type can quietly grow an unlisted key or accept a null where the contract has
// none. Cross-field rules are attached with Check and run only after the keys,
// branches and member types of that object have already passed.

// CodeInputSchemaUnsupported marks a structurally valid value that names a branch
// the current release deliberately does not execute (a future Vars mode, a future
// media-info shape). It is refused, never converted into a supported one.
const CodeInputSchemaUnsupported Code = "INPUT_SCHEMA_UNSUPPORTED"

// Schema validates one Value.
type Schema interface {
	validate(r reader, v Value) error
}

type field struct {
	name string
	typ  Schema
}

// F names one member of an object or union branch.
func F(name string, typ Schema) field { return field{name: name, typ: typ} }

type objectSchema struct {
	fields   []field
	optional map[string]struct{}
	checks   []func(r reader, m map[string]Value) error
}

// Obj is a closed object: exactly these keys, each validated by its schema.
func Obj(fields ...field) *objectSchema { return &objectSchema{fields: fields} }

// Optional lets the named members be absent. The object stays closed: an
// unknown key is still refused, and a present optional member is validated.
// It exists for a member added to a stored shape after values without it were
// frozen, so those values keep their exact canonical form and digest.
func (o *objectSchema) Optional(names ...string) *objectSchema {
	if o.optional == nil {
		o.optional = make(map[string]struct{}, len(names))
	}
	for _, name := range names {
		o.optional[name] = struct{}{}
	}
	return o
}

// Check adds a cross-field rule that runs after every member has validated.
func (o *objectSchema) Check(fn func(r reader, m map[string]Value) error) *objectSchema {
	o.checks = append(o.checks, fn)
	return o
}

func (o *objectSchema) validate(r reader, v Value) error {
	names := make([]string, len(o.fields))
	for i, f := range o.fields {
		names[i] = f.name
	}
	if len(o.optional) > 0 && v.Kind == KindObject {
		// An absent optional member leaves the required set; every other key
		// rule (unknown, missing, member validation) is unchanged.
		present := make([]string, 0, len(names))
		for _, name := range names {
			if _, optional := o.optional[name]; optional {
				if _, ok := v.Object[name]; !ok {
					continue
				}
			}
			present = append(present, name)
		}
		names = present
	}
	m, err := r.closed(v, names...)
	if err != nil {
		return err
	}
	return o.members(r, m)
}

func (o *objectSchema) members(r reader, m map[string]Value) error {
	byName := make(map[string]Schema, len(o.fields))
	for _, f := range o.fields {
		byName[f.name] = f.typ
	}
	for _, k := range sortedKeys(m) {
		if err := byName[k].validate(r.at(k), m[k]); err != nil {
			return err
		}
	}
	for _, check := range o.checks {
		if err := check(r, m); err != nil {
			return err
		}
	}
	return nil
}

type unionSchema struct {
	discriminator string
	branches      map[string]*objectSchema
}

// Union is a discriminated union: the discriminator picks one branch and that
// branch's exact keys apply. A key of another branch is UNION_BRANCH_INVALID.
func Union(discriminator string, branches map[string]*objectSchema) Schema {
	return &unionSchema{discriminator: discriminator, branches: branches}
}

func (u *unionSchema) validate(r reader, v Value) error {
	keys := make(map[string][]string, len(u.branches))
	for name, b := range u.branches {
		for _, f := range b.fields {
			keys[name] = append(keys[name], f.name)
		}
		if keys[name] == nil {
			keys[name] = []string{}
		}
	}
	name, m, err := r.branch(v, u.discriminator, keys)
	if err != nil {
		return err
	}
	branch := u.branches[name]
	rest := make(map[string]Value, len(m))
	for k, val := range m {
		if k != u.discriminator {
			rest[k] = val
		}
	}
	// The discriminator is part of the branch's key set in the wire form but is
	// not re-validated as a member; every other member is.
	if err := branch.members(r, rest); err != nil {
		return err
	}
	return nil
}

type fvSchema struct{ inner Schema }

// FVOf is a strict frozen value (known / absent / unavailable) whose known value
// is validated by inner.
func FVOf(inner Schema) Schema { return fvSchema{inner: inner} }

func (s fvSchema) validate(r reader, v Value) error {
	_, err := decodeFV(r, v, func(r reader, v Value) (struct{}, error) {
		return struct{}{}, s.inner.validate(r, v)
	})
	return err
}

type nullableSchema struct{ inner Schema }

// Nullable is N(T): the key must be present and the value may be null.
func Nullable(inner Schema) Schema { return nullableSchema{inner: inner} }

func (s nullableSchema) validate(r reader, v Value) error {
	if v.Kind == KindNull {
		return nil
	}
	return s.inner.validate(r, v)
}

type funcSchema func(r reader, v Value) error

func (f funcSchema) validate(r reader, v Value) error { return f(r, v) }

// Custom wraps a hand-written validator for the few types with their own rules.
func Custom(fn func(r reader, v Value) error) Schema { return funcSchema(fn) }

type refSchema struct{ name string }

// Ref points at a registered type by name, so types can share and recurse.
func Ref(name string) Schema { return refSchema{name: name} }

func (s refSchema) validate(r reader, v Value) error {
	target, ok := registry[s.name]
	if !ok {
		panic("domain: schema " + s.name + " is not registered")
	}
	return target.validate(r, v)
}

var registry = map[string]Schema{}

func register(name string, s Schema) {
	if _, dup := registry[name]; dup {
		panic("domain: schema " + name + " registered twice")
	}
	registry[name] = s
}

// Validate checks v against the registered type name. It returns the first error
// in the contract's order, with a JSON Pointer and a stable code.
func Validate(name string, v Value) error {
	s, ok := registry[name]
	if !ok {
		return &Error{Code: CodeTypeInvalid, Path: "/<unregistered " + name + ">"}
	}
	return s.validate(reader{}, v)
}

// Registered lists the registered type names, for tests that audit the registry.
func Registered() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// --- scalars -----------------------------------------------------------------

type strSchema struct {
	min, max int // byte length bounds; max 0 means unbounded (callers always set one)
	enum     []string
	ok       func(string) bool
}

// Text is a string of min..max UTF-8 bytes with no NUL (already refused by the parser).
func Text(min, max int) Schema { return strSchema{min: min, max: max} }

// Enum is a closed set of strings.
func Enum(values ...string) Schema { return strSchema{enum: values} }

func (s strSchema) validate(r reader, v Value) error {
	if v.Kind != KindString {
		return r.fail(CodeTypeInvalid)
	}
	str := v.String
	if len(s.enum) > 0 {
		for _, e := range s.enum {
			if e == str {
				return nil
			}
		}
		return r.fail(CodeUnionBranchInvalid)
	}
	if len(str) < s.min || (s.max > 0 && len(str) > s.max) {
		return r.fail(CodeTypeInvalid)
	}
	if s.ok != nil && !s.ok(str) {
		return r.fail(CodeTypeInvalid)
	}
	return nil
}

type intSchema struct{ min, max int64 }

// Int is an I64 within min..max.
func Int(min, max int64) Schema { return intSchema{min: min, max: max} }

func (s intSchema) validate(r reader, v Value) error {
	if v.Kind != KindInteger {
		return r.fail(CodeTypeInvalid)
	}
	if v.Int < s.min || v.Int > s.max {
		return r.fail(CodeConstraintViolated)
	}
	return nil
}

const maxI64 = int64(^uint64(0) >> 1)

var (
	// I64 is any 64-bit signed integer; Size is a non-negative one.
	I64  = Int(-maxI64-1, maxI64)
	Size = Int(0, maxI64)
	Bool = Custom(func(r reader, v Value) error {
		if v.Kind != KindBool {
			return r.fail(CodeTypeInvalid)
		}
		return nil
	})
	IDType   = Custom(func(r reader, v Value) error { _, err := r.id(v); return err })
	DigestOf = Custom(func(r reader, v Value) error { _, err := r.digest(v); return err })
	ReasonOf = Custom(func(r reader, v Value) error { _, err := r.reason(v); return err })
)

// Path is an absolute or relative path string of at most 4096 bytes. Whether it is
// safe to use is a runtime decision; the registry only bounds and types it.
var Path = Text(1, 4096)

// Title and Display are bounded display strings; known empty values are allowed
// only where the contract says so, by using Text(0, …) at that field.
var (
	Title   = Text(1, 8192)
	Display = Text(0, 8192)
)

// Time is the persisted UTC timestamp: RFC 3339 with a literal Z, at most
// microsecond precision, and no superfluous trailing fractional zeros. So
// "2026-10-03T01:02:03Z" and "2026-10-03T01:02:03.123456Z" are canonical while
// "...03.120000Z" (a trailing zero), "...03.1234567Z" (finer than a microsecond),
// "...03+00:00" (an offset) and a lower-case z are refused, never silently
// rewritten: re-hashing a rewritten time would drift the digest.
var Time = Custom(func(r reader, v Value) error {
	if v.Kind != KindString {
		return r.fail(CodeTypeInvalid)
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil || t.Nanosecond()%1000 != 0 || t.UTC().Format(time.RFC3339Nano) != v.String {
		return r.fail(CodeTypeInvalid)
	}
	return nil
})

var episodePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]*[1-9])?$`)

// Episode is a canonical non-negative decimal string of at most 128 bytes:
// "12.5" is canonical, "0012.50" is refused rather than quietly re-hashed.
var Episode = Custom(func(r reader, v Value) error {
	if v.Kind != KindString || len(v.String) > 128 || !episodePattern.MatchString(v.String) {
		return r.fail(CodeTypeInvalid)
	}
	return nil
})

func hexString(n int) Schema {
	return Custom(func(r reader, v Value) error {
		if v.Kind != KindString || len(v.String) != n {
			return r.fail(CodeTypeInvalid)
		}
		for _, c := range v.String {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return r.fail(CodeTypeInvalid)
			}
		}
		return nil
	})
}

// HashV1 is a 40-digit and HashV2 a 64-digit lower-case hex torrent info-hash. They
// are not SHA-256 content digests and never compare equal to one.
var (
	HashV1 = hexString(40)
	HashV2 = hexString(64)
)

// U64Text is a uint64 carried as text so it is never rounded through a JSON float.
var U64Text = Custom(func(r reader, v Value) error {
	if v.Kind != KindString || v.String == "" || len(v.String) > 20 {
		return r.fail(CodeTypeInvalid)
	}
	if v.String != "0" && v.String[0] == '0' {
		return r.fail(CodeTypeInvalid)
	}
	var n uint64
	for _, c := range v.String {
		if c < '0' || c > '9' {
			return r.fail(CodeTypeInvalid)
		}
		d := uint64(c - '0')
		if n > (^uint64(0)-d)/10 {
			return r.fail(CodeTypeInvalid)
		}
		n = n*10 + d
	}
	return nil
})

type arrSchema struct {
	elem     Schema
	min, max int
	check    func(r reader, items []Value) error
}

// Array is a present, non-null array of min..max elements, each validated by elem.
func Array(elem Schema, min, max int) *arrSchema { return &arrSchema{elem: elem, min: min, max: max} }

// Ordered adds a rule on the whole array (sorted, unique, …).
func (a *arrSchema) Ordered(check func(r reader, items []Value) error) *arrSchema {
	a.check = check
	return a
}

func (a *arrSchema) validate(r reader, v Value) error {
	if v.Kind != KindArray {
		return r.fail(CodeTypeInvalid)
	}
	if len(v.Array) < a.min || len(v.Array) > a.max {
		return r.fail(CodeConstraintViolated)
	}
	for i, item := range v.Array {
		if err := a.elem.validate(r.at(itoa(i)), item); err != nil {
			return err
		}
	}
	if a.check != nil {
		return a.check(r, v.Array)
	}
	return nil
}

// SortedUnique requires string elements in ascending byte order with no repeats.
func SortedUnique(r reader, items []Value) error {
	for i := 1; i < len(items); i++ {
		if items[i].String <= items[i-1].String {
			return r.fail(CodeConstraintViolated)
		}
	}
	return nil
}

// containsControl reports whether s has any control character.
func containsControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }
