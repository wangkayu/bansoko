// Package notifypresent turns the channel-neutral notification message into the
// exact presentation each supported channel receives. Delivery request builders
// and the side-effect-free template preview both call these functions, so the
// preview cannot drift from what is actually sent. Nothing here reads secrets,
// touches the database, or performs network I/O.
package notifypresent

import (
	"html"
	"strings"
	"unicode/utf16"

	"bansoko/internal/templatecatalog/notification"
)

// Image is the message-level image a channel may show. A requested image with
// no URL (the built-in placeholder) still counts as an image for Telegram but
// has no link for Feishu.
type Image struct {
	Source      string
	URL         string
	Placeholder bool
}

// Message is the channel-neutral input. Parts is the body in template order;
// Body is the same content as one plain-text string.
type Message struct {
	Title string
	Body  string
	Parts []notification.BodyPart
	Image *Image
	Links []notification.Link
}

// Presentation is every channel's rendering of one Message.
type Presentation struct {
	Telegram TelegramPresentation
	Feishu   FeishuPresentation
	Webhook  WebhookPresentation
}

// Build renders the channel-neutral message for every supported channel.
func Build(message Message) Presentation {
	return Presentation{
		Telegram: Telegram(message),
		Feishu:   Feishu(message),
		Webhook:  Webhook(message),
	}
}

// ---------------------------------------------------------------------------
// Telegram
// ---------------------------------------------------------------------------

// Telegram counts UTF-16 code units. A picture message's caption is limited to
// TelegramCaptionLimit units; longer text is sent as a regular message and the
// picture is dropped. The text message is cut to TelegramTextLimit units.
const (
	TelegramCaptionLimit = 1024
	TelegramTextLimit    = 4096

	// TelegramModePhoto and TelegramModeMessage are the two representations.
	TelegramModePhoto   = "sendPhoto"
	TelegramModeMessage = "sendMessage"
)

// TelegramPresentation is the text Telegram receives and the representation
// that carries it. Text is the final HTML: the caption for sendPhoto or the
// message text for sendMessage.
type TelegramPresentation struct {
	Mode  string
	Text  string
	Title string
	Parts []notification.BodyPart
	// TextUnits is the next payload's UTF-16 length after fitting.
	TextUnits int
	// ImageOmitted reports that an image was requested but the caption is over
	// the photo limit, so the message is sent without it.
	ImageOmitted bool
	// Truncated reports that the text exceeded the message limit and was cut.
	Truncated bool
}

// Telegram renders the message for Telegram. The decision and the final HTML
// are the same ones the delivery renderer uses.
func Telegram(message Message) TelegramPresentation {
	full := TelegramHTML(message)
	fullUnits := TelegramTextUnits(full)
	if message.Image != nil && fullUnits <= TelegramCaptionLimit {
		return TelegramPresentation{Mode: TelegramModePhoto, Text: full, TextUnits: fullUnits, Title: message.Title, Parts: telegramDisplayParts(message)}
	}
	truncated := fullUnits > TelegramTextLimit
	text := full
	if truncated {
		message = fitTelegram(message, TelegramTextLimit)
		text = TelegramHTML(message)
	}
	return TelegramPresentation{
		Mode:         TelegramModeMessage,
		Text:         text,
		Title:        message.Title,
		Parts:        telegramDisplayParts(message),
		TextUnits:    TelegramTextUnits(text),
		ImageOmitted: message.Image != nil,
		Truncated:    truncated,
	}
}

func telegramDisplayParts(message Message) []notification.BodyPart {
	if len(message.Parts) == 0 && message.Body != "" {
		return []notification.BodyPart{{Text: message.Body}}
	}
	return message.Parts
}

// TelegramHTML renders the title in bold and the body with inline links, the
// document Telegram receives.
func TelegramHTML(message Message) string {
	var builder strings.Builder
	if message.Title != "" {
		builder.WriteString("<b>")
		builder.WriteString(html.EscapeString(message.Title))
		builder.WriteString("</b>")
	}
	body := telegramBodyHTML(message)
	if body != "" {
		if builder.Len() > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(body)
	}
	return builder.String()
}

// telegramBodyHTML renders body parts in template order: links stay inline,
// text is escaped. Without parts (a text-only caller) the plain body is used.
func telegramBodyHTML(message Message) string {
	if len(message.Parts) == 0 {
		return html.EscapeString(message.Body)
	}
	var builder strings.Builder
	for _, part := range message.Parts {
		builder.WriteString(telegramPartHTML(part))
	}
	return builder.String()
}

