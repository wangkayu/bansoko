package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	deliverypayload "bansoko/internal/plugintarget/payload"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

type fakeNotificationImageResolver struct {
	image   plugintargetport.NotificationImage
	err     error
	request plugintargetport.NotificationImageRequest
}

func (f *fakeNotificationImageResolver) ResolveNotificationImage(_ context.Context, request plugintargetport.NotificationImageRequest) (plugintargetport.NotificationImage, error) {
	f.request = request
	return f.image, f.err
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (f httpDoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

type telegramHTTPResult struct {
	status int
	body   string
	err    error
}

type telegramSequenceClient struct {
	results  []telegramHTTPResult
	requests []*http.Request
	bodies   [][]byte
}

func (c *telegramSequenceClient) Do(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req)
	body, _ := io.ReadAll(req.Body)
	c.bodies = append(c.bodies, body)
	if len(c.results) == 0 {
		return nil, errors.New("unexpected telegram request")
	}
	result := c.results[0]
	c.results = c.results[1:]
	if result.err != nil {
		markRequestWritten(req)
		return nil, result.err
	}
	return &http.Response{StatusCode: result.status, Body: io.NopCloser(strings.NewReader(result.body)), Header: make(http.Header)}, nil
}

func richDeliveryRow(t *testing.T, facts map[string]any) deliveryRow {
	t.Helper()
	payload, err := deliverypayload.Encode("download_submitted", facts)
	if err != nil {
		t.Fatal(err)
	}
	return deliveryRow{
		adapterKind: "telegram", adapterConfigJSON: []byte(`{"adapter_kind":"telegram","chat_id":"999"}`),
		deliveryKind: "business", triggerKind: "download_submitted", payload: payload,
		titleTemplate: "{{anime.title}}", bodyTemplate: "{{image}}\n{{release.title}}\n{{link}}",
	}
}

func TestTelegramRichMessageUsesEscapedHTMLPhotoAndMetadataLinks(t *testing.T) {
	resolver := &fakeNotificationImageResolver{image: plugintargetport.NotificationImage{Source: "mikan", URL: "https://mikanani.me/images/1.jpg"}}
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	handler.SetNotificationImageResolver(resolver)
	row := richDeliveryRow(t, map[string]any{
		"anime.title": "Show <&>", "release.title": "Episode & 1", "release.src": "mikan",
		"bgm.id": "123", "tmdb.id": "456", "tmdb.kind": "tv",
	})
	message, err := handler.resolveOutboundMessage(context.Background(), row, "Show <&>", "Episode & 1")
	if err != nil {
		t.Fatal(err)
	}
	req, mode, err := handler.buildTelegramMessageRequest(context.Background(), row, message, `{"bot_token":"123:abc"}`)
	if err != nil {
		t.Fatal(err)
	}
	if mode.String() != "sendPhoto:url" || !strings.HasSuffix(req.URL.Path, "/sendPhoto") {
		t.Fatalf("mode=%s url=%s", mode.String(), req.URL)
	}
	var payload map[string]string
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["parse_mode"] != "HTML" || payload["photo"] != resolver.image.URL {
		t.Fatalf("payload=%v", payload)
	}
	caption := payload["caption"]
	for _, want := range []string{"<b>Show &lt;&amp;&gt;</b>", "Episode &amp; 1", `<a href="https://bgm.tv/subject/123">Bangumi</a>`, `<a href="https://www.themoviedb.org/tv/456">TMDB</a>`} {
		if !strings.Contains(caption, want) {
			t.Errorf("caption %q does not contain %q", caption, want)
		}
	}
	if strings.Contains(caption, "Show <&>") {
		t.Fatalf("dynamic title was not escaped: %q", caption)
	}
	if resolver.request.ManagedAnimeID != "" || resolver.request.ReleaseSource != "mikan" || resolver.request.BGMID != "123" || resolver.request.TMDBID != "456" {
		t.Fatalf("resolver request=%+v", resolver.request)
	}
}

func TestTelegramOneShotWithoutMetadataUsesBuiltInPlaceholder(t *testing.T) {
	resolver := &fakeNotificationImageResolver{}
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	handler.SetNotificationImageResolver(resolver)
	row := richDeliveryRow(t, map[string]any{"anime.title": "Custom", "release.title": "One shot"})
	message, err := handler.resolveOutboundMessage(context.Background(), row, "Custom", "One shot")
	if err != nil {
		t.Fatal(err)
	}
	if message.Image == nil || !message.Image.Placeholder || len(message.Links) != 0 {
		t.Fatalf("message=%+v", message)
	}
	req, mode, err := handler.buildTelegramMessageRequest(context.Background(), row, message, `{"bot_token":"123:abc"}`)
	if err != nil {
		t.Fatal(err)
	}
	if mode.String() != "sendPhoto:placeholder" {
		t.Fatalf("mode=%s", mode.String())
	}
	photo, err := telegramMultipartPhoto(req)
	if err != nil || !bytes.Equal(photo, notificationPlaceholderJPEG) {
		t.Fatalf("placeholder photo bytes=%d err=%v", len(photo), err)
	}
}

func TestTelegramLongCaptionFallsBackToBoundedHTMLText(t *testing.T) {
	handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
	row := richDeliveryRow(t, map[string]any{"bgm.id": "123"})
	message := outboundMessage{
		Title: "Title", Body: strings.Repeat("界", 5000),
		Image: &outboundImage{Source: "bgm", URL: "https://lain.bgm.tv/pic/1.jpg"},
		Links: []notification.Link{{Provider: "bgm", Label: "Bangumi", URL: "https://bgm.tv/subject/123"}},
	}
	req, mode, err := handler.buildTelegramMessageRequest(context.Background(), row, message, `{"bot_token":"123:abc"}`)
	if err != nil {
		t.Fatal(err)
	}
	if mode.Method != "sendMessage" {
		t.Fatalf("long caption mode=%s", mode.String())
	}
	var payload map[string]string
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if telegramTextLength(payload["text"]) > telegramTextLimit || !strings.Contains(payload["text"], "Bangumi") || payload["parse_mode"] != "HTML" {
		t.Fatalf("bounded text length=%d payload=%v", telegramTextLength(payload["text"]), payload)
	}
}

func TestTelegramHTMLTruncationBudgetsEscapedText(t *testing.T) {
	for _, limit := range []int{telegramCaptionLimit, telegramTextLimit} {
		message := outboundMessage{
			Title: strings.Repeat(`<&>"'`, 100),
			Body:  strings.Repeat(`<&>"'`, 5000),
			Links: []notification.Link{{Provider: "bgm", Label: "Bangumi", URL: "https://bgm.tv/subject/123"}},
		}
		got := fitTelegramHTML(message, limit)
		if telegramTextLength(got) > limit {
			t.Fatalf("limit %d: escaped HTML has %d UTF-16 units: %q", limit, telegramTextLength(got), got)
		}
		if !strings.Contains(got, "&amp;") || !strings.Contains(got, "Bangumi") {
			t.Fatalf("limit %d: escaped body or links were lost: %q", limit, got)
		}
	}
}

func TestTelegramURLRejectionUploadsBytesOnce(t *testing.T) {
	client := &telegramSequenceClient{results: []telegramHTTPResult{
		{status: 400, body: `{"ok":false,"description":"Bad Request: failed to get HTTP URL content"}`},
		{status: 200, body: `{"ok":true,"result":{"message_id":1}}`},
	}}
	handler := NewPluginDeliverHandler(nil, nil, nil, client, nil)
	imageFetches := 0
	handler.imageClient = httpDoerFunc(func(req *http.Request) (*http.Response, error) {
		imageFetches++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/jpeg"}}, Body: io.NopCloser(bytes.NewReader([]byte{0xff, 0xd8, 0xff, 0xd9}))}, nil
	})
	row := richDeliveryRow(t, map[string]any{})
	message := outboundMessage{Title: "Title", Body: "Body", Image: &outboundImage{Source: "mikan", URL: "https://mikanani.me/images/1.jpg"}}
	req, mode, err := handler.buildTelegramMessageRequest(context.Background(), row, message, `{"bot_token":"123:abc"}`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := handler.sendTelegramMessage(context.Background(), req, mode, row, message, `{"bot_token":"123:abc"}`, newSecretRedactor(""))
	if err != nil || result.state != "delivered" || imageFetches != 1 || len(client.requests) != 2 {
		t.Fatalf("result=%+v err=%v fetches=%d requests=%d", result, err, imageFetches, len(client.requests))
	}
	if !strings.HasPrefix(client.requests[1].Header.Get("Content-Type"), "multipart/form-data") || len(result.telegramModes) != 2 || result.telegramModes[1] != "sendPhoto:multipart" {
		t.Fatalf("fallback evidence=%v content-type=%s", result.telegramModes, client.requests[1].Header.Get("Content-Type"))
	}
}

func TestTelegramUnknownAndRetryableOutcomesNeverChangeRepresentation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		client HTTPDoer
		state  string
	}{
		{name: "unknown", client: writtenThenFailingClient{err: context.DeadlineExceeded}, state: "unknown"},
		{name: "retryable", client: &telegramSequenceClient{results: []telegramHTTPResult{{status: 500, body: `{"ok":false}`}}}, state: "waiting_retry"},
		{name: "definite non-image failure", client: &telegramSequenceClient{results: []telegramHTTPResult{{status: 400, body: `{"ok":false,"description":"Bad Request: chat not found"}`}}}, state: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewPluginDeliverHandler(nil, nil, nil, tc.client, nil)
			imageFetches := 0
			handler.imageClient = httpDoerFunc(func(*http.Request) (*http.Response, error) { imageFetches++; return nil, errors.New("must not fetch") })
			row := richDeliveryRow(t, map[string]any{})
			message := outboundMessage{Title: "Title", Body: "Body", Image: &outboundImage{Source: "mikan", URL: "https://mikanani.me/images/1.jpg"}}
			req, mode, err := handler.buildTelegramMessageRequest(context.Background(), row, message, `{"bot_token":"123:abc"}`)
			if err != nil {
				t.Fatal(err)
			}
			result, _ := handler.sendTelegramMessage(context.Background(), req, mode, row, message, `{"bot_token":"123:abc"}`, newSecretRedactor(""))
			if result.state != tc.state || imageFetches != 0 || len(result.telegramModes) != 1 {
				t.Fatalf("result=%+v fetches=%d", result, imageFetches)
			}
		})
	}
}

