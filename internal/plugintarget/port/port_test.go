package port

import (
	"context"
	"encoding/json"
	"testing"

	"bansoko/internal/persistence/txscope"
)

// plugintarget/port is the narrow admission contract for business delivery.
// Covered: trigger kind constant stability,
// the payload boundary semantics of BusinessDeliveryRequest (bounded and
// redacted — no credentials, URLs,
// private absolute paths, or raw provider responses), and the Enqueuer
// contract (no transaction management of its own).

// The deletion notice names the obligation as its source and carries the file
// base name only, never its path.
func TestReplacementDeletionFailedRequest(t *testing.T) {
	request, err := ReplacementDeletionFailedRequest("cleanup-1", 4, "anime-1", "Show", "/library/Show/Season 1/Show - 01.mkv", "file may be in use", "busy")
	if err != nil {
		t.Fatal(err)
	}
	if request.TriggerKind != TriggerReplacementDeletionFailed || request.SourceOwnerType != OwnerReplacementDeletion ||
		request.SourceOwnerID != "cleanup-1" || request.SourceOwnerVersion != 4 || request.ManagedAnimeID != "anime-1" ||
		request.ResultKey != ReplacementDeletionFailedResultKey {
		t.Fatalf("request = %+v", request)
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 5 || payload["file.name"] != "Show - 01.mkv" || payload["anime.title"] != "Show" ||
		payload["anime.id"] != "anime-1" || payload["result.reason"] != "file may be in use" || payload["result.category"] != "busy" {
		t.Fatalf("payload = %v", payload)
	}
	untitled, err := ReplacementDeletionFailedRequest("cleanup-1", 4, "anime-1", "", "x.mkv", "r", "busy")
	if err != nil {
		t.Fatal(err)
	}
	payload = nil
	if err := json.Unmarshal(untitled.Payload, &payload); err != nil || payload["anime.title"] != nil {
		t.Fatalf("an empty title must be null: %v %v", payload, err)
	}
}

// The started notice freezes the work binding the subscription began with;
// every other transition carries no binding, so a later presentation can only
// link the work on the message that introduces it.
func TestSubscriptionChangedRequestFreezesBindingsOnlyForStarted(t *testing.T) {
	binding := SubscriptionBinding{BGMID: "400602", TMDBID: "209867", TMDBKind: "tv"}
	started, err := SubscriptionChangedRequest("sub-1", 1, "anime-1", "Show", SubscriptionStarted, binding)
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if err := json.Unmarshal(started.Payload, &facts); err != nil {
		t.Fatal(err)
	}
	if facts["event.action"] != SubscriptionStarted || facts["bgm.id"] != "400602" ||
		facts["tmdb.id"] != "209867" || facts["tmdb.kind"] != "tv" {
		t.Fatalf("started facts = %v", facts)
	}
	for _, action := range []string{SubscriptionStopped, SubscriptionResumed, SubscriptionRetired} {
		request, err := SubscriptionChangedRequest("sub-1", 2, "anime-1", "Show", action, binding)
		if err != nil {
			t.Fatal(err)
		}
		facts = nil
		if err := json.Unmarshal(request.Payload, &facts); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"bgm.id", "tmdb.id", "tmdb.kind"} {
			if _, exists := facts[key]; exists {
				t.Fatalf("%s carries %s: %v", action, key, facts)
			}
		}
	}
	// A started subscription without a binding carries no id facts at all.
	without, err := SubscriptionChangedRequest("sub-1", 1, "anime-1", "Show", SubscriptionStarted, SubscriptionBinding{})
	if err != nil {
		t.Fatal(err)
	}
	facts = nil
	if err := json.Unmarshal(without.Payload, &facts); err != nil {
		t.Fatal(err)
	}
	if _, exists := facts["bgm.id"]; exists {
		t.Fatalf("unbound started facts = %v", facts)
	}
	if _, exists := facts["tmdb.id"]; exists {
		t.Fatalf("unbound started facts = %v", facts)
	}
	if _, exists := facts["tmdb.kind"]; exists {
		t.Fatalf("unbound started facts = %v", facts)
	}
}

func TestTriggerKindsAreStable(t *testing.T) {
	if TriggerDownloadSubmitted != "download_submitted" ||
		TriggerFileOperationCompleted != "file_operation_completed" ||
		TriggerFileOperationFailed != "file_operation_failed" {
		t.Fatal("notification trigger literals drifted")
	}
}

