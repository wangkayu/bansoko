package notification

import (
	"net/url"
	"regexp"
	"strings"
)

// Semantic variables are directives, not text facts. They never read a URL
// from the template payload: the sender resolves an image from trusted server
// state and links are derived from the frozen provider identities below.
const (
	ImageVariable = "image"
	LinkVariable  = "link"
)

// Link is one channel-neutral external metadata link.
type Link struct {
	Provider string
	Label    string
	URL      string
}

// Attachments is the channel-neutral semantic content requested by a template.
// Image is deliberately only a request: resolving its source is an application
// concern and may produce a provider image or the built-in placeholder.
type Attachments struct {
	Image bool
	Link  bool
	Links []Link
}

var providerWorkIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// SemanticAttachments returns the attachments requested by title/body. Link
// URLs are constructed only from validated, frozen provider IDs; arbitrary
// payload strings and application localhost URLs are never promoted to links.
func SemanticAttachments(trigger, titleTemplate, bodyTemplate string, facts map[string]any) (Attachments, bool) {
	if _, ok := knownVariables(trigger); !ok {
		return Attachments{}, false
	}
	refs := TemplateReferences(titleTemplate, bodyTemplate)
	attachments := Attachments{Image: refs[ImageVariable], Link: refs[LinkVariable]}
	if !attachments.Link {
		return attachments, true
	}
	if id := providerWorkID(facts, "bgm.id"); id != "" {
		attachments.Links = append(attachments.Links, Link{
			Provider: "bgm", Label: "Bangumi", URL: "https://bgm.tv/subject/" + id,
		})
	}
	kind := strings.TrimSpace(FormatFact(mustLookupFact(facts, "tmdb.kind")))
	if id := providerWorkID(facts, "tmdb.id"); id != "" && (kind == "tv" || kind == "movie") {
		attachments.Links = append(attachments.Links, Link{
			Provider: "tmdb", Label: "TMDB",
			URL: (&url.URL{Scheme: "https", Host: "www.themoviedb.org", Path: "/" + kind + "/" + id}).String(),
		})
	}
	return attachments, true
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

// semanticSourceSegments removes standalone attachment directives while
// retaining each remaining segment's original byte offset. Rendering and
// diagnostics therefore use the same source positions as the editor. A final
// directive consumes its preceding newline; other directives consume their
// own following newline, so defaults do not leave blank attachment lines.
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
	name := strings.TrimSpace(line[2 : len(line)-2])
	return name == ImageVariable || name == LinkVariable
}
