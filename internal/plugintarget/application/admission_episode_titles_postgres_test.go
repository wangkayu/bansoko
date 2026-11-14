package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"bansoko/internal/episodetitle"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	runtimeworker "bansoko/internal/runtime/worker"
	templateapplication "bansoko/internal/templatecatalog/application"
	"bansoko/internal/templatecatalog/notification"
	"bansoko/internal/testutil/disposabledb"
	"bansoko/internal/workitems"
)

type fakeAdmissionEpisodeTitles struct {
	requests []plugintargetport.NotificationEpisodeTitleRequest
	answers  map[string]plugintargetport.NotificationEpisodeTitle
	failures []error
	before   func(plugintargetport.NotificationEpisodeTitleRequest)
}

func (f *fakeAdmissionEpisodeTitles) ResolveNotificationEpisodeTitle(_ context.Context, request plugintargetport.NotificationEpisodeTitleRequest) (plugintargetport.NotificationEpisodeTitle, error) {
	f.requests = append(f.requests, request)
	if f.before != nil {
		f.before(request)
	}
	if len(f.failures) > 0 {
		err := f.failures[0]
		f.failures = f.failures[1:]
		if err != nil {
			return plugintargetport.NotificationEpisodeTitle{}, err
		}
	}
	if answer, ok := f.answers[request.Source]; ok {
		return answer, nil
	}
	return plugintargetport.NotificationEpisodeTitle{}, nil
}

