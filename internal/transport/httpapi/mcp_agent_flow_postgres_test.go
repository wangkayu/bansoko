package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	targetpersistence "bansoko/internal/acquisition/target/adapter/persistence"
	"bansoko/internal/acquisition/torrent"
	animepersistence "bansoko/internal/anime/adapter/persistence"
	animeapplication "bansoko/internal/anime/application"
	draftapplication "bansoko/internal/anime/draft/application"
	oneshotapplication "bansoko/internal/anime/oneshot/application"
	catalogpersistence "bansoko/internal/catalog/adapter/persistence"
	catalogapplication "bansoko/internal/catalog/application"
	configurationapplication "bansoko/internal/configuration/application"
	configurationdomain "bansoko/internal/configuration/domain"
	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/parserpolicy"
	providerdomain "bansoko/internal/provider/domain"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/workitems"
)

// MCP-only end-to-end proofs for the agent provider flows: every step is a
// tools/call, the agent never holds a download, feed or torrent URL, and no tool
// output carries one (the HTTP responses for the same reads do, which is what
// makes the absence a redaction and not a missing fixture).

const (
	mcpFlowGroupRef = "678"
	mcpFlowSecret   = "mcpflowpasskey"
	mcpFlowSeedKey  = "mcp-flow-seed-01"
	mcpFlowTitle    = "[Group] MCP Flow Show - 01 [1080p]"
	mcpFlowTorrent  = "https://torrent.example.test/mcp-flow-01.torrent?passkey=" + mcpFlowSecret
)

type mcpFlowGroupReader struct{}

func (mcpFlowGroupReader) ReadGroups(_ context.Context, entryRef string) (providerdomain.GroupReadResult, error) {
	observed := time.Now().UTC().Truncate(time.Microsecond)
	active := true
	group := providerdomain.GroupRef{Provider: providerdomain.ProviderMikan, ExternalID: mcpFlowGroupRef}
	return providerdomain.GroupReadResult{
		Provider: providerdomain.ProviderMikan, CatalogEntryRef: entryRef, ObservedAt: observed,
		SourceVersion: "mikan-source-1", SchemaVersion: "mikan-detail-groups-v1",
		Complete: true, CompletenessEvidence: "mikan-detail-group-blocks-v1",
		Groups: []providerdomain.GroupSnapshotEntry{{
			Ref: group, DisplayName: "Group " + mcpFlowGroupRef, ObservedAt: observed, Active: &active,
			FeedLocator: providerdomain.FeedLocator{Provider: providerdomain.ProviderMikan, CatalogEntryRef: entryRef, Group: group},
		}},
	}, nil
}

// mcpFlowConnections lists no provider connection, so discovery falls back to
// the default public endpoint and the HTTP result carries a real feed_url.
type mcpFlowConnections struct{}

func (mcpFlowConnections) ListConnections(context.Context) ([]configurationdomain.Connection, error) {
	return nil, nil
}

type mcpFlowTorrentFetcher struct {
	payload animeapplication.TorrentPayload
}

func (f mcpFlowTorrentFetcher) FetchPayload(context.Context, string) (animeapplication.TorrentPayload, error) {
	return f.payload, nil
}

// mcpFlowFeedItem is one published seed whose torrent the fetcher retained, as
// the acquisition fetcher delivers it to the catalog.
func mcpFlowFeedItem(payload animeapplication.TorrentPayload) acquisitiondomain.FeedItem {
	item := acquisitiondomain.FeedItem{
		GUID: mcpFlowSeedKey, Title: mcpFlowTitle, Enclosure: mcpFlowTorrent,
		Published:       time.Now().UTC().Truncate(time.Microsecond),
		TorrentPayload:  append([]byte(nil), payload.Bytes...),
		TorrentVersion:  payload.Metadata.TorrentVersion,
		TorrentFilename: payload.Metadata.TorrentName, TorrentSizeBytes: payload.Metadata.TotalSize,
		EnclosureLength:   payload.Metadata.TotalSize,
		TorrentInfoHashV1: payload.Metadata.InfoHashV1, TorrentInfoHashV2: payload.Metadata.InfoHashV2,
		TorrentInfoHash: payload.Metadata.InfoHashV1,
	}
	if item.TorrentInfoHash == "" {
		item.TorrentInfoHash = payload.Metadata.InfoHashV2
	}
	for _, file := range payload.Metadata.Files {
		size := file.SizeBytes
		item.ReleaseFiles = append(item.ReleaseFiles, acquisitiondomain.ReleaseFile{
			SourcePath: file.Path, Filename: file.Name, SizeBytes: &size, MediaKind: file.MediaKind, SourceHash: item.TorrentInfoHash,
		})
	}
	return item
}

