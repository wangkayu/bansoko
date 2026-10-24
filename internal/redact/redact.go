// Package redact provides the shared bounded free-text redaction policy.
package redact

import (
	"regexp"
	"sort"
	"strings"
)

// Redacted is the marker used by sanitising read projections.
const Redacted = "<redacted>"

// SecretPlaceholder is the fixed value every x-secret field of the HTTP
// contract carries for a bearer without the secrets scope (SEC-033). It is
// never a valid stored value: a write that still carries it after the owner's
// placeholder resolution is refused.
const SecretPlaceholder = "[secret]"

// Policy selects whether sensitive input is rejected or replaced.
type Policy uint8

const (
	Reject Policy = iota
	Mask
	// MaskFragments replaces only the sensitive spans with the marker and keeps
	// the surrounding text, so ordinary upstream text (release and anime titles)
	// stays readable while a URL, credential, or path inside it never persists.
	// If a span rule cannot isolate what the detector matched, the whole value
	// collapses to the marker, exactly as Mask.
	MaskFragments
)

// pathBoundary is where a Unix path can start: start of text, whitespace, or
// punctuation that introduces a value in diagnostics (quotes, '=', ':', '(',
// '<', '[', '{', ',', ';', '|', '>' and their full-width Chinese forms). A
// slash after a letter, CJK character or closing bracket ("】/Oshi no Ko",
// "and/or", "12/20") is a title or prose separator, not a path.
const pathBoundary = `(?:^|[\s\x{3000}"'` + "`" + `=:,;|<>(\[{（：「『，；])`

// pathRoots are leading segments that mark a path even without a second
// segment ("/tmp", "/data:").
const pathRoots = `(?:home|Users|root|tmp|var|opt|etc|mnt|media|srv|data|downloads|library|Library|private|Volumes|volume[0-9]*|usr|proc|dev|run|nas|share|app|config|workspace)`

var (
	credentialAssignmentPattern = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:api[_-]?key|apikey|passkey|cookie|credential|credentials|private[_-]?key|password|token|secret|authorization|access[_-]?token|client[_-]?secret|access[_-]?key|secret[_-]?key|dsn)["']?\s*[:=]`)
	bearerWordPattern           = regexp.MustCompile(`(?i)\bbearer\b`)
	absoluteURLPattern          = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://`)
	legacyURLPattern            = regexp.MustCompile(`(?i)(?:https?|postgres(?:ql)?|mysql|redis|s?ftp)://[^\s'"<>]+`)
	magnetPattern               = regexp.MustCompile(`(?i)magnet:`)
	// A Unix path starts at a pathBoundary and either has two segments
	// ("/a/b") or a known root ("/tmp"). A lone "/Word" after a boundary is
	// still prose ("Title /Alias"), so it no longer counts.
	unixPathPattern      = regexp.MustCompile(pathBoundary + `(/` + pathRoots + `\b\S*|/[^\s/]+/\S*)`)
	windowsPathPattern   = regexp.MustCompile(`(?i)[a-zA-Z]:[\\/]`)
	uncPathPattern       = regexp.MustCompile(`\\\\\S`)
	tildeRelativePattern = regexp.MustCompile(`(?:^|[^a-zA-Z0-9_])~/\S`)
	dotRelativePattern   = regexp.MustCompile(`(?:^|[^a-zA-Z0-9_])(?:\.\.?)/\S`)
	sensitiveWordPattern = regexp.MustCompile(`(?i)\b(?:token|secret|password|authorization|dsn)\b`)
)

// Apply normalizes and bounds free-form text using the shared union marker set.
// A sensitive match is reported unchanged under Reject, or collapsed to marker
// under Mask. maxRunes bounds surviving text; values below one yield empty text.
func Apply(value string, maxRunes int, policy Policy, marker string) (string, bool) {
	return apply(value, maxRunes, policy, marker, sensitive)
}

// ApplyLibraryRelative is Apply for a path the server already projected
// relative to the library (displaypath): its slashes are the library's own
// folders, not a private location, so the path markers do not apply. Every
// other marker (URL, credential, magnet, sensitive word, home-relative) still
// does, and an absolute or parent-relative value stays sensitive.
func ApplyLibraryRelative(value string, maxRunes int, policy Policy, marker string) (string, bool) {
	return apply(value, maxRunes, policy, marker, sensitiveLibraryRelative)
}

