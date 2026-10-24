// Package domain holds the strict, pure value protocol of the independent
// acquisition: one strict decoder, one canonical encoder and the digest domains
// every persisted acquisition value is hashed under. It has no I/O and no
// dependency on the database, transport or any runtime; nothing here is wired
// into the running product yet.
//
// The protocol is specified in the task's persistence and DTO contracts
// (canonical V1). Its design vectors were computed independently in Python; the
// tests in this package reproduce them byte for byte, so the encoder is checked
// against an implementation that shares none of its code.
package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// CanonicalVersion names the byte encoding only; it is not a domain schema.
const CanonicalVersion = "acquisition-canonical-v1"

// MaxDepth bounds nesting so a hostile document cannot exhaust the stack.
const MaxDepth = 32

// Code is a stable, safe error code. It never carries a raw value.
type Code string

// Parse-layer codes (lexical, duplicate keys, closed objects) come first in the
// error order; type and semantic layers add theirs in their own decoders.
const (
	CodeSyntaxInvalid       Code = "SYNTAX_INVALID"
	CodeDepthExceeded       Code = "DEPTH_EXCEEDED"
	CodeTrailingToken       Code = "TRAILING_TOKEN"
	CodeUTF8Invalid         Code = "UTF8_INVALID"
	CodeNULForbidden        Code = "NUL_FORBIDDEN"
	CodeIntegerTokenInvalid Code = "INTEGER_TOKEN_INVALID"
	CodeDuplicateKey        Code = "DUPLICATE_KEY"
	CodeMissingKey          Code = "MISSING_KEY"
	CodeUnknownKey          Code = "UNKNOWN_KEY"
	CodeUnionBranchInvalid  Code = "UNION_BRANCH_INVALID"
	CodeNullNotAllowed      Code = "NULL_NOT_ALLOWED"
)

// Error is the first error found, with a JSON Pointer to where it was found.
type Error struct {
	Code Code
	Path string
}

func (e *Error) Error() string {
	if e.Path == "" {
		return string(e.Code)
	}
	return string(e.Code) + " at " + e.Path
}

func newError(code Code, path []string) *Error {
	var b strings.Builder
	for _, segment := range path {
		b.WriteByte('/')
		b.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(segment))
	}
	return &Error{Code: code, Path: b.String()}
}

// Kind is the JSON kind of a Value. There is no float: the protocol has none.
type Kind int

const (
	KindNull Kind = iota
	KindBool
	KindString
	KindInteger
	KindArray
	KindObject
)

// Value is a parsed JSON value. Integers are exact int64s; objects have unique keys.
type Value struct {
	Kind   Kind
	Bool   bool
	String string
	Int    int64
	Array  []Value
	Object map[string]Value
}

// ParseStrict parses exactly one JSON value and nothing else. It rejects what
// the canonical protocol must never absorb silently: duplicate keys (the last
// one is not taken), non-integer or out-of-range numbers, invalid UTF-8 and lone
// surrogates, NUL in any form, raw control characters, and trailing tokens.
func ParseStrict(data []byte) (Value, error) {
	if !utf8.Valid(data) {
		return Value{}, newError(CodeUTF8Invalid, nil)
	}
	p := &parser{data: data}
	p.skipSpace()
	value, err := p.value(nil, 0)
	if err != nil {
		return Value{}, err
	}
	p.skipSpace()
	if p.pos != len(p.data) {
		return Value{}, newError(CodeTrailingToken, nil)
	}
	return value, nil
}

type parser struct {
	data []byte
	pos  int
}