// newMCPAgentFlowRig is the httpapi rig plus the collaborators the agent flows
// need, wired like cmd/bansoko-server: catalog discovery and feed snapshots, a fully
// configured draft service, and the one-shot service reading seeds from the
// persisted snapshots.
func newMCPAgentFlowRig(t *testing.T) *testRig {
	t.Helper()
	rig := newTestRig(t, "development", true)
	db := rig.db
	animetest.InstallRegistries()
	animetest.PublishRegistries(t, db)
	animetest.SeedQbConnection(t, db)

	data := animetest.ValidTestTorrentBytes()
	meta, err := torrent.Parse(data)
	if err != nil {
		t.Fatalf("parse test torrent: %v", err)
	}
	payload := animeapplication.TorrentPayload{Bytes: data, Metadata: meta}

	rig.catalog.SetGroupReaders(map[string]catalogapplication.GroupReader{"mikan": mcpFlowGroupReader{}})
	rig.catalog.SetProviderConnectionSource(mcpFlowConnections{})
	rig.catalog.SetProviderFeedFetcher(&stubCatalogFeedFetcher{items: []acquisitiondomain.FeedItem{mcpFlowFeedItem(payload)}})
	rig.catalog.SetFeedSnapshotStore(catalogpersistence.NewRepository(db))

	store, err := workitems.NewStore(db, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := draftapplication.NewService(db, draftapplication.Dependencies{WorkItems: store})
	if err != nil {
		t.Fatal(err)
	}
	parser := parserpolicy.NewService(db)
	draft.SetFrozenParserPolicy(parser)
	activity, err := observabilityapplication.NewService(db)
	if err != nil {
		t.Fatal(err)
	}
	draft.SetActivityWriter(activity)
	provisioner := animeapplication.NewProvisioner(parser)
	oneShot, err := oneshotapplication.NewService(db, oneshotapplication.Dependencies{
		SeedPreviews:       animeapplication.SeedPreviewSourceFromProviderFeedSnapshot(rig.catalog.ReadProviderEntryRSSByIdentity, rig.catalog.ReadProviderEntrySeedPayload),
		CandidateRevisions: draft,
		OneShotMarkers:     animepersistence.NewRepository(db),
		Provisioner:        &provisioner,
		TorrentFetcher:     mcpFlowTorrentFetcher{payload: payload},
		TargetStore:        acquisitiontarget.NewStore(targetpersistence.NewRepository(db)),
		TargetProbe:        acquisitiontarget.FilesystemProbe{},
		WorkItems:          store,
	})
	if err != nil {
		t.Fatal(err)
	}

	deps := rig.deps
	deps.Catalog = rig.catalog
	deps.Draft = draft
	deps.OneShot = oneShot
	deps.GlobalSections = configurationapplication.NewService(db, nil)
	server, err := NewServer(deps)
	if err != nil {
		t.Fatal(err)
	}
	rig.deps = deps
	rig.handler = server.Handler()
	return rig
}

// mcpFlowCall calls one tool and returns its structured result, failing the
// test on a protocol error or a tool error. Every tool output is checked for
// URL leaks before it is decoded.
func mcpFlowCall(t *testing.T, rig *testRig, tool string, args map[string]any, forbidden ...string) map[string]any {
	t.Helper()
	body := mcpCallTool(t, rig, tool, args)
	assertNoMCPURL(t, tool, body, forbidden...)
	structured, isError, text := decodeMCPToolResult(t, tool, body)
	if isError {
		t.Fatalf("%s returned a tool error: %s", tool, text)
	}
	return structured
}

// mcpFlowCallError calls one tool that must fail and returns the error text.
func mcpFlowCallError(t *testing.T, rig *testRig, tool string, args map[string]any, forbidden ...string) string {
	t.Helper()
	body := mcpCallTool(t, rig, tool, args)
	assertNoMCPURL(t, tool, body, forbidden...)
	_, isError, text := decodeMCPToolResult(t, tool, body)
	if !isError {
		t.Fatalf("%s must fail: %s", tool, body)
	}
	return text
}

func decodeMCPToolResult(t *testing.T, tool string, body []byte) (map[string]any, bool, string) {
	t.Helper()
	raw := body
	// The streamable transport may answer with one SSE event.
	if index := bytes.Index(raw, []byte("data: ")); index >= 0 && !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		raw = raw[index+len("data: "):]
		if end := bytes.IndexByte(raw, '\n'); end >= 0 {
			raw = raw[:end]
		}
	}
	var envelope struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			Structured map[string]any `json:"structuredContent"`
			IsError    bool           `json:"isError"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("%s: decode JSON-RPC response %s: %v", tool, body, err)
	}
	if envelope.Error != nil || envelope.Result == nil {
		t.Fatalf("%s: JSON-RPC error: %s", tool, body)
	}
	texts := make([]string, 0, len(envelope.Result.Content))
	for _, content := range envelope.Result.Content {
		texts = append(texts, content.Text)
	}
	return envelope.Result.Structured, envelope.Result.IsError, strings.Join(texts, "\n")
}

// assertNoMCPURL fails when a tool output carries the seed's torrent URL, its
// passkey, any withheld URL field, or one of the extra forbidden values.
func assertNoMCPURL(t *testing.T, tool string, body []byte, forbidden ...string) {
	t.Helper()
	for _, value := range append([]string{mcpFlowSecret, "torrent.example.test", `"download_url"`, `"feed_url"`, `"locator"`, `"expected_payload_digest"`, `stable_item_key"`, `"identity_value"`}, forbidden...) {
		if value != "" && bytes.Contains(body, []byte(value)) {
			t.Fatalf("%s output leaked %q: %s", tool, value, body)
		}
	}
}