func sensitiveLibraryRelative(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") ||
		credentialAssignmentPattern.MatchString(value) ||
		bearerWordPattern.MatchString(value) ||
		absoluteURLPattern.MatchString(value) || legacyURLPattern.MatchString(value) ||
		magnetPattern.MatchString(value) || windowsPathPattern.MatchString(value) ||
		tildeRelativePattern.MatchString(value) || dotRelativePattern.MatchString(value) ||
		sensitiveWordPattern.MatchString(value) || strings.Contains(lower, "payload") ||
		strings.Contains(lower, "panic:")
}

func apply(value string, maxRunes int, policy Policy, marker string, sensitive func(string) bool) (string, bool) {
	if maxRunes < 1 {
		return "", false
	}
	value = strings.ToValidUTF8(value, "�")
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return ' '
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if sensitive(value) {
		switch policy {
		case Mask:
			return marker, true
		case MaskFragments:
			value = maskFragments(value, marker)
		default:
			return value, true
		}
		runes := []rune(value)
		if len(runes) > maxRunes {
			value = string(runes[:maxRunes])
		}
		return value, true
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]), false
	}
	return value, false
}

// Text sanitises legacy read projections with the shared mask policy.
func Text(value string, maxRunes int) string {
	safe, _ := Apply(value, maxRunes, Mask, Redacted)
	return safe
}

// fragmentRules isolate the span each detector marker covers. Capture group 1
// is the span to replace; a leading non-capturing group only anchors it.
var fragmentRules = []*regexp.Regexp{
	// Dumps and panics carry arbitrary state after the marker.
	regexp.MustCompile(`(?i)((?:payload|panic:).*)`),
	regexp.MustCompile(`(?i)(?:^|[^a-z0-9])((?:api[_-]?key|apikey|passkey|cookie|credential|credentials|private[_-]?key|password|token|secret|authorization|access[_-]?token|client[_-]?secret|access[_-]?key|secret[_-]?key|dsn)["']?\s*[:=]\s*(?:bearer\s+)?(?:"[^"]*"?|'[^']*'?|\S*))`),
	regexp.MustCompile(`(?i)\b(bearer\b(?:\s+\S+)?)`),
	regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://\S*)`),
	regexp.MustCompile(`(?i)(magnet:\S*)`),
	unixPathPattern,
	regexp.MustCompile(`(?i)(/(?:home|users)/\S*)`),
	regexp.MustCompile(`([a-zA-Z]:[\\/]\S*)`),
	regexp.MustCompile(`(\\\\\S*)`),
	regexp.MustCompile(`(?:^|[^a-zA-Z0-9_])(~/\S*)`),
	regexp.MustCompile(`(?:^|[^a-zA-Z0-9_])(\.\.?/\S*)`),
	// A bare credential word takes the token after it with it.
	regexp.MustCompile(`(?i)\b((?:token|secret|password|authorization|dsn)\b(?:\s+\S+)?)`),
}

// maskFragments replaces every sensitive span with marker. When the masked
// text still trips the detector (a shape no span rule isolates), the whole
// value collapses to marker so MaskFragments is never weaker than Mask.
func maskFragments(value, marker string) string {
	type span struct{ start, end int }
	var spans []span
	for _, rule := range fragmentRules {
		for _, match := range rule.FindAllStringSubmatchIndex(value, -1) {
			if len(match) >= 4 && match[2] >= 0 && match[3] > match[2] {
				spans = append(spans, span{match[2], match[3]})
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	var out strings.Builder
	cursor := 0
	for _, s := range spans {
		if s.end <= cursor {
			continue
		}
		if s.start < cursor {
			s.start = cursor
		} else {
			out.WriteString(value[cursor:s.start])
			out.WriteString(marker)
		}
		cursor = s.end
	}
	out.WriteString(value[cursor:])
	masked := strings.TrimSpace(out.String())
	if masked == "" || sensitive(masked) {
		return marker
	}
	return masked
}

func sensitive(value string) bool {
	return sensitiveWith(value, unixPathPattern)
}

func sensitiveWith(value string, unixPath *regexp.Regexp) bool {
	lower := strings.ToLower(value)
	return credentialAssignmentPattern.MatchString(value) ||
		bearerWordPattern.MatchString(value) ||
		absoluteURLPattern.MatchString(value) || legacyURLPattern.MatchString(value) ||
		magnetPattern.MatchString(value) || unixPath.MatchString(value) ||
		windowsPathPattern.MatchString(value) || uncPathPattern.MatchString(value) ||
		tildeRelativePattern.MatchString(value) || dotRelativePattern.MatchString(value) ||
		sensitiveWordPattern.MatchString(value) || strings.Contains(lower, "payload") ||
		strings.Contains(lower, "panic:") || strings.Contains(lower, "/users/") ||
		strings.Contains(lower, "/home/")
}