func (p *parser) skipSpace() {
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value(path []string, depth int) (Value, error) {
	if depth > MaxDepth {
		return Value{}, newError(CodeDepthExceeded, path)
	}
	if p.pos >= len(p.data) {
		return Value{}, newError(CodeSyntaxInvalid, path)
	}
	switch c := p.data[p.pos]; {
	case c == '{':
		return p.object(path, depth)
	case c == '[':
		return p.array(path, depth)
	case c == '"':
		s, err := p.str(path)
		return Value{Kind: KindString, String: s}, err
	case c == '-' || (c >= '0' && c <= '9'):
		return p.integer(path)
	case p.literal("true"):
		return Value{Kind: KindBool, Bool: true}, nil
	case p.literal("false"):
		return Value{Kind: KindBool}, nil
	case p.literal("null"):
		return Value{Kind: KindNull}, nil
	}
	return Value{}, newError(CodeSyntaxInvalid, path)
}

func (p *parser) literal(word string) bool {
	if strings.HasPrefix(string(p.data[p.pos:min(len(p.data), p.pos+len(word))]), word) {
		p.pos += len(word)
		return true
	}
	return false
}

func (p *parser) object(path []string, depth int) (Value, error) {
	p.pos++ // {
	obj := map[string]Value{}
	p.skipSpace()
	if p.pos < len(p.data) && p.data[p.pos] == '}' {
		p.pos++
		return Value{Kind: KindObject, Object: obj}, nil
	}
	for {
		p.skipSpace()
		if p.pos >= len(p.data) || p.data[p.pos] != '"' {
			return Value{}, newError(CodeSyntaxInvalid, path)
		}
		key, err := p.str(path)
		if err != nil {
			return Value{}, err
		}
		child := append(append([]string(nil), path...), key)
		if _, dup := obj[key]; dup {
			return Value{}, newError(CodeDuplicateKey, child)
		}
		p.skipSpace()
		if p.pos >= len(p.data) || p.data[p.pos] != ':' {
			return Value{}, newError(CodeSyntaxInvalid, child)
		}
		p.pos++
		p.skipSpace()
		member, err := p.value(child, depth+1)
		if err != nil {
			return Value{}, err
		}
		obj[key] = member
		p.skipSpace()
		if p.pos >= len(p.data) {
			return Value{}, newError(CodeSyntaxInvalid, path)
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return Value{Kind: KindObject, Object: obj}, nil
		default:
			return Value{}, newError(CodeSyntaxInvalid, path)
		}
	}
}

func (p *parser) array(path []string, depth int) (Value, error) {
	p.pos++ // [
	items := []Value{}
	p.skipSpace()
	if p.pos < len(p.data) && p.data[p.pos] == ']' {
		p.pos++
		return Value{Kind: KindArray, Array: items}, nil
	}
	for {
		p.skipSpace()
		item, err := p.value(append(append([]string(nil), path...), strconv.Itoa(len(items))), depth+1)
		if err != nil {
			return Value{}, err
		}
		items = append(items, item)
		p.skipSpace()
		if p.pos >= len(p.data) {
			return Value{}, newError(CodeSyntaxInvalid, path)
		}
		switch p.data[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return Value{Kind: KindArray, Array: items}, nil
		default:
			return Value{}, newError(CodeSyntaxInvalid, path)
		}
	}
}

// integer accepts only -?(0|[1-9][0-9]*) that fits an int64: no fraction, no
// exponent, no leading zero, no negative zero.
func (p *parser) integer(path []string) (Value, error) {
	start := p.pos
	if p.data[p.pos] == '-' {
		p.pos++
	}
	digits := p.pos
	for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
		p.pos++
	}
	if p.pos == digits {
		return Value{}, newError(CodeSyntaxInvalid, path)
	}
	if p.pos < len(p.data) {
		switch p.data[p.pos] {
		case '.', 'e', 'E':
			return Value{}, newError(CodeIntegerTokenInvalid, path)
		}
	}
	token := string(p.data[start:p.pos])
	if (p.pos-digits > 1 && p.data[digits] == '0') || token == "-0" {
		return Value{}, newError(CodeIntegerTokenInvalid, path)
	}
	n, err := strconv.ParseInt(token, 10, 64)
	if err != nil {
		return Value{}, newError(CodeIntegerTokenInvalid, path)
	}
	return Value{Kind: KindInteger, Int: n}, nil
}