func telegramPartHTML(part notification.BodyPart) string {
	if part.Link == nil {
		return html.EscapeString(part.Text)
	}
	var builder strings.Builder
	builder.WriteString(`<a href="`)
	builder.WriteString(html.EscapeString(part.Link.URL))
	builder.WriteString(`">`)
	builder.WriteString(html.EscapeString(part.Link.Label))
	builder.WriteString("</a>")
	return builder.String()
}

// FitTelegram returns the message HTML fitted to limit UTF-16 units. Truncation
// keeps every link and spends the remaining budget on text in template order;
// the pre-truncation decision is the caller's (see Telegram).
func FitTelegram(message Message, limit int) string {
	full := TelegramHTML(message)
	if TelegramTextUnits(full) <= limit {
		return full
	}
	return TelegramHTML(fitTelegram(message, limit))
}

func fitTelegram(message Message, limit int) Message {
	message.Title = truncateTelegramHTMLText(message.Title, min(256, limit/4))
	reserved := TelegramTextUnits(html.EscapeString(message.Title)) + len("<b></b>") + 2
	budget := max(0, limit-reserved)
	if len(message.Parts) == 0 {
		message.Body = truncateTelegramHTMLText(message.Body, budget)
		return message
	}
	message.Parts = truncateTelegramParts(message.Parts, budget)
	message.Body = notification.PlainText(message.Parts)
	return message
}

// truncateTelegramParts keeps every link and spends the remaining budget on
// text in template order, so an overflowing body still carries its links. A
// text part that does not fit ends the body with an ellipsis.
func truncateTelegramParts(parts []notification.BodyPart, limit int) []notification.BodyPart {
	linkWidth := 0
	for _, part := range parts {
		if part.Link != nil {
			linkWidth += TelegramTextUnits(telegramPartHTML(part))
		}
	}
	if linkWidth >= limit {
		return truncateTelegramPartsPrefix(parts, limit)
	}
	textBudget := limit - linkWidth
	kept := make([]notification.BodyPart, 0, len(parts))
	truncated := false
	for _, part := range parts {
		if part.Link != nil {
			kept = append(kept, part)
			continue
		}
		if truncated {
			continue
		}
		if width := TelegramTextUnits(telegramPartHTML(part)); width <= textBudget {
			kept = append(kept, part)
			textBudget -= width
			continue
		}
		text := truncateTelegramHTMLText(part.Text, textBudget-1)
		if text != "" {
			kept = append(kept, notification.BodyPart{Text: text})
		}
		truncated = true
	}
	return kept
}

// truncateTelegramPartsPrefix fills the budget in template order and drops the
// rest, closing with an ellipsis; it is the last resort when the links alone
// exceed the limit.
func truncateTelegramPartsPrefix(parts []notification.BodyPart, limit int) []notification.BodyPart {
	kept := make([]notification.BodyPart, 0, len(parts))
	remaining := limit
	for _, part := range parts {
		width := TelegramTextUnits(telegramPartHTML(part))
		if width <= remaining {
			kept = append(kept, part)
			remaining -= width
			continue
		}
		if part.Link != nil {
			return append(kept, notification.BodyPart{Text: "…"})
		}
		text := truncateTelegramHTMLText(part.Text, remaining-1)
		if text != "" {
			kept = append(kept, notification.BodyPart{Text: text})
		}
		return kept
	}
	return kept
}

// truncateTelegramHTMLText budgets the escaped representation that Telegram
// actually receives, not the shorter plain text before HTML escaping.
func truncateTelegramHTMLText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if TelegramTextUnits(html.EscapeString(value)) <= limit {
		return value
	}
	var builder strings.Builder
	units := 0
	for _, r := range value {
		width := TelegramTextUnits(html.EscapeString(string(r)))
		if units+width+1 > limit {
			break
		}
		builder.WriteRune(r)
		units += width
	}
	builder.WriteRune('…')
	return builder.String()
}

// TelegramTextUnits counts UTF-16 code units, the unit Telegram limits.
func TelegramTextUnits(value string) int {
	return len(utf16.Encode([]rune(value)))
}

// ---------------------------------------------------------------------------
// Feishu
// ---------------------------------------------------------------------------

// FeishuElement is one native post element: literal text or a link.
type FeishuElement struct {
	Tag  string `json:"tag"`
	Text string `json:"text"`
	Href string `json:"href,omitempty"`
}

