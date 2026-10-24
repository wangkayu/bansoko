package application

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	deliverypayload "bansoko/internal/plugintarget/payload"
	templatecatalog "bansoko/internal/templatecatalog/application"
	"bansoko/internal/templatecatalog/notification"
)

// A template that previews valid must render identically when it is sent:
// preview, test delivery and business delivery share one rendering core.
func TestPreviewTestAndBusinessDeliveryRenderIdentically(t *testing.T) {
	catalog := templatecatalog.NewService()
	custom := []struct{ title, body string }{
		{"{{coalesce(anime.title, release.title)}} · {{upper(event.type)}}", "{{defaultIfEmpty(anime.group, '未知, 无')}}|{{replace(release.title, '[', '(')}}|{{if(eq(result.outcome, 'success'), '成功', '失败')}}|{{lower('ABC')}}"},
		{"{{anime.title}}", "{{release.title}}\n{{event.at}}"},
	}
	// The custom templates read result.* facts, which only the media-pipeline
	// triggers carry.
	for _, trigger := range []string{notification.TriggerDownloadSubmitted, notification.TriggerFileOperationCompleted, notification.TriggerFileOperationFailed} {
		title, body := notification.Defaults(trigger)
		cases := append([]struct{ title, body string }{{title, body}}, custom...)
		for _, tc := range cases {
			preview, err := catalog.PreviewNotification(context.Background(), templatecatalog.NotificationTemplatePreviewInput{
				TitleTemplate: tc.title, BodyTemplate: tc.body, TriggerKind: templatecatalog.NotificationTriggerKind(trigger),
				LanguageVersion: templatecatalog.NotificationTemplatePreviewInputLanguageVersionBansokoTemplate1,
				CatalogVersion:  templatecatalog.NotificationTemplatePreviewInputCatalogVersion(notification.CatalogVersion),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !preview.Valid {
				t.Fatalf("%s: preview invalid: %+v", trigger, preview.Errors)
			}
			// The business path renders frozen payload bytes, not the sample map.
			frozen, err := deliverypayload.Encode(trigger, notification.Sample(trigger))
			if err != nil {
				t.Fatal(err)
			}
			var envelope struct {
				Facts json.RawMessage `json:"facts"`
			}
			if err := json.Unmarshal(frozen, &envelope); err != nil {
				t.Fatal(err)
			}
			facts, err := decodeBusinessFacts(envelope.Facts)
			if err != nil {
				t.Fatal(err)
			}
			sent := renderBusinessMessage(trigger, tc.title, tc.body, facts)
			if !sent.Valid() || sent.Title != preview.Title || sent.Body != preview.Body {
				t.Fatalf("%s: business render %+v differs from preview title=%q body=%q", trigger, sent, preview.Title, preview.Body)
			}
			testTitle, testBody, err := catalog.RenderNotification(tc.title, tc.body, trigger, notification.CatalogVersion)
			if err != nil || testTitle != preview.Title || testBody != preview.Body {
				t.Fatalf("%s: test delivery render title=%q body=%q err=%v differs from preview", trigger, testTitle, testBody, err)
			}
		}
	}
}

func TestBusinessDeliveryRejectsWhatPreviewRejects(t *testing.T) {
	for _, body := range []string{"{{anime.unknown}}", "{{nope(anime.title)}}", "{{anime.title", "{{defaultIfEmpty(anime.title)}}"} {
		message := renderBusinessMessage(notification.TriggerDownloadSubmitted, "{{anime.title}}", body, notification.Sample(notification.TriggerDownloadSubmitted))
		if message.Valid() {
			t.Fatalf("%q rendered valid on the business path", body)
		}
	}
}

func TestWithEventFactsFillsTypeAndOccurredAtFromTheLedger(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 30, 0, 0, time.FixedZone("x", 8*3600))
	got, _ := notification.DecodeFacts(withEventFacts(json.RawMessage(`{"anime.title":"A","file.size":734003200}`), notification.TriggerFileOperationCompleted, at))
	if got["event.type"] != notification.TriggerFileOperationCompleted || got["event.at"] != "2026-10-11T04:30:00Z" || got["file.size"].(json.Number).String() != "734003200" {
		t.Fatalf("facts=%v", got)
	}
	kept, _ := notification.DecodeFacts(withEventFacts(json.RawMessage(`{"event.type":"download_submitted"}`), notification.TriggerFileOperationCompleted, at))
	if kept["event.type"] != "download_submitted" {
		t.Fatalf("producer event.type overwritten: %v", kept)
	}
}