func mcpFlowString(t *testing.T, value map[string]any, path ...string) string {
	t.Helper()
	var current any = value
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%v: %v is not an object in %v", path, key, value)
		}
		current = object[key]
	}
	switch typed := current.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	}
	t.Fatalf("%v is %T (%v), want a string or number", path, current, current)
	return ""
}

func mcpFlowFirst(t *testing.T, value map[string]any, key string) map[string]any {
	t.Helper()
	list, _ := value[key].([]any)
	if len(list) == 0 {
		t.Fatalf("%s is empty in %v", key, value)
	}
	first, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("%s[0] is not an object in %v", key, value)
	}
	return first
}

// mcpFlowDraft is the provider draft an agent builds: the flow's group pinned
// with a manual selection, every rules section at its code-default copy. It
// still has to go through load_business_preset before preview, save or
// admission.
func mcpFlowDraft(entryRef string) map[string]any {
	return map[string]any{
		"origin":           "mikan",
		"origin_entry_ref": entryRef,
		"title":            "MCP Flow Show",
		"metadata": map[string]any{
			"bgm":  map[string]any{"work_id": "bgm-mcp-flow"},
			"tmdb": map[string]any{"work_id": "tmdb-mcp-flow", "media_kind": "tv", "season": 1},
		},
		"subgroups": map[string]any{
			"max_rss": 3,
			"entries": []any{map[string]any{
				"provider": "mikan", "group_ref": mcpFlowGroupRef, "display_name": "Group " + mcpFlowGroupRef,
				"selection_mode": "manual", "manual_selected": true, "score": 50,
				"effective_selected": true,
			}},
		},
		"rules":       defaultRulesPayload(),
		"rss_sources": []any{},
	}
}

