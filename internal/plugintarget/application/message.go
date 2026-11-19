package application

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

// outboundMessage is the channel-neutral delivery IR. Templates produce text
// plus semantic attachments; only a channel renderer turns these values into
// Telegram HTML, a multipart photo, or another provider's native shape.
type outboundMessage struct {
	Title string
	Body  string
	// Parts is the body in template order. Channels that can show inline
	// links render it; Body is the same content as one text string.
	Parts []notification.BodyPart
	Image *outboundImage
	Links []notification.Link
}

type outboundImage struct {
	Source      string
	URL         string
	Data        []byte
	ContentType string
	Placeholder bool
}

//go:embed assets/notification_placeholder.jpg
var notificationPlaceholderJPEG []byte

func placeholderNotificationImage() *outboundImage {
	return &outboundImage{
		Source: "placeholder", Data: notificationPlaceholderJPEG,
		ContentType: "image/jpeg", Placeholder: true,
	}
}

func (h *PluginDeliverHandler) prepareOutboundMessage(ctx context.Context, row deliveryRow) (outboundMessage, error) {
	if len(row.messageSnapshot) == 0 {
		// Pre-snapshot rows retain their legacy delivery path until they expire.
		title, _, err := h.resolveMessage(row)
		if err != nil {
			return outboundMessage{}, err
		}
		return h.resolveOutboundMessage(ctx, row, title)
	}
	snapshot, err := loadNotificationMessage(row.messageSnapshot, row.renderedTitle.String, row.renderedBody.String, row.renderedDigest.String)
	if err != nil {
		return outboundMessage{}, err
	}
	message := outboundMessage{
		Title: snapshot.Title, Body: notification.PlainText(snapshot.Parts),
		Parts: snapshot.Parts, Links: notification.Links(snapshot.Parts),
	}
	if snapshot.Image != nil {
		message.Image = h.resolveOutboundImage(ctx, snapshot.Image.request(), snapshot.Image.URL)
	}
	return message, nil
}

func (h *PluginDeliverHandler) resolveOutboundMessage(ctx context.Context, row deliveryRow, title string) (outboundMessage, error) {
	facts, err := notificationFactsForDelivery(row)
	if err != nil {
		return outboundMessage{Title: title}, err
	}
	// Body and its inline parts come from one render so the text and the link
	// positions can never disagree. The title keeps the resolveMessage value
	// (a test delivery's render carries the [TEST] marker).
	rendered, ok := notification.RenderMessage(row.triggerKind, row.titleTemplate, row.bodyTemplate, facts)
	if !ok {
		return outboundMessage{Title: title}, errors.New("notification trigger is invalid")
	}
	message := outboundMessage{Title: title, Body: rendered.Body, Parts: rendered.BodyParts}
	attachments := rendered.Attachments
	message.Links = attachments.Links
	if !attachments.Image {
		return message, nil
	}
	request := plugintargetport.NotificationImageRequest{
		ManagedAnimeID:  row.imageManagedAnimeID.String,
		CatalogEntry:    row.imageCatalogEntry,
		ReleaseSource:   notificationFactString(facts, "release.src"),
		BGMID:           notificationFactString(facts, "bgm.id"),
		TMDBID:          notificationFactString(facts, "tmdb.id"),
		TMDBKind:        notificationFactString(facts, "tmdb.kind"),
		PreferredSource: attachments.ImageSource,
	}
	if request.BGMID == "" {
		request.BGMID = row.imageBinding.BGMID
	}
	if request.TMDBID == "" {
		request.TMDBID, request.TMDBKind = row.imageBinding.TMDBID, row.imageBinding.TMDBKind
	}
	message.Image = h.resolveOutboundImage(ctx, request, attachments.ImageURL)
	return message, nil
}

func (h *PluginDeliverHandler) resolveOutboundImage(ctx context.Context, request plugintargetport.NotificationImageRequest, customURL string) *outboundImage {
	if request.PreferredSource == "custom" {
		if imageURL := notification.SafeExternalImageURL(customURL); imageURL != "" {
			return &outboundImage{Source: "custom", URL: imageURL}
		}
		return placeholderNotificationImage()
	}
	if h.imageResolver != nil {
		resolved, resolveErr := h.imageResolver.ResolveNotificationImage(ctx, request)
		if resolveErr == nil && validNotificationImageURL(resolved.URL) {
			return &outboundImage{Source: resolved.Source, URL: resolved.URL}
		}
	}
	return placeholderNotificationImage()
}

func notificationFactsForDelivery(row deliveryRow) (map[string]any, error) {
	if row.deliveryKind == "test" {
		return notification.Sample(row.triggerKind), nil
	}
	var envelope struct {
		Facts json.RawMessage `json:"facts"`
	}
	if err := json.Unmarshal(row.payload, &envelope); err != nil {
		return nil, errors.New("decode notification payload facts")
	}
	return decodeBusinessFacts(envelope.Facts)
}

func notificationFactString(facts map[string]any, name string) string {
	value, _ := notification.LookupFact(facts, name)
	return strings.TrimSpace(notification.FormatFact(value))
}

func validNotificationImageURL(raw string) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.User == nil && parsed.Hostname() != "" && (parsed.Scheme == "https" || parsed.Scheme == "http")
}
