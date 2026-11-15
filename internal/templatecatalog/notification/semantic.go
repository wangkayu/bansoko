package notification

import (
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Semantic directives produce channel-native attachments rather than message
// text. Provider variables contain no user-supplied URL: links are derived from
// frozen identities and images are resolved by the server at delivery time.
const (
	ImageVariable        = "image"
	BGMLinkVariable      = "bgm.link"
	TMDBLinkVariable     = "tmdb.link"
	AniBTLinkVariable    = "anibt.link"
	MikanLinkVariable    = "mikan.link"
	DownloadLinkVariable = "download.link"
	BGMImageVariable     = "bgm.image"
	TMDBImageVariable    = "tmdb.image"
	AniBTImageVariable   = "anibt.image"
	MikanImageVariable   = "mikan.image"
)

var (
	LinkVariables  = []string{BGMLinkVariable, TMDBLinkVariable, AniBTLinkVariable, MikanLinkVariable, DownloadLinkVariable}
	ImageVariables = []string{BGMImageVariable, TMDBImageVariable, AniBTImageVariable, MikanImageVariable}
)

// Link is one channel-neutral external link.
type Link struct {
	Provider string
	Label    string
	URL      string
}

// Attachments is the channel-neutral semantic content requested by a template.
// ImageSource is empty for automatic selection, a provider name for an explicit
// provider variable, or "custom" with ImageURL for a literal HTTPS image.
type Attachments struct {
	Image       bool
	ImageSource string
	ImageURL    string
	Links       []Link
}

var providerWorkIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// SafeExternalURL validates and canonicalizes a link target. Credential-bearing
// query parameters fail closed instead of being partially redacted into a URL
// that still discloses the surrounding private locator.
func SafeExternalURL(raw string, httpsOnly bool) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 || strings.ContainsAny(raw, "\r\n") || strings.Contains(raw, "{{") || strings.Contains(raw, "}}") {
		return ""
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return ""
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Hostname() == "" || parsed.Opaque != "" {
		return ""
	}
	if parsed.Scheme != "https" && (!(!httpsOnly && parsed.Scheme == "http")) {
		return ""
	}
	for key := range parsed.Query() {
		normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", ""))
		switch normalized {
		case "passkey", "token", "secret", "password", "credential", "credentials", "auth", "authorization", "key", "apikey", "signature", "sig", "cookie", "rsskey":
			return ""
		}
	}
	return parsed.String()
}

// SafeExternalImageURL additionally rejects literal local/internal targets.
// The delivery HTTP transport repeats the check after DNS resolution and on
// redirects, so a public hostname cannot be used to reach a private address.
func SafeExternalImageURL(raw string) string {
	safe := SafeExternalURL(raw, true)
	if safe == "" {
		return ""
	}
	parsed, _ := url.Parse(safe)
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".home.arpa") {
		return ""
	}
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsUnspecified() || address.IsMulticast() {
			return ""
		}
	}
	return safe
}

func isLinkVariable(name string) bool {
	for _, candidate := range LinkVariables {
		if name == candidate {
			return true
		}
	}
	return false
}

func isImageVariable(name string) bool {
	for _, candidate := range ImageVariables {
		if name == candidate {
			return true
		}
	}
	return false
}

// LinkValue resolves a safe link variable for direct text rendering and for
// link(...). Provider-specific values are validated again at this boundary.
func LinkValue(facts map[string]any, name string) string {
	switch name {
	case BGMLinkVariable:
		if id := providerWorkID(facts, "bgm.id"); id != "" {
			return "https://bgm.tv/subject/" + id
		}
	case TMDBLinkVariable:
		kind := strings.TrimSpace(FormatFact(mustLookupFact(facts, "tmdb.kind")))
		if id := providerWorkID(facts, "tmdb.id"); id != "" && (kind == "tv" || kind == "movie") {
			return (&url.URL{Scheme: "https", Host: "www.themoviedb.org", Path: "/" + kind + "/" + id}).String()
		}
	case AniBTLinkVariable, MikanLinkVariable, DownloadLinkVariable:
		raw := strings.TrimSpace(FormatFact(mustLookupFact(facts, name)))
		safe := SafeExternalURL(raw, false)
		if safe == "" {
			return ""
		}
		parsed, _ := url.Parse(safe)
		switch name {
		case AniBTLinkVariable:
			if !strings.EqualFold(parsed.Hostname(), "anibt.net") || !strings.HasPrefix(parsed.Path, "/release/") {
				return ""
			}
		case MikanLinkVariable:
			if !strings.EqualFold(parsed.Hostname(), "mikanani.me") || !strings.HasPrefix(parsed.Path, "/Home/Episode/") {
				return ""
			}
		}
		return safe
	}
	return ""
}