// mcpFlowSnapshot runs discovery, refresh and the pure snapshot read over MCP
// and returns the snapshot plus the chosen seed. It also proves the HTTP
// responses for the same reads carry the URLs the tool outputs withhold.
func mcpFlowSnapshot(t *testing.T, rig *testRig, entryRef string) (map[string]any, map[string]any) {
	t.Helper()
	discovered := mcpFlowCall(t, rig, "discover_feed_groups", map[string]any{"provider": "mikan", "provider_entry_ref": entryRef})
	group := mcpFlowFirst(t, discovered, "groups")
	if mcpFlowString(t, group, "group_ref") != mcpFlowGroupRef {
		t.Fatalf("discovered group = %v, want %s", group, mcpFlowGroupRef)
	}
	recorder, body := rig.do(t, http.MethodPost, "/api/v1/catalog/providers/mikan/entries/"+entryRef+"/groups:discover", nil, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP discovery = %d %s", recorder.Code, body)
	}
	if !bytes.Contains(body, []byte(`"feed_url":"http`)) {
		t.Fatalf("the HTTP discovery result must keep feed_url: %s", body)
	}

	refreshed := mcpFlowCall(t, rig, "refresh_feed_snapshot", map[string]any{"provider": "mikan", "provider_entry_ref": entryRef, "group_ref": mcpFlowGroupRef})
	if mcpFlowString(t, refreshed, "freshness") != "fresh" {
		t.Fatalf("refreshed snapshot = %v, want fresh", refreshed)
	}
	snapshot := mcpFlowCall(t, rig, "get_feed_snapshot", map[string]any{"provider": "mikan", "provider_entry_ref": entryRef, "group_ref": mcpFlowGroupRef})
	for _, key := range []string{"snapshot_id", "snapshot_version", "content_hash"} {
		if mcpFlowString(t, snapshot, key) != mcpFlowString(t, refreshed, key) {
			t.Fatalf("get_feed_snapshot %s = %v, refresh returned %v", key, snapshot[key], refreshed[key])
		}
	}
	seed := mcpFlowFirst(t, snapshot, "seeds")
	if mcpFlowString(t, seed, "stable_seed_key") != mcpFlowSeedKey || mcpFlowString(t, seed, "title") != mcpFlowTitle {
		t.Fatalf("seed = %v", seed)
	}
	for _, key := range []string{"published_at", "size_bytes", "info_hash"} {
		if _, ok := seed[key]; !ok {
			t.Fatalf("the agent seed view must keep %s: %v", key, seed)
		}
	}
	recorder, body = rig.do(t, http.MethodGet, "/api/v1/catalog/providers/mikan/entries/"+entryRef+"/rss?group_ref="+mcpFlowGroupRef, nil, nil)
	if recorder.Code != http.StatusOK || !bytes.Contains(body, []byte(mcpFlowSecret)) {
		t.Fatalf("the HTTP snapshot read must keep download_url: %d %s", recorder.Code, body)
	}
	return snapshot, seed
}

// mcpFlowLoadBusiness runs the documented draft step: the agent's draft goes
// through load_business_preset, which returns it with the Global business
// snapshot and its digest. The pinned group must survive the load.
func mcpFlowLoadBusiness(t *testing.T, rig *testRig, entryRef string) map[string]any {
	t.Helper()
	draft := mcpFlowCall(t, rig, "load_business_preset", map[string]any{"draft": mcpFlowDraft(entryRef)})
	if draft["business"] == nil || mcpFlowString(t, draft, "business_digest") == "" {
		t.Fatalf("load_business_preset returned no business snapshot: %v", draft)
	}
	subgroups, _ := draft["subgroups"].(map[string]any)
	entries, _ := subgroups["entries"].([]any)
	pinned := false
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry["group_ref"] == mcpFlowGroupRef && entry["effective_selected"] == true {
			pinned = true
		}
	}
	if !pinned {
		t.Fatalf("the loaded draft lost the pinned group: %v", subgroups)
	}
	return draft
}

// Provider subscription: discover groups, read the feed snapshot, load the
// business into the draft, save a candidate, preview it with the snapshot
// identity, activate it.
func TestMCPAgentProviderSubscriptionFlowWithholdsURLs(t *testing.T) {
	rig := newMCPAgentFlowRig(t)
	const entryRef = "80901"
	snapshot, _ := mcpFlowSnapshot(t, rig, entryRef)

	draft := mcpFlowLoadBusiness(t, rig, entryRef)
	candidate := mcpFlowCall(t, rig, "create_candidate", map[string]any{"draft": draft})
	candidateID := mcpFlowString(t, candidate, "id")
	revision := candidate["current_revision"]
	if revision == nil {
		t.Fatalf("candidate has no revision: %v", candidate)
	}

	view := mcpFlowCall(t, rig, "rss_list_view", map[string]any{
		"input_source": "candidate_revision", "candidate_id": candidateID, "revision": revision,
		"feed": []any{map[string]any{
			"provider": "mikan", "provider_entry_ref": entryRef, "provider_group_ref": mcpFlowGroupRef,
			"snapshot_id": snapshot["snapshot_id"], "snapshot_version": snapshot["snapshot_version"],
			"content_hash": snapshot["content_hash"],
		}},
	})
	group := mcpFlowFirst(t, view, "groups")
	if mcpFlowString(t, group, "feed", "state") != "fresh" {
		t.Fatalf("preview group = %v, want the fresh snapshot", group)
	}
	item := mcpFlowFirst(t, group, "items")
	if mcpFlowString(t, item, "seed", "title") != mcpFlowTitle {
		t.Fatalf("preview item = %v", item)
	}

	activated := mcpFlowCall(t, rig, "activate_candidate", map[string]any{
		"candidate_id": candidateID, "revision": revision, "expected_version": candidate["version"],
		"idempotency_key": "mcp-agent-provider-flow-0001",
	})
	animeID := mcpFlowString(t, activated, "managed_anime", "id")
	var linked string
	if err := rig.db.QueryRowContext(context.Background(), `SELECT activated_managed_anime_id FROM anime_config_candidates WHERE id = $1`, candidateID).Scan(&linked); err != nil || linked != animeID {
		t.Fatalf("candidate activation link = %q, %v; want %s", linked, err, animeID)
	}
}