func createEpisodeTitleTarget(t *testing.T, service *Service, name, title, body string) TargetConfig {
	t.Helper()
	input := validCreateInput(t)
	input.Name = name
	input.Enabled = true
	input.Secret = secretSetInput(t)
	input.TriggerKinds = []TriggerKind{TriggerFileOperationCompleted}
	input.TemplateSet = episodeTitleTestTarget(t, title, body).TemplateSet
	target, err := service.CreateTarget(context.Background(), input, "episode-title-create-"+name)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func admitEpisodeTitleEvent(t *testing.T, service *Service, resultKey string, payload json.RawMessage) {
	t.Helper()
	tx, err := service.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = service.EnqueueBusinessDelivery(context.Background(), txscope.Wrap(tx), plugintargetport.BusinessDeliveryRequest{
		SourceOwnerType: plugintargetport.OwnerFileOperation, SourceOwnerID: "episode-title-operation-" + resultKey,
		SourceOwnerVersion: 1, TriggerKind: plugintargetport.TriggerFileOperationCompleted,
		ResultKey: resultKey, Payload: payload,
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func episodeTitleDeliveryFacts(t *testing.T, service *Service, targetID string) map[string]any {
	t.Helper()
	var raw []byte
	if err := service.db.QueryRow(`SELECT payload->'facts' FROM plugin_deliveries WHERE plugin_target_config_id=$1`, targetID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	facts, ok := notification.DecodeFacts(raw)
	if !ok {
		t.Fatalf("invalid frozen facts: %s", raw)
	}
	return facts
}

func TestAdmissionResolvesEpisodeTitleWithoutNamingSnapshotAndDeliveryRetryReusesIt(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	target := createEpisodeTitleTarget(t, service, "frozen-title", "{{bgm.ep_title}}", "Episode: {{bgm.ep_title}}")
	admitEpisodeTitleEvent(t, service, "without-naming-snapshot", json.RawMessage(`{"anime.title":"Show","release.title":"Show 07","bgm.id":"123","bgm.ep_title":null,"ep.number":7}`))
	lookups := &fakeAdmissionEpisodeTitles{answers: map[string]plugintargetport.NotificationEpisodeTitle{
		"bgm": {Known: true, Title: "Admission title"},
	}}
	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, EpisodeTitles: lookups}
	created, err := resolver.SweepOnce(context.Background())
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	if len(lookups.requests) != 1 || lookups.requests[0].Source != "bgm" || lookups.requests[0].Episode != 7 {
		t.Fatalf("lookups=%+v", lookups.requests)
	}
	if got := episodeTitleDeliveryFacts(t, service, target.ID)["bgm.ep_title"]; got != "Admission title" {
		t.Fatalf("frozen title=%v", got)
	}
	var deliveryID, rendered string
	if err := db.QueryRow(`SELECT id,rendered_body FROM plugin_deliveries WHERE plugin_target_config_id=$1`, target.ID).Scan(&deliveryID, &rendered); err != nil {
		t.Fatal(err)
	}
	if rendered != "Episode: Admission title" {
		t.Fatalf("rendered body=%q", rendered)
	}

	claim, err := service.workItems.ClaimDue(context.Background(), "episode-title-worker-1", []string{workitems.KindPluginDeliver}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	client := &countingHTTPClient{status: http.StatusInternalServerError}
	handler := NewPluginDeliverHandler(db, service.secrets, service.workItems, client, templateapplication.NewService())
	outcome, err := handler.Handle(context.Background(), claim)
	if err != nil || outcome.Kind != runtimeworker.OutcomeRetry {
		t.Fatalf("first delivery outcome=%+v err=%v", outcome, err)
	}
	finishHandlerOutcome(t, service, claim, outcome)
	if _, err := disposabledb.Owner(t).Exec(`UPDATE work_items SET available_at=CURRENT_TIMESTAMP-INTERVAL '1 second' WHERE owner_type='plugin_delivery' AND owner_id=$1 AND state='available'`, deliveryID); err != nil {
		t.Fatal(err)
	}
	retry, err := service.workItems.ClaimDue(context.Background(), "episode-title-worker-2", []string{workitems.KindPluginDeliver}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	client.status = http.StatusNoContent
	outcome, err = handler.Handle(context.Background(), retry)
	if err != nil || outcome.Kind != runtimeworker.OutcomeFinish {
		t.Fatalf("retry outcome=%+v err=%v", outcome, err)
	}
	finishHandlerOutcome(t, service, retry, outcome)
	if len(lookups.requests) != 1 || client.calls != 2 {
		t.Fatalf("metadata lookups=%d delivery requests=%d, want 1/2", len(lookups.requests), client.calls)
	}
}

func TestAdmissionEpisodeTitleTransientFailureRetriesBeforeDeliveryCreation(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	createEpisodeTitleTarget(t, service, "transient-title", "{{bgm.ep_title}}", "body")
	admitEpisodeTitleEvent(t, service, "transient-title", json.RawMessage(`{"anime.title":"Show","release.title":"Show 01","bgm.id":"123","ep.number":1}`))
	lookups := &fakeAdmissionEpisodeTitles{
		failures: []error{fmt.Errorf("%w: provider returned HTTP 503", episodetitle.ErrTransient)},
		answers:  map[string]plugintargetport.NotificationEpisodeTitle{"bgm": {Known: true, Title: "Recovered"}},
	}
	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, EpisodeTitles: lookups, Backoff: func(int) time.Duration { return 0 }}
	created, err := resolver.SweepOnce(context.Background())
	if err != nil || created != 0 {
		t.Fatalf("first sweep created=%d err=%v", created, err)
	}
	var state, lastErrorCode string
	var attempts, deliveries int
	if err := db.QueryRow(`SELECT (SELECT state FROM notification_admissions),(SELECT attempt_count FROM notification_admissions),(SELECT last_error_code FROM notification_admissions),(SELECT COUNT(*) FROM plugin_deliveries)`).Scan(&state, &attempts, &lastErrorCode, &deliveries); err != nil {
		t.Fatal(err)
	}
	if state != AdmissionPendingTargetResolution || attempts != 0 || lastErrorCode != codeAdmissionTransient || deliveries != 0 {
		t.Fatalf("state=%q attempts=%d last_error=%q deliveries=%d", state, attempts, lastErrorCode, deliveries)
	}
	created, err = resolver.SweepOnce(context.Background())
	if err != nil || created != 1 || len(lookups.requests) != 2 {
		t.Fatalf("second sweep created=%d lookups=%d err=%v", created, len(lookups.requests), err)
	}
}

func TestAdmissionTargetEditDuringEpisodeTitleLookupCannotMixTemplateVersions(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	target := createEpisodeTitleTarget(t, service, "version-race", "{{bgm.ep_title}}", "old {{bgm.ep_title}}")
	admitEpisodeTitleEvent(t, service, "version-race", json.RawMessage(`{"anime.title":"Show","release.title":"Show 01","bgm.id":"123","bgm.ep_title":null,"ep.number":1}`))
	newSet := episodeTitleTestTarget(t, "new title", "new body").TemplateSet
	lookups := &fakeAdmissionEpisodeTitles{answers: map[string]plugintargetport.NotificationEpisodeTitle{"bgm": {Known: true, Title: "Old title"}}}
	lookups.before = func(plugintargetport.NotificationEpisodeTitleRequest) {
		if _, err := db.Exec(`UPDATE plugin_target_configs SET template_set=$2,version=version+1,updated_at=clock_timestamp() WHERE id=$1`, target.ID, newSet); err != nil {
			t.Fatal(err)
		}
	}
	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, EpisodeTitles: lookups}
	created, err := resolver.SweepOnce(context.Background())
	if err != nil || created != 1 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	var version int
	var rendered string
	if err := db.QueryRow(`SELECT plugin_target_config_version,rendered_body FROM plugin_deliveries WHERE plugin_target_config_id=$1`, target.ID).Scan(&version, &rendered); err != nil {
		t.Fatal(err)
	}
	if version != 2 || rendered != "new body" || len(lookups.requests) != 1 {
		t.Fatalf("version=%d body=%q lookups=%d", version, rendered, len(lookups.requests))
	}
	if got := episodeTitleDeliveryFacts(t, service, target.ID)["bgm.ep_title"]; got != nil {
		t.Fatalf("old lookup leaked into new target version: %v", got)
	}
}

func TestAdmissionEpisodeTitlesAreResolvedPerTargetTemplate(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	withTitle := createEpisodeTitleTarget(t, service, "per-target-title", "{{tmdb.ep_title}}", "body")
	withoutTitle := createEpisodeTitleTarget(t, service, "per-target-plain", "plain", "body")
	admitEpisodeTitleEvent(t, service, "per-target", json.RawMessage(`{"anime.title":"Show","release.title":"Show 03","tmdb.id":"456","tmdb.kind":"tv","tmdb.season":2,"ep.number":3}`))
	lookups := &fakeAdmissionEpisodeTitles{answers: map[string]plugintargetport.NotificationEpisodeTitle{"tmdb": {Known: true, Title: "TMDB title"}}}
	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, EpisodeTitles: lookups}
	created, err := resolver.SweepOnce(context.Background())
	if err != nil || created != 2 || len(lookups.requests) != 1 {
		t.Fatalf("created=%d lookups=%d err=%v", created, len(lookups.requests), err)
	}
	if got := episodeTitleDeliveryFacts(t, service, withTitle.ID)["tmdb.ep_title"]; got != "TMDB title" {
		t.Fatalf("referenced target title=%v", got)
	}
	if got := episodeTitleDeliveryFacts(t, service, withoutTitle.ID)["tmdb.ep_title"]; got != nil {
		t.Fatalf("unreferenced target unexpectedly enriched: %v", got)
	}
}

func TestAdmissionReplayReusesExistingDeliveryWithoutResolvingAgain(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	target := createEpisodeTitleTarget(t, service, "replay-title", "{{bgm.ep_title}}", "Episode: {{bgm.ep_title}}")
	admitEpisodeTitleEvent(t, service, "replay-title", json.RawMessage(`{"anime.title":"Show","bgm.id":"123","ep.number":7}`))
	lookups := &fakeAdmissionEpisodeTitles{answers: map[string]plugintargetport.NotificationEpisodeTitle{
		"bgm": {Known: true, Title: "Frozen on first attempt"},
	}}
	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, EpisodeTitles: lookups}
	created, err := resolver.SweepOnce(context.Background())
	if err != nil || created != 1 {
		t.Fatalf("first sweep created=%d err=%v", created, err)
	}
	// Recreate the reachable crash window: the target delivery committed, but
	// the admission settlement did not. A later provider answer must not replace
	// or conflict with the delivery that already owns this identity.
	if _, err := db.Exec(`UPDATE notification_admissions SET state=$1`, AdmissionPendingTargetResolution); err != nil {
		t.Fatal(err)
	}
	lookups.answers["bgm"] = plugintargetport.NotificationEpisodeTitle{Known: true, Title: "Changed later"}
	created, err = resolver.SweepOnce(context.Background())
	if err != nil || created != 0 || len(lookups.requests) != 1 {
		t.Fatalf("replay created=%d lookups=%d err=%v", created, len(lookups.requests), err)
	}
	var state, rendered string
	if err := db.QueryRow(`SELECT (SELECT state FROM notification_admissions),(SELECT rendered_body FROM plugin_deliveries WHERE plugin_target_config_id=$1)`, target.ID).Scan(&state, &rendered); err != nil {
		t.Fatal(err)
	}
	if state != AdmissionResolved || rendered != "Episode: Frozen on first attempt" {
		t.Fatalf("state=%q rendered=%q", state, rendered)
	}
}

func TestAdmissionTargetDisabledDuringEpisodeTitleLookupDoesNotDeliver(t *testing.T) {
	db := openTestDB(t)
	service := newTestService(t, db)
	target := createEpisodeTitleTarget(t, service, "disabled-title", "{{bgm.ep_title}}", "body")
	admitEpisodeTitleEvent(t, service, "disabled-title", json.RawMessage(`{"anime.title":"Show","bgm.id":"123","ep.number":7}`))
	lookups := &fakeAdmissionEpisodeTitles{answers: map[string]plugintargetport.NotificationEpisodeTitle{
		"bgm": {Known: true, Title: "Unused"},
	}}
	lookups.before = func(plugintargetport.NotificationEpisodeTitleRequest) {
		if _, err := db.Exec(`UPDATE plugin_target_configs SET enabled=FALSE,version=version+1,updated_at=clock_timestamp() WHERE id=$1`, target.ID); err != nil {
			t.Fatal(err)
		}
	}
	resolver := &AdmissionResolver{Service: service, Logger: observabilityport.NopRuntimeLogger{}, EpisodeTitles: lookups}
	created, err := resolver.SweepOnce(context.Background())
	if err != nil || created != 0 || len(lookups.requests) != 1 {
		t.Fatalf("created=%d lookups=%d err=%v", created, len(lookups.requests), err)
	}
	var state string
	var deliveries int
	if err := db.QueryRow(`SELECT (SELECT state FROM notification_admissions),(SELECT COUNT(*) FROM plugin_deliveries)`).Scan(&state, &deliveries); err != nil {
		t.Fatal(err)
	}
	if state != AdmissionResolved || deliveries != 0 {
		t.Fatalf("state=%q deliveries=%d", state, deliveries)
	}
}