func (p *parser) str(path []string) (string, error) {
	p.pos++ // opening quote
	var b strings.Builder
	for {
		if p.pos >= len(p.data) {
			return "", newError(CodeSyntaxInvalid, path)
		}
		c := p.data[p.pos]
		switch {
		case c == '"':
			p.pos++
			return b.String(), nil
		case c == 0:
			return "", newError(CodeNULForbidden, path)
		case c < 0x20:
			return "", newError(CodeSyntaxInvalid, path)
		case c == '\\':
			r, err := p.escape(path)
			if err != nil {
				return "", err
			}
			b.WriteRune(r)
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
}

func (p *parser) escape(path []string) (rune, error) {
	p.pos++ // backslash
	if p.pos >= len(p.data) {
		return 0, newError(CodeSyntaxInvalid, path)
	}
	c := p.data[p.pos]
	p.pos++
	switch c {
	case '"', '\\', '/':
		return rune(c), nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		r, ok := p.hex4()
		if !ok {
			return 0, newError(CodeSyntaxInvalid, path)
		}
		if r == 0 {
			return 0, newError(CodeNULForbidden, path)
		}
		switch {
		case r >= 0xD800 && r <= 0xDBFF:
			// A high surrogate must be followed by an escaped low surrogate.
			if p.pos+1 < len(p.data) && p.data[p.pos] == '\\' && p.data[p.pos+1] == 'u' {
				save := p.pos
				p.pos += 2
				low, ok := p.hex4()
				if ok && low >= 0xDC00 && low <= 0xDFFF {
					return 0x10000 + (r-0xD800)<<10 + (low - 0xDC00), nil
				}
				p.pos = save
			}
			return 0, newError(CodeUTF8Invalid, path)
		case r >= 0xDC00 && r <= 0xDFFF:
			return 0, newError(CodeUTF8Invalid, path)
		}
		return r, nil
	}
	return 0, newError(CodeSyntaxInvalid, path)
}

func (p *parser) hex4() (rune, bool) {
	if p.pos+4 > len(p.data) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(p.data[p.pos:p.pos+4]), 16, 32)
	if err != nil {
		return 0, false
	}
	p.pos += 4
	return rune(n), true
}

// Canonical returns the one byte encoding of v: UTF-8 without BOM, no whitespace,
// object keys in ascending UTF-8 byte order, integers in decimal, and a string
// escaped only for the quote, the backslash and U+0001..U+001F (lower-case \u00xx,
// never \n or \t). It does not HTML-escape and writes U+2028/U+2029 literally.
// The same value always yields the same bytes.
func Canonical(v Value) []byte {
	var b []byte
	return appendCanonical(b, v)
}

func appendCanonical(b []byte, v Value) []byte {
	switch v.Kind {
	case KindNull:
		return append(b, "null"...)
	case KindBool:
		if v.Bool {
			return append(b, "true"...)
		}
		return append(b, "false"...)
	case KindInteger:
		return strconv.AppendInt(b, v.Int, 10)
	case KindString:
		return appendString(b, v.String)
	case KindArray:
		b = append(b, '[')
		for i, item := range v.Array {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendCanonical(b, item)
		}
		return append(b, ']')
	case KindObject:
		keys := make([]string, 0, len(v.Object))
		for k := range v.Object {
			keys = append(keys, k)
		}
		sort.Strings(keys) // Go string order is byte order, i.e. UTF-8 order
		b = append(b, '{')
		for i, k := range keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendString(b, k)
			b = append(b, ':')
			b = appendCanonical(b, v.Object[k])
		}
		return append(b, '}')
	}
	panic(fmt.Sprintf("canonical: unknown value kind %d", v.Kind))
}

const hexDigits = "0123456789abcdef"

func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}