func TestOwnerKindsAreStable(t *testing.T) {
	if OwnerDownloadTask != "download_task" || OwnerFileOperation != "file_operation" {
		t.Fatal("delivery source owner literals drifted")
	}
}

func TestBusinessDeliveryRequestCarriesRedactedPayload(t *testing.T) {
	req := BusinessDeliveryRequest{
		SourceOwnerType:    OwnerDownloadTask,
		SourceOwnerID:      "task-1",
		SourceOwnerVersion: 3,
		ManagedAnimeID:     "anime-1",
		TriggerKind:        TriggerDownloadSubmitted,
		Payload:            json.RawMessage(`{"title":"Show","episode":1}`),
		ResultKey:          "r1",
	}
	if req.SourceOwnerVersion != 3 || req.TriggerKind != TriggerDownloadSubmitted {
		t.Fatalf("request = %+v", req)
	}
	if len(req.Payload) == 0 {
		t.Fatal("payload must be non-empty for a business delivery")
	}
	// The payload is a bounded, redacted JSON object; it must not carry
	// credentials, URLs or private paths.
	var decoded map[string]any
	if err := json.Unmarshal(req.Payload, &decoded); err != nil {
		t.Fatalf("payload must be valid JSON: %v", err)
	}
	for _, banned := range []string{"password", "secret", "token", "http", "/Users/", "endpoint"} {
		if containsJSON(decoded, banned) {
			t.Fatalf("payload must not contain %q (redaction contract)", banned)
		}
	}
}

// Enqueuer contract: implementations must not begin, commit or roll back
// transactions themselves. A stub verifies
// the interface shape and that the tx parameter is passed through (no
// self-managed transaction semantics).
type stubEnqueuer struct {
	got *BusinessDeliveryRequest
}

func (s *stubEnqueuer) EnqueueBusinessDelivery(_ context.Context, _ txscope.Tx, req BusinessDeliveryRequest) error {
	cp := req
	s.got = &cp
	return nil
}

func TestBusinessDeliveryEnqueuerContract(t *testing.T) {
	var _ BusinessDeliveryEnqueuer = (*stubEnqueuer)(nil)

	e := &stubEnqueuer{}
	req := BusinessDeliveryRequest{SourceOwnerType: OwnerFileOperation, SourceOwnerID: "fo-1", TriggerKind: TriggerFileOperationCompleted}
	if err := e.EnqueueBusinessDelivery(context.Background(), txscope.Tx{}, req); err != nil {
		t.Fatal(err)
	}
	if e.got == nil || e.got.SourceOwnerID != "fo-1" || e.got.TriggerKind != TriggerFileOperationCompleted {
		t.Fatalf("enqueuer did not receive request: %+v", e.got)
	}
}

func containsJSON(value any, needle string) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, val := range v {
			if contains(key, needle) || containsJSON(val, needle) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if containsJSON(item, needle) {
				return true
			}
		}
	case string:
		return contains(v, needle)
	}
	return false
}

func contains(s, needle string) bool {
	return len(s) >= len(needle) && (s == needle || len(needle) == 0 || (len(s) > 0 && indexOf(s, needle) >= 0))
}

