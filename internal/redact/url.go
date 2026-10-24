package redact

import (
	"net/url"
	"regexp"
	"strings"
)

// URLKey replaces a credential inside a URL that is shown, logged or sent.
// It is plain ASCII so the redacted URL still reads as a URL.
const URLKey = "REDACTED"

// Feed and torrent URLs of private trackers carry the user's credential inline,
// under names that differ per tracker architecture (every custom source is
// treated this way, whatever its architecture):
//
//   - query parameters: NexusPHP passkey=, UNIT3D rsskey=, and generically any
//     name that reads as a key, token, secret, password, auth or signature,
//     plus the uid of a uid+pass pair;
//   - path segments: UNIT3D /torrents/download/<id>.<key> and /rss/<id>.<key>,
//     and any segment holding a long opaque token.
//
// URL and URLsInText are the one redactor for these values. Stored values stay
// intact (fetching needs the key); only what leaves the process is redacted.

// credentialQueryNames are query parameter names that always hold a credential.
var credentialQueryNames = map[string]bool{
	"key": true, "sign": true, "sig": true, "auth": true, "pass": true, "pwd": true,
	"passkey": true, "rsskey": true, "apikey": true, "api_key": true, "authkey": true,
	"token": true, "secret": true, "password": true, "signature": true, "torrent_pass": true,
	"access_token": true, "sid": true, "session": true, "hash_key": true,
}

// credentialQueryWords mark a credential when a parameter name contains one.
var credentialQueryWords = []string{"key", "token", "secret", "pass", "auth", "sign"}

// opaqueToken is a run long enough to be a key rather than a name or an id:
// 16+ hex characters, or 24+ letters and digits that mix both.
var (
	hexRun          = regexp.MustCompile(`[0-9A-Fa-f]{16,}`)
	alnumRun        = regexp.MustCompile(`[A-Za-z0-9_-]{24,}`)
	publicInfoHashT = regexp.MustCompile(`^(?i:[0-9a-f]{40}|[0-9a-f]{64})\.torrent$`)
	urlInText       = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>]+`)
)

func credentialQueryName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if credentialQueryNames[lower] {
		return true
	}
	for _, word := range credentialQueryWords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// URL returns raw with its credentials replaced by URLKey: userinfo is
// dropped, credential query values and opaque path tokens are replaced, and the
// fragment is removed. The host, the path shape and ordinary parameters stay so
// a viewer can still tell which feed or torrent it is. A value that does not
// parse as an absolute URL is returned as URLKey: it cannot be proven clean.
func URL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return URLKey
	}
	parsed.User = nil
	parsed.Fragment = ""
	parsed.RawFragment = ""
	path := redactPath(parsed.EscapedPath())
	query := redactQuery(parsed.RawQuery)
	var out strings.Builder
	out.WriteString(strings.ToLower(parsed.Scheme))
	out.WriteString("://")
	out.WriteString(parsed.Host)
	out.WriteString(path)
	if query != "" || parsed.ForceQuery {
		out.WriteString("?")
		out.WriteString(query)
	}
	return out.String()
}

// URLsInText replaces every http(s) URL inside free text (an error message,
// a log line) with its redacted form. The surrounding text is kept.
func URLsInText(text string) string {
	if !strings.Contains(strings.ToLower(text), "http") {
		return text
	}
	return urlInText.ReplaceAllStringFunc(text, func(match string) string {
		// A URL quoted by Go's %q or url.Error ends before the closing quote,
		// which the pattern already excludes; trailing punctuation of a
		// sentence is kept outside the URL.
		trimmed := strings.TrimRight(match, ".,;:)]}")
		return URL(trimmed) + match[len(trimmed):]
	})
}

// ContainsCredential reports whether raw differs from its redacted form in a
// way that hides a credential (used by tests and guards).
func ContainsCredential(raw string) bool {
	value := strings.TrimSpace(raw)
	if value == "" {
		return false
	}
	return strings.Contains(URL(value), URLKey)
}

func redactPath(escaped string) string {
	if escaped == "" {
		return ""
	}
	segments := strings.Split(escaped, "/")
	for index, segment := range segments {
		if segment == "" || publicInfoHashT.MatchString(segment) {
			continue
		}
		segment = hexRun.ReplaceAllString(segment, URLKey)
		segment = alnumRun.ReplaceAllStringFunc(segment, func(run string) string {
			if strings.ContainsAny(run, "0123456789") && strings.IndexFunc(run, isLetter) >= 0 {
				return URLKey
			}
			return run
		})
		segments[index] = segment
	}
	return strings.Join(segments, "/")
}

func isLetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

func redactQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	hasPass := false
	for _, part := range parts {
		name, _, _ := strings.Cut(part, "=")
		if decoded, err := url.QueryUnescape(name); err == nil {
			name = decoded
		}
		if strings.EqualFold(name, "pass") || strings.EqualFold(name, "password") {
			hasPass = true
		}
	}
	for index, part := range parts {
		name, value, hasValue := strings.Cut(part, "=")
		decoded := name
		if unescaped, err := url.QueryUnescape(name); err == nil {
			decoded = unescaped
		}
		switch {
		case !hasValue || value == "":
			continue
		case credentialQueryName(decoded), hasPass && strings.EqualFold(decoded, "uid"):
			parts[index] = name + "=" + URLKey
		default:
			parts[index] = name + "=" + redactQueryValue(value)
		}
	}
	return strings.Join(parts, "&")
}

// URLError returns err with the URL of a *url.Error redacted. net/http client
// errors print their full request URL (Get "https://...?passkey=...": ...),
// so every outbound fetch wraps its client error through here before the
// error can reach a log, a stored reason or a response. The error chain is
// kept: errors.Is and errors.As still see the inner cause.
func URLError(err error) error {
	typed, ok := err.(*url.Error)
	if !ok || typed == nil {
		return err
	}
	return &url.Error{Op: typed.Op, URL: URL(typed.URL), Err: typed.Err}
}

// redactQueryValue catches an opaque token under an innocent name.
func redactQueryValue(value string) string {
	if hexRun.MatchString(value) && len(value) >= 32 {
		return URLKey
	}
	return value
}
