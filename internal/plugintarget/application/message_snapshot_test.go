package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"testing"

	"bansoko/internal/templatecatalog/notification"
)

func TestNotificationSnapshotRetainsFrozenButtonsAndReadsOldRows(t *testing.T) {
	const title, body = "Title", "Body"
	const template = body + "\n{{button(bgm.link, 'Bangumi')}}"
	rendered, _ := notification.RenderMessage(notification.TriggerDownloadSubmitted, title, template, map[string]any{"bgm.id": "123"})
	if len(rendered.Attachments.Buttons) != 1 {
		t.Fatalf("rendered = %+v", rendered)
	}
	raw, err := freezeNotificationMessage(rendered, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := json.RawMessage(`{"version":1,"title":"Title","parts":[{"Text":"Body","Link":null}]}`)
	for _, tc := range []struct {
		name string
		raw  json.RawMessage
		want []notification.Button
	}{
		{name: "frozen buttons", raw: raw, want: rendered.Attachments.Buttons},
		{name: "v1 without buttons", raw: legacy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := deliveryRow{
				messageSnapshot: tc.raw,
				renderedTitle:   sql.NullString{String: title, Valid: true},
				renderedBody:    sql.NullString{String: body, Valid: true},
				renderedDigest:  sql.NullString{String: hashBytes([]byte(title + "\n\n" + body)), Valid: true},
				bodyTemplate:    body + "\n{{button('https://example.com/changed', 'Changed')}}",
			}
			handler := NewPluginDeliverHandler(nil, nil, nil, nil, nil)
			message, err := handler.prepareOutboundMessage(context.Background(), row)
			if err != nil || message.Body != body || !slices.Equal(message.Buttons, tc.want) {
				t.Fatalf("outbound = %+v, err = %v", message, err)
			}
		})
	}
}