func providerWorkID(facts map[string]any, name string) string {
	value := strings.TrimSpace(FormatFact(mustLookupFact(facts, name)))
	if !providerWorkIDPattern.MatchString(value) {
		return ""
	}
	return value
}

func mustLookupFact(facts map[string]any, name string) any {
	value, _ := LookupFact(facts, name)
	return value
}

// SemanticAttachments returns attachments in template source order, title then
// body. An unavailable target simply emits no attachment; text rendering still
// reports the same empty-variable warning where appropriate.
func SemanticAttachments(trigger, titleTemplate, bodyTemplate string, facts map[string]any) (Attachments, bool) {
	if _, ok := knownVariables(trigger); !ok {
		return Attachments{}, false
	}
	var attachments Attachments
	for _, source := range []string{titleTemplate, bodyTemplate} {
		for _, expression := range sourceExpressions(source) {
			switch node := expression.(type) {
			case variableExpression:
				if node.name == ImageVariable && !attachments.Image {
					attachments.Image = true
				}
			case callExpression:
				switch node.name {
				case ImageVariable:
					if attachments.Image || len(node.args) != 1 {
						continue
					}
					attachments.Image = true
					switch target := node.args[0].(type) {
					case variableExpression:
						attachments.ImageSource = strings.TrimSuffix(target.name, ".image")
					case literalExpression:
						attachments.ImageSource = "custom"
						attachments.ImageURL = SafeExternalImageURL(target.value)
					}
				case "link":
					if len(node.args) != 2 {
						continue
					}
					label, ok := node.args[1].(literalExpression)
					if !ok || !validLinkLabel(label.value) {
						continue
					}
					provider, target := "custom", ""
					switch value := node.args[0].(type) {
					case variableExpression:
						provider = strings.TrimSuffix(value.name, ".link")
						target = LinkValue(facts, value.name)
					case literalExpression:
						target = SafeExternalURL(value.value, false)
					}
					if target != "" {
						attachments.Links = append(attachments.Links, Link{Provider: provider, Label: strings.TrimSpace(label.value), URL: target})
					}
				}
			}
		}
	}
	return attachments, true
}

func validLinkLabel(label string) bool {
	label = strings.TrimSpace(label)
	if label == "" || utf8.RuneCountInString(label) > 128 || strings.Contains(label, "{{") || strings.Contains(label, "}}") {
		return false
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func sourceExpressions(source string) []templateExpression {
	var expressions []templateExpression
	remaining := source
	for len(remaining) > 0 {
		start := strings.Index(remaining, "{{")
		if start < 0 {
			break
		}
		afterOpen := remaining[start+2:]
		end := strings.Index(afterOpen, "}}")
		if end < 0 {
			break
		}
		if expression, err := parseExpression(strings.TrimSpace(afterOpen[:end])); err == nil {
			expressions = append(expressions, expression)
		}
		remaining = afterOpen[end+2:]
	}
	return expressions
}

// semanticSourceSegments removes standalone attachment directives while
// retaining every other segment's original byte offset. Validation of the
// removed expressions happens separately in renderText.
func semanticSourceSegments(source string) []templateSourceSegment {
	if !strings.Contains(source, "{{") {
		return []templateSourceSegment{{text: source}}
	}
	segments := []templateSourceSegment{}
	segmentStart := 0
	for lineStart := 0; lineStart < len(source); {
		lineEnd := strings.IndexByte(source[lineStart:], '\n')
		hasNewline := lineEnd >= 0
		if hasNewline {
			lineEnd += lineStart + 1
		} else {
			lineEnd = len(source)
		}
		if !isSemanticDirectiveLine(source[lineStart:lineEnd]) {
			lineStart = lineEnd
			continue
		}
		if lineStart > segmentStart {
			segments = append(segments, templateSourceSegment{text: source[segmentStart:lineStart], originalStart: segmentStart})
		}
		if !hasNewline && len(segments) > 0 {
			last := &segments[len(segments)-1]
			last.text = strings.TrimSuffix(last.text, "\n")
			last.text = strings.TrimSuffix(last.text, "\r")
		}
		segmentStart = lineEnd
		lineStart = lineEnd
	}
	if segmentStart < len(source) {
		segments = append(segments, templateSourceSegment{text: source[segmentStart:], originalStart: segmentStart})
	}
	return segments
}

func isSemanticDirectiveLine(line string) bool {
	line = strings.TrimSpace(line)
	if len(line) < 4 || !strings.HasPrefix(line, "{{") || !strings.HasSuffix(line, "}}") {
		return false
	}
	expression, err := parseExpression(strings.TrimSpace(line[2 : len(line)-2]))
	if err != nil {
		return false
	}
	switch node := expression.(type) {
	case variableExpression:
		return node.name == ImageVariable
	case callExpression:
		return node.name == ImageVariable || node.name == "link"
	default:
		return false
	}
}