func indexOf(s, needle string) int {
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// The replacement notice names the decision as its source, the winning
// release, and every old version by file name plus whatever identity that
// version recorded. The legacy variables keep the versions' current library
// names; the frozen original download source travels in separate
// replaced.new.* / replaced.old_versions facts.
func TestReplacementAppliedRequest(t *testing.T) {
	request, err := ReplacementAppliedRequest("dec-1", 1, "anime-1", "Show", "GroupA", "[GroupA] Show - 01 [1080p]", "winning-release", []DisplacedTarget{
		{File: "/library/Show/EP01.mkv", Group: "GroupA", Title: "[GroupA] Show - 01 [720p]"},
		{File: "/library/Show/extra.mkv"},
	}, OriginalSource{
		New: DisplacedTarget{File: "/downloads/[GroupA] Show - 01 [1080p].mkv", Group: "SourceGroup", Title: "[SourceGroup] Show - 01 [1080p]"},
		Old: []DisplacedTarget{
			{File: "/downloads/[GroupA] Show - 01 [720p].mkv", Group: "SourceGroup", Title: "[SourceGroup] Show - 01 [720p]"},
			{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if request.TriggerKind != TriggerReplacementApplied || request.SourceOwnerType != OwnerReplacementDecision ||
		request.SourceOwnerID != "dec-1" || request.SourceOwnerVersion != 1 || request.ManagedAnimeID != "anime-1" ||
		request.ResultKey != ReplacementAppliedResultKey {
		t.Fatalf("request = %+v", request)
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["anime.title"] != "Show" || payload["anime.group"] != "GroupA" ||
		payload["release.title"] != "[GroupA] Show - 01 [1080p]" ||
		payload["replaced.count"] != float64(2) {
		t.Fatalf("payload = %v", payload)
	}
	targets, _ := payload["replaced.targets"].([]any)
	if len(targets) != 2 {
		t.Fatalf("replaced.targets = %v", payload["replaced.targets"])
	}
	first, _ := targets[0].(map[string]any)
	if first["path"] != "EP01.mkv" || first["group"] != "GroupA" || first["title"] != "[GroupA] Show - 01 [720p]" {
		t.Fatalf("first target = %v", first)
	}
	// A version that records no release travels as its file name alone.
	second, _ := targets[1].(map[string]any)
	if len(second) != 1 || second["path"] != "extra.mkv" {
		t.Fatalf("second target = %v", second)
	}
	// The default presentation reads only the frozen original source, never the
	// legacy current-name facts.
	if payload["replaced.new.title"] != "[SourceGroup] Show - 01 [1080p]" ||
		payload["replaced.new.group"] != "SourceGroup" ||
		payload["replaced.new.file"] != "[GroupA] Show - 01 [1080p].mkv" {
		t.Fatalf("new original source = %v", payload)
	}
	originals, _ := payload["replaced.old_versions"].([]any)
	if len(originals) != 1 {
		t.Fatalf("replaced.old_versions = %v, want the one version with a source", payload["replaced.old_versions"])
	}
	original, _ := originals[0].(map[string]any)
	if original["path"] != "[GroupA] Show - 01 [720p].mkv" || original["group"] != "SourceGroup" || original["title"] != "[SourceGroup] Show - 01 [720p]" {
		t.Fatalf("original old version = %v", original)
	}
	if _, err := ReplacementAppliedRequest("dec-1", 1, "anime-1", "Show", "", "", "winning-release", nil, OriginalSource{}); err == nil {
		t.Fatal("a replacement that replaced nothing must not build a notice")
	}
	untitled, err := ReplacementAppliedRequest("dec-1", 1, "anime-1", "", "", "", "winning-release", []DisplacedTarget{{File: "ep.mkv"}}, OriginalSource{})
	if err != nil {
		t.Fatal(err)
	}
	payload = nil
	if err := json.Unmarshal(untitled.Payload, &payload); err != nil || payload["anime.title"] != nil || payload["anime.group"] != nil {
		t.Fatalf("absent values must stay absent: %v %v", payload, err)
	}
}

// The displacement notice names the victim placement as its source, the file
// that lost its place, and the taker beside it.
func TestFileDisplacedRequest(t *testing.T) {
	request, err := FileDisplacedRequest("plc-1", 2, "anime-1", "Show", "/library/Show/EP01.mkv", "redownload",
		"/library/Show/EP01.new.mkv", "GroupB", "[GroupB] Show - 01 [1080p]", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if request.TriggerKind != TriggerFileDisplaced || request.SourceOwnerType != OwnerPlacement ||
		request.SourceOwnerID != "plc-1" || request.SourceOwnerVersion != 2 || request.ManagedAnimeID != "anime-1" ||
		request.ResultKey != FileDisplacedResultKey {
		t.Fatalf("request = %+v", request)
	}
	var payload map[string]any
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"anime.id": "anime-1", "anime.title": "Show", "file.name": "EP01.mkv", "event.cause": "redownload",
		"taker.file": "EP01.new.mkv", "taker.group": "GroupB", "taker.title": "[GroupB] Show - 01 [1080p]",
	}
	if len(payload) != len(want) {
		t.Fatalf("payload = %v", payload)
	}
	for key, value := range want {
		if payload[key] != value {
			t.Fatalf("payload[%s] = %v, want %v", key, payload[key], value)
		}
	}
	anonymous, err := FileDisplacedRequest("plc-1", 2, "anime-1", "", "/library/Show/EP01.mkv", "overwrite", "/library/Show/EP02.mkv", "", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload = nil
	if err := json.Unmarshal(anonymous.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["taker.group"]; ok {
		t.Fatalf("an unrecorded taker group must not be sent empty: %v", payload)
	}
	if _, ok := payload["taker.title"]; ok {
		t.Fatalf("an unrecorded taker title must not be sent empty: %v", payload)
	}
}