func TestTelegramRealImageFailuresConvergeToPlaceholderThenText(t *testing.T) {
	t.Run("failed download uses placeholder", func(t *testing.T) {
		client := &telegramSequenceClient{results: []telegramHTTPResult{
			{status: 400, body: `{"ok":false,"description":"Bad Request: wrong file identifier/HTTP URL specified"}`},
			{status: 200, body: `{"ok":true}`},
		}}
		handler := NewPluginDeliverHandler(nil, nil, nil, client, nil)
		handler.imageClient = httpDoerFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("image unavailable") })
		row := richDeliveryRow(t, map[string]any{})
		message := outboundMessage{Title: "Title", Body: "Body", Image: &outboundImage{Source: "mikan", URL: "https://mikanani.me/images/1.jpg"}}
		req, mode, _ := handler.buildTelegramMessageRequest(context.Background(), row, message, `{"bot_token":"123:abc"}`)
		result, _ := handler.sendTelegramMessage(context.Background(), req, mode, row, message, `{"bot_token":"123:abc"}`, newSecretRedactor(""))
		if result.state != "delivered" || strings.Join(result.telegramModes, ",") != "sendPhoto:url,sendPhoto:placeholder" {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("rejected real image upload uses placeholder", func(t *testing.T) {
		client := &telegramSequenceClient{results: []telegramHTTPResult{
			{status: 400, body: `{"ok":false,"description":"Bad Request: failed to get HTTP URL content"}`},
			{status: 400, body: `{"ok":false,"description":"Bad Request: IMAGE_PROCESS_FAILED"}`},
			{status: 200, body: `{"ok":true}`},
		}}
		handler := NewPluginDeliverHandler(nil, nil, nil, client, nil)
		realJPEG := []byte{0xff, 0xd8, 0xff, 0xd9}
		handler.imageClient = httpDoerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"image/jpeg"}}, Body: io.NopCloser(bytes.NewReader(realJPEG))}, nil
		})
		row := richDeliveryRow(t, map[string]any{})
		message := outboundMessage{Title: "Title", Body: "Body", Image: &outboundImage{Source: "mikan", URL: "https://mikanani.me/images/1.jpg"}}
		req, mode, _ := handler.buildTelegramMessageRequest(context.Background(), row, message, `{"bot_token":"123:abc"}`)
		result, _ := handler.sendTelegramMessage(context.Background(), req, mode, row, message, `{"bot_token":"123:abc"}`, newSecretRedactor(""))
		if result.state != "delivered" || strings.Join(result.telegramModes, ",") != "sendPhoto:url,sendPhoto:multipart,sendPhoto:placeholder" {
			t.Fatalf("result=%+v", result)
		}
		if len(client.bodies) != 3 || !bytes.Contains(client.bodies[1], realJPEG) || !bytes.Contains(client.bodies[2], notificationPlaceholderJPEG) {
			t.Fatalf("multipart fallback bodies=%d", len(client.bodies))
		}
	})

	t.Run("rejected placeholder retains links in text", func(t *testing.T) {
		client := &telegramSequenceClient{results: []telegramHTTPResult{
			{status: 400, body: `{"ok":false,"description":"Bad Request: IMAGE_PROCESS_FAILED"}`},
			{status: 200, body: `{"ok":true}`},
		}}
		handler := NewPluginDeliverHandler(nil, nil, nil, client, nil)
		row := richDeliveryRow(t, map[string]any{})
		message := outboundMessage{Title: "Title", Body: "Body", Image: placeholderNotificationImage(), Links: []notification.Link{{Provider: "bgm", Label: "Bangumi", URL: "https://bgm.tv/subject/123"}}}
		req, mode, _ := handler.buildTelegramMessageRequest(context.Background(), row, message, `{"bot_token":"123:abc"}`)
		result, _ := handler.sendTelegramMessage(context.Background(), req, mode, row, message, `{"bot_token":"123:abc"}`, newSecretRedactor(""))
		if result.state != "delivered" || strings.Join(result.telegramModes, ",") != "sendPhoto:placeholder,sendMessage" || !strings.Contains(string(client.bodies[1]), "bgm.tv") {
			t.Fatalf("result=%+v final=%s", result, client.bodies[len(client.bodies)-1])
		}
	})
}