// One-shot: pick a seed from the snapshot, admit it by reference, read the
// acquisition. The agent sends no locator and the server resolves it.
func TestMCPAgentOneShotFlowAdmitsASeedByReference(t *testing.T) {
	rig := newMCPAgentFlowRig(t)
	const entryRef = "80902"
	snapshot, seed := mcpFlowSnapshot(t, rig, entryRef)
	draft := mcpFlowLoadBusiness(t, rig, entryRef)
	feed := []any{map[string]any{
		"provider": "mikan", "provider_entry_ref": entryRef, "provider_group_ref": mcpFlowGroupRef,
		"snapshot_id": snapshot["snapshot_id"], "snapshot_version": snapshot["snapshot_version"],
		"content_hash": snapshot["content_hash"],
	}}
	preview := mcpFlowCall(t, rig, "rss_list_view", map[string]any{"input_source": "current_input", "draft": draft, "feed": feed})
	if item := mcpFlowFirst(t, mcpFlowFirst(t, preview, "groups"), "items"); mcpFlowString(t, item, "seed", "title") != mcpFlowTitle {
		t.Fatalf("current_input preview item = %v", item)
	}
	reference := map[string]any{
		"provider": "mikan", "provider_entry_ref": entryRef, "provider_group_ref": mcpFlowGroupRef,
		"snapshot_id": snapshot["snapshot_id"], "snapshot_version": snapshot["snapshot_version"],
		"snapshot_content_hash": snapshot["content_hash"], "stable_seed_key": seed["stable_seed_key"],
	}
	args := map[string]any{
		"input_source": "current_input", "draft": draft,
		"selected_seed": reference, "idempotency_key": "mcp-agent-one-shot-flow-0001",
	}
	admitted := mcpFlowCall(t, rig, "acquire_seed_once", args)
	oneShotID := mcpFlowString(t, admitted, "one_shot_id")
	if replayed := mcpFlowCall(t, rig, "acquire_seed_once", args); mcpFlowString(t, replayed, "one_shot_id") != oneShotID {
		t.Fatalf("replayed reference = %v, want %s", replayed, oneShotID)
	}

	view := mcpFlowCall(t, rig, "get_one_shot_acquisition", map[string]any{"one_shot_id": oneShotID})
	if mcpFlowString(t, view, "input_snapshot", "selected_seed", "stable_seed_key") != mcpFlowSeedKey {
		t.Fatalf("acquisition input snapshot = %v", view["input_snapshot"])
	}
	recorder, body := rig.do(t, http.MethodGet, "/api/v1/one-shot-acquisitions/"+oneShotID, nil, nil)
	if recorder.Code != http.StatusOK || !bytes.Contains(body, []byte(mcpFlowSecret)) {
		t.Fatalf("the HTTP acquisition read must keep the resolved locator: %d %s", recorder.Code, body)
	}

	// A seed key outside the snapshot fails closed with the stale code and no
	// writes.
	markers := animetest.CountRows(t, rig.db, "one_shot_acquisitions")
	unknown := map[string]any{}
	for key, value := range reference {
		unknown[key] = value
	}
	unknown["stable_seed_key"] = "mcp-flow-seed-missing"
	text := mcpFlowCallError(t, rig, "acquire_seed_once", map[string]any{
		"input_source": "current_input", "draft": draft,
		"selected_seed": unknown, "idempotency_key": "mcp-agent-one-shot-flow-0002",
	})
	if !strings.Contains(text, "SEED_PREVIEW_STALE") {
		t.Fatalf("unknown seed key error = %s, want SEED_PREVIEW_STALE", text)
	}
	if got := animetest.CountRows(t, rig.db, "one_shot_acquisitions"); got != markers {
		t.Fatalf("a refused reference wrote a one-shot: %d -> %d", markers, got)
	}
}
