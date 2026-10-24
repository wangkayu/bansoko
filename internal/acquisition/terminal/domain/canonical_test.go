package domain

import (
	"errors"
	"strings"
	"testing"
)

func mustCanonical(t *testing.T, input string) string {
	t.Helper()
	v, err := ParseStrict([]byte(input))
	if err != nil {
		t.Fatalf("ParseStrict(%q): %v", input, err)
	}
	return string(Canonical(v))
}

func parseCode(t *testing.T, input string) Code {
	t.Helper()
	_, err := ParseStrict([]byte(input))
	if err == nil {
		t.Fatalf("ParseStrict(%q) must be rejected", input)
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not a *Error", err)
	}
	return e.Code
}

// The encoder table of the persistence contract (canonical V1). Key order and
// whitespace do not change the output; a large integer is not rounded; the
// output is literal UTF-8, not HTML-escaped.
func TestCanonicalEncoderTable(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"b":0,"a":false}`, `{"a":false,"b":0}`},
		{`{"a":false,"b":0}`, `{"a":false,"b":0}`},
		{` { "a" : false , "b" : 0 } `, `{"a":false,"b":0}`},
		{`{"n":9007199254740993}`, `{"n":9007199254740993}`},
		{`{"n":-9223372036854775808}`, `{"n":-9223372036854775808}`},
		{`{"n":9223372036854775807}`, `{"n":9223372036854775807}`},
		{`{"s":"<猫>"}`, `{"s":"<猫>"}`},
		{`{"s":"a&b<"}`, `{"s":"a&b<"}`},
		{`{"a":[],"b":{},"c":null}`, `{"a":[],"b":{},"c":null}`},
		{`[3,1,2]`, `[3,1,2]`},
	} {
		if got := mustCanonical(t, tc.in); got != tc.want {
			t.Errorf("canonical(%s) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// Only the quote, the backslash and U+0001..U+001F are escaped, always as
// lower-case \u00xx for control characters, never as \n or \t; U+2028 and U+2029
// and U+007F are written literally.
func TestCanonicalStringEscaping(t *testing.T) {
	in := "{\"s\":\"q\\\" b\\\\ n\\n t\\t c\\u001f d\\u007f ls  ps  e\\u00e9\"}"
	want := "{\"s\":\"q\\\" b\\\\ n\\u000a t\\u0009 c\\u001f d\x7f ls  ps  eé\"}"
	if got := mustCanonical(t, in); got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	// An escaped surrogate pair is the astral code point, written as literal UTF-8.
	if got := mustCanonical(t, `{"s":"😀"}`); got != "{\"s\":\"😀\"}" {
		t.Fatalf("surrogate pair: %q", got)
	}
}

func TestCanonicalIsIdempotent(t *testing.T) {
	once := mustCanonical(t, `{"z":[1,{"b":"x","a":"\u0001"}],"a":true}`)
	if twice := mustCanonical(t, once); twice != once {
		t.Fatalf("canonical is not a fixed point:\n%s\n%s", once, twice)
	}
}

// Digest = SHA256(domain || 0x00 || canonical): the contract's hex table, computed
// independently in Python, reproduced here.
func TestDigestMatchesTheContractTable(t *testing.T) {
	for _, tc := range []struct{ canonical, hex string }{
		{`{"a":false,"b":0}`, "89656c04e5502455a108f70fc6169f8ca9fdf8ab06c4703b44fe29acce6f9cf6"},
		{`{"n":9007199254740993}`, "7b3d8b20c937255e469ba013354684f14e7c5e556018029d9fc51ff9813b8acd"},
		{`{"s":"<猫>"}`, "3ad704b3e05deb302287d20a818a39f9a616bb7f51ef22829e5dcfff506feba3"},
		{`{"reason_code":null,"state":"absent","value":null}`, "28e3f2953fb52cfdc0ccfe196746718be9065bb49577a9edd013a62d163091c6"},
		{`{"reason_code":null,"state":"known","value":0}`, "aa77f8c1c4299a8295a69adbbe14f69317f2e51843555a3aac9e5ae85dc89a1c"},
		{`{"reason_code":"MISSING_METADATA","state":"unavailable","value":null}`, "f053d05ea80aed08fa9bfb1d8f3c494dfacededa359351e2e849502cc5ea64e5"},
	} {
		got := Digest("acquisition-input:v1", []byte(tc.canonical))
		if got != "sha256:"+tc.hex {
			t.Errorf("Digest(%s) = %s, want sha256:%s", tc.canonical, got, tc.hex)
		}
	}
}

func TestDigestDomainsDoNotCollide(t *testing.T) {
	bytes := []byte(`{"a":1}`)
	a, b := Digest("acquisition-input:v1", bytes), Digest("acquisition-plan:v1", bytes)
	if a == b {
		t.Fatal("the same bytes under two domains must hash differently")
	}
	// The NUL separator keeps a domain/body boundary unambiguous.
	if Digest("ab", []byte("c")) == Digest("a", []byte("bc")) {
		t.Fatal("domain and body must not be able to shift across the separator")
	}
}

// What the strict parser must refuse, with the first-error code the DTO contract
// names for each.
func TestParseStrictRejectsWhatCanonicalMustNeverAbsorb(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		code Code
	}{
		{"duplicate key", `{"a":1,"a":2}`, CodeDuplicateKey},
		{"nested duplicate key", `{"o":{"k":1,"k":1}}`, CodeDuplicateKey},
		{"exponent integer", `{"n":1e0}`, CodeIntegerTokenInvalid},
		{"fraction", `{"n":1.5}`, CodeIntegerTokenInvalid},
		{"integer-valued fraction", `{"n":1.0}`, CodeIntegerTokenInvalid},
		{"leading zero", `{"n":012}`, CodeIntegerTokenInvalid},
		{"negative zero", `{"n":-0}`, CodeIntegerTokenInvalid},
		{"beyond int64", `{"n":9223372036854775808}`, CodeIntegerTokenInvalid},
		{"below int64", `{"n":-9223372036854775809}`, CodeIntegerTokenInvalid},
		{"two objects", `{"a":1}{}`, CodeTrailingToken},
		{"trailing garbage", `{"a":1} x`, CodeTrailingToken},
		{"NUL escape", `{"s":"\u0000"}`, CodeNULForbidden},
		{"raw NUL", "{\"s\":\"a\x00b\"}", CodeNULForbidden},
		{"NUL in a key", `{"k\u0000":1}`, CodeNULForbidden},
		{"lone high surrogate", `{"s":"\ud83d"}`, CodeUTF8Invalid},
		{"lone low surrogate", `{"s":"\ude00"}`, CodeUTF8Invalid},
		{"invalid UTF-8", "{\"s\":\"\xff\"}", CodeUTF8Invalid},
		{"raw control character", "{\"s\":\"a\tb\"}", CodeSyntaxInvalid},
		{"BOM is not whitespace", "\xef\xbb\xbf{}", CodeSyntaxInvalid},
		{"truncated", `{"a":`, CodeSyntaxInvalid},
		{"empty", ``, CodeSyntaxInvalid},
		{"trailing comma", `{"a":1,}`, CodeSyntaxInvalid},
		{"single quotes", `{'a':1}`, CodeSyntaxInvalid},
		{"bare word", `{"a":nope}`, CodeSyntaxInvalid},
	} {
		if got := parseCode(t, tc.in); got != tc.code {
			t.Errorf("%s: code = %s, want %s", tc.name, got, tc.code)
		}
	}
}

func TestParseStrictReportsTheFirstErrorWithAPath(t *testing.T) {
	_, err := ParseStrict([]byte(`{"outer":{"inner":[1,{"k":1,"k":2}]}}`))
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("not a *Error: %v", err)
	}
	if e.Code != CodeDuplicateKey || e.Path != "/outer/inner/1/k" {
		t.Fatalf("got %s at %q, want DUPLICATE_KEY at /outer/inner/1/k", e.Code, e.Path)
	}
	// A '/' or '~' in a key is escaped in the pointer, so the path is unambiguous.
	_, err = ParseStrict([]byte(`{"a/b~c":{"k":1,"k":2}}`))
	if !errors.As(err, &e) || e.Path != "/a~1b~0c/k" {
		t.Fatalf("pointer escaping: %v", err)
	}
	// The error never carries a value.
	if strings.Contains(e.Error(), "secret") {
		t.Fatal("errors must not echo values")
	}
}

func TestParseStrictBoundsNesting(t *testing.T) {
	deep := strings.Repeat("[", MaxDepth+2) + strings.Repeat("]", MaxDepth+2)
	if got := parseCode(t, deep); got != CodeDepthExceeded {
		t.Fatalf("depth code = %s", got)
	}
	ok := strings.Repeat("[", MaxDepth) + strings.Repeat("]", MaxDepth)
	if _, err := ParseStrict([]byte(ok)); err != nil {
		t.Fatalf("depth %d must be accepted: %v", MaxDepth, err)
	}
}
