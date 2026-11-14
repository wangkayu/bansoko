package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	adapterconfig "bansoko/internal/plugintarget/config"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
)

type episodeTitleResolverFunc func(context.Context, plugintargetport.NotificationEpisodeTitleRequest) (plugintargetport.NotificationEpisodeTitle, error)

func (f episodeTitleResolverFunc) ResolveNotificationEpisodeTitle(ctx context.Context, request plugintargetport.NotificationEpisodeTitleRequest) (plugintargetport.NotificationEpisodeTitle, error) {
	return f(ctx, request)
}

func episodeTitleTestTarget(t *testing.T, title, body string) TargetConfig {
	t.Helper()
	set, err := json.Marshal(map[string]any{
		"language_version": templateLanguageVersion,
		"catalog_version":  templateCatalogVersion,
		"events": map[string]any{
			"download_submitted":       map[string]any{"mode": "system_default"},
			"file_operation_completed": map[string]any{"mode": "custom", "title_template": title, "body_template": body},
			"file_operation_failed":    map[string]any{"mode": "system_default"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return TargetConfig{ConfigStatus: adapterconfig.StatusAvailable, TemplateSet: set}
}

func episodeTitleTestRequest(payload string) plugintargetport.BusinessDeliveryRequest {
	return plugintargetport.BusinessDeliveryRequest{TriggerKind: plugintargetport.TriggerFileOperationCompleted, Payload: json.RawMessage(payload)}
}

func TestAdmissionEpisodeTitlesResolveOnlyReferencedBoundSources(t *testing.T) {
	var calls []plugintargetport.NotificationEpisodeTitleRequest
	resolver := &AdmissionResolver{EpisodeTitles: episodeTitleResolverFunc(func(_ context.Context, request plugintargetport.NotificationEpisodeTitleRequest) (plugintargetport.NotificationEpisodeTitle, error) {
		calls = append(calls, request)
		return plugintargetport.NotificationEpisodeTitle{Known: true, Title: "Episode from " + request.Source}, nil
	})}
	target := episodeTitleTestTarget(t, "{{bgm.ep_title}}", "{{tmdb.ep_title}}")
	payload, err := resolver.resolveAdmissionEpisodeTitles(context.Background(), target, episodeTitleTestRequest(`{"bgm.id":"123","tmdb.id":"456","tmdb.kind":"tv","tmdb.season":2,"ep.number":"7"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Source != "bgm" || calls[0].WorkID != "123" || calls[0].Season != 0 || calls[0].Episode != 7 || calls[1].Source != "tmdb" || calls[1].Season != 2 {
		t.Fatalf("unexpected lookups: %+v", calls)
	}
	facts, ok := notification.DecodeFacts(payload)
	if !ok || facts["bgm.ep_title"] != "Episode from bgm" || facts["tmdb.ep_title"] != "Episode from tmdb" {
		t.Fatalf("enriched facts: %+v", facts)
	}
}

func TestAdmissionEpisodeTitlesReuseFrozenValuesAndSkipInvalidLookups(t *testing.T) {
	for _, tc := range []struct {
		name    string
		title   string
		body    string
		payload string
	}{
		{name: "not referenced", title: "plain", body: "body", payload: `{"bgm.id":"123","ep.number":1}`},
		{name: "already frozen", title: "{{bgm.ep_title}}", body: "body", payload: `{"bgm.id":"123","bgm.ep_title":"Frozen","ep.number":1}`},
		{name: "missing binding", title: "{{bgm.ep_title}}", body: "body", payload: `{"ep.number":1}`},
		{name: "missing episode", title: "{{bgm.ep_title}}", body: "body", payload: `{"bgm.id":"123"}`},
		{name: "fractional episode", title: "{{bgm.ep_title}}", body: "body", payload: `{"bgm.id":"123","ep.number":1.5}`},
		{name: "TMDB movie", title: "{{tmdb.ep_title}}", body: "body", payload: `{"tmdb.id":"456","tmdb.kind":"movie","tmdb.season":1,"ep.number":1}`},
		{name: "TMDB missing season", title: "{{tmdb.ep_title}}", body: "body", payload: `{"tmdb.id":"456","tmdb.kind":"tv","ep.number":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &AdmissionResolver{EpisodeTitles: episodeTitleResolverFunc(func(context.Context, plugintargetport.NotificationEpisodeTitleRequest) (plugintargetport.NotificationEpisodeTitle, error) {
				t.Fatal("unexpected provider lookup")
				return plugintargetport.NotificationEpisodeTitle{}, nil
			})}
			if _, err := resolver.resolveAdmissionEpisodeTitles(context.Background(), episodeTitleTestTarget(t, tc.title, tc.body), episodeTitleTestRequest(tc.payload)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdmissionEpisodeTitlesPropagateLookupFailure(t *testing.T) {
	want := errors.New("temporary provider failure")
	resolver := &AdmissionResolver{EpisodeTitles: episodeTitleResolverFunc(func(context.Context, plugintargetport.NotificationEpisodeTitleRequest) (plugintargetport.NotificationEpisodeTitle, error) {
		return plugintargetport.NotificationEpisodeTitle{}, want
	})}
	_, err := resolver.resolveAdmissionEpisodeTitles(context.Background(), episodeTitleTestTarget(t, "{{bgm.ep_title}}", "body"), episodeTitleTestRequest(`{"bgm.id":"123","ep.number":1}`))
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want wrapped provider failure", err)
	}
}