// FeishuImage describes the image link Feishu appends as its own paragraph.
type FeishuImage struct {
	Label string
	URL   string
}

// FeishuPresentation is the title, the post paragraphs, and the image link.
// Content is the exact content Feishu receives, including the image paragraph.
type FeishuPresentation struct {
	Title   string
	Content [][]FeishuElement
	// Image is the appended image link; nil when no image URL is available.
	Image *FeishuImage
	// ImageOmitted reports that an image was requested but has no URL, so
	// Feishu shows none.
	ImageOmitted bool
}

// Feishu renders the message as a custom-bot post. Feishu cannot upload image
// bytes or reference an image URL natively, so an image is one final link
// paragraph.
func Feishu(message Message) FeishuPresentation {
	presentation := FeishuPresentation{Title: message.Title, Content: feishuPostContent(message)}
	if message.Image == nil {
		return presentation
	}
	if message.Image.URL == "" {
		presentation.ImageOmitted = true
		return presentation
	}
	presentation.Image = &FeishuImage{Label: FeishuImageLabel(message.Image.Source), URL: message.Image.URL}
	return presentation
}

// FeishuImageLabel labels an image link by source.
func FeishuImageLabel(source string) string {
	switch source {
	case "mikan":
		return "Mikan"
	case "anibt":
		return "AniBT"
	case "bgm":
		return "Bangumi"
	case "tmdb":
		return "TMDB"
	default:
		return "Image"
	}
}

// feishuPostContent renders the body as paragraphs: a source line is one
// paragraph, and links keep their place inside the line. An empty line stays an
// empty paragraph. Without parts the plain body is split the same way. The
// image link, when available, is its own final paragraph.
func feishuPostContent(message Message) [][]FeishuElement {
	lines := make([][]FeishuElement, 0, len(message.Links)+2)
	current := []FeishuElement{}
	flush := func() {
		if len(current) == 0 {
			current = append(current, FeishuElement{Tag: "text", Text: " "})
		}
		lines = append(lines, current)
		current = nil
	}
	if len(message.Parts) == 0 {
		for _, line := range strings.Split(message.Body, "\n") {
			if line != "" {
				current = append(current, FeishuElement{Tag: "text", Text: line})
			}
			flush()
		}
	} else {
		for _, part := range message.Parts {
			if part.Link != nil {
				current = append(current, FeishuElement{Tag: "a", Text: part.Link.Label, Href: part.Link.URL})
				continue
			}
			chunks := strings.Split(part.Text, "\n")
			for index, chunk := range chunks {
				if index > 0 {
					flush()
				}
				if chunk != "" {
					current = append(current, FeishuElement{Tag: "text", Text: chunk})
				}
			}
		}
		flush()
	}
	if message.Image != nil && message.Image.URL != "" {
		lines = append(lines, []FeishuElement{{Tag: "a", Text: FeishuImageLabel(message.Image.Source), Href: message.Image.URL}})
	}
	return lines
}

// ---------------------------------------------------------------------------
// Webhook
// ---------------------------------------------------------------------------

// WebhookImage is the typed image of the webhook message.
type WebhookImage struct {
	Source      string `json:"source"`
	URL         string `json:"url,omitempty"`
	Placeholder bool   `json:"placeholder,omitempty"`
}

// WebhookLink is one typed link of the webhook message.
type WebhookLink struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	URL      string `json:"url"`
}

// WebhookMessage is the message member of the webhook document.
type WebhookMessage struct {
	Title string        `json:"title"`
	Body  string        `json:"body"`
	Image *WebhookImage `json:"image,omitempty"`
	Links []WebhookLink `json:"links"`
}

// WebhookPresentation is the message member of the outbound webhook document.
// The envelope adds delivery_id, event, timestamp, attempt and payload; this
// type is only what every channel sees as the message.
type WebhookPresentation struct {
	Message WebhookMessage
}

// Webhook renders the typed webhook message.
func Webhook(message Message) WebhookPresentation {
	typed := WebhookMessage{
		Title: message.Title, Body: message.Body,
		Links: make([]WebhookLink, 0, len(message.Links)),
	}
	if message.Image != nil {
		typed.Image = &WebhookImage{Source: message.Image.Source, URL: message.Image.URL, Placeholder: message.Image.Placeholder}
	}
	for _, link := range message.Links {
		typed.Links = append(typed.Links, WebhookLink{Provider: link.Provider, Label: link.Label, URL: link.URL})
	}
	return WebhookPresentation{Message: typed}
}
