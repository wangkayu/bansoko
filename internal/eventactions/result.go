package eventactions

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxResultBytes = 2 << 10

type scriptResult struct {
	summary    string
	httpStatus int
}

// Keep draining after overflow so reporting cannot break the script's writes.
type resultCapture struct {
	body     []byte
	overflow bool
}

func (c *resultCapture) Write(p []byte) (int, error) {
	if !c.overflow {
		if len(p) > maxResultBytes-len(c.body) {
			c.overflow = true
			c.body = nil
		} else {
			c.body = append(c.body, p...)
		}
	}
	return len(p), nil
}

func (c *resultCapture) result() scriptResult {
	if c == nil || c.overflow || !utf8.Valid(c.body) {
		return scriptResult{}
	}
	dec := json.NewDecoder(bytes.NewReader(c.body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return scriptResult{}
	}
	var out scriptResult
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok || seen[key] {
			return scriptResult{}
		}
		seen[key] = true
		switch key {
		case "version":
			var v int
			if dec.Decode(&v) != nil || v != 1 {
				return scriptResult{}
			}
		case "summary":
			if dec.Decode(&out.summary) != nil || strings.TrimSpace(out.summary) == "" || utf8.RuneCountInString(out.summary) > 240 || strings.ContainsFunc(out.summary, func(r rune) bool {
				return unicode.IsControl(r) || r == ' ' || r == ' '
			}) {
				return scriptResult{}
			}
		case "http_status":
			if dec.Decode(&out.httpStatus) != nil || out.httpStatus < 100 || out.httpStatus > 599 {
				return scriptResult{}
			}
		default:
			return scriptResult{}
		}
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') || !seen["version"] || !seen["summary"] {
		return scriptResult{}
	}
	if _, err := dec.Token(); err != io.EOF {
		return scriptResult{}
	}
	return out
}
