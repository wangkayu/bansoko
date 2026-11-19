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
	ReleaseLinkVariable  = "release.link"
	BGMImageVariable     = "bgm.image"
	TMDBImageVariable    = "tmdb.image"
	AniBTImageVariable   = "anibt.image"
	MikanImageVariable   = "mikan.image"
)

var (
	LinkVariables  = []string{BGMLinkVariable, TMDBLinkVariable, AniBTLinkVariable, MikanLinkVariable, DownloadLinkVariable, ReleaseLinkVariable}
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
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return ""
	}
	for key := range query {
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
	case AniBTLinkVariable, MikanLinkVariable, DownloadLinkVariable, ReleaseLinkVariable:
		raw := strings.TrimSpace(FormatFact(mustLookupFact(facts, name)))
		safe := SafeExternalURL(raw, false)
		if safe == "" {
			return ""
		}
		parsed, _ := url.Parse(safe)
		switch name {
		case AniBTLinkVariable:
			if !strings.EqualFold(parsed.Hostname(), "anibt.net") || !strings.HasPrefix(parsed.Path, "/release/") || strings.TrimPrefix(parsed.Path, "/release/") == "" {
				return ""
			}
		case MikanLinkVariable:
			if (!strings.EqualFold(parsed.Hostname(), "mikanani.me") && !strings.EqualFold(parsed.Hostname(), "mikanime.tv")) || !strings.HasPrefix(parsed.Path, "/Home/Episode/") || strings.TrimPrefix(parsed.Path, "/Home/Episode/") == "" {
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

// SemanticAttachments returns the semantic content of the message a template
// renders. It is a compatibility wrapper over the single render result:
// RenderMessage already collects the image directive and the body links it
// resolved, so a caller that has the Message should read Message.Attachments
// instead of rendering twice.
func SemanticAttachments(trigger, titleTemplate, bodyTemplate string, facts map[string]any) (Attachments, bool) {
	message, ok := RenderMessage(trigger, titleTemplate, bodyTemplate, facts)
	if !ok {
		return Attachments{}, false
	}
	return message.Attachments, true
}

// imageAttachment returns the message-level image an expression requests. Only
// a top-level image directive has an effect; an expression that nests one in
// another function never produces an attachment (the renderer reports that
// form instead).
func imageAttachment(expression templateExpression) (Attachments, bool) {
	switch node := expression.(type) {
	case variableExpression:
		switch {
		case node.name == ImageVariable:
			return Attachments{Image: true}, true
		case isImageVariable(node.name):
			return Attachments{Image: true, ImageSource: strings.TrimSuffix(node.name, ".image")}, true
		}
	case callExpression:
		if node.name != ImageVariable || len(node.args) != 1 {
			return Attachments{}, false
		}
		attachment := Attachments{Image: true}
		switch target := node.args[0].(type) {
		case variableExpression:
			attachment.ImageSource = strings.TrimSuffix(target.name, ".image")
		case literalExpression:
			attachment.ImageSource = "custom"
			attachment.ImageURL = SafeExternalImageURL(target.value)
		}
		return attachment, true
	}
	return Attachments{}, false
}

// mergeFirstImage keeps the first image a template requests in rendering order.
// Channels carry one message image, so later directives add nothing.
func mergeFirstImage(dst *Attachments, src Attachments) {
	if dst.Image || !src.Image {
		return
	}
	dst.Image, dst.ImageSource, dst.ImageURL = src.Image, src.ImageSource, src.ImageURL
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

// semanticSourceSegments retains standalone image directives in source order
// without rendering their lines as text. Every segment keeps its byte offset.
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
		line := source[lineStart:lineEnd]
		image, ok := semanticDirectiveExpression(line)
		if !ok {
			lineStart = lineEnd
			continue
		}
		if lineStart > segmentStart {
			segments = append(segments, templateSourceSegment{text: source[segmentStart:lineStart], originalStart: segmentStart})
		}
		if !hasNewline {
			for index := len(segments) - 1; index >= 0; index-- {
				if last := &segments[index]; last.image == nil {
					last.text = strings.TrimSuffix(strings.TrimSuffix(last.text, "\n"), "\r")
					break
				}
			}
		}
		segments = append(segments, templateSourceSegment{text: line, originalStart: lineStart, image: image})
		segmentStart = lineEnd
		lineStart = lineEnd
	}
	if segmentStart < len(source) {
		segments = append(segments, templateSourceSegment{text: source[segmentStart:], originalStart: segmentStart})
	}
	return segments
}

// semanticDirectiveExpression parses a line that contains exactly one
// message-level attachment directive. The whole trimmed line must be that
// expression.
func semanticDirectiveExpression(line string) (templateExpression, bool) {
	line = strings.TrimSpace(line)
	if len(line) < 4 || !strings.HasPrefix(line, "{{") || !strings.HasSuffix(line, "}}") {
		return nil, false
	}
	expression, err := parseExpression(strings.TrimSpace(line[2 : len(line)-2]))
	if err != nil || !isAttachmentExpression(expression) {
		return nil, false
	}
	return expression, true
}

// isAttachmentExpression reports whether an expression produces a message-level
// attachment rather than text. Images are message-level because channels take
// them outside the text (photo, post image); links render in place.
func isAttachmentExpression(expression templateExpression) bool {
	switch node := expression.(type) {
	case variableExpression:
		return node.name == ImageVariable || isImageVariable(node.name)
	case callExpression:
		return node.name == ImageVariable
	default:
		return false
	}
}
