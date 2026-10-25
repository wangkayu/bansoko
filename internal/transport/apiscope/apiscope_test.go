package apiscope

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func load(t *testing.T) *Table {
	t.Helper()
	table, err := Load()
	if err != nil {
		t.Fatalf("load scope table: %v", err)
	}
	return table
}

// Every operation declares a scope Parse accepts; a missing or unknown x-scope
// fails the whole table, so a server built from this contract cannot start.
func TestEveryOperationDeclaresAScope(t *testing.T) {
	table := load(t)
	counts := map[string]int{}
	var secrets []string
	for _, route := range table.Routes() {
		counts[route.Scope]++
		if route.Scope == ScopeSecrets {
			secrets = append(secrets, route.OperationID)
		}
	}
	if counts[ScopeRead] == 0 || counts[ScopeWrite] == 0 || counts[ScopeNone] == 0 {
		t.Fatalf("unexpected scope distribution %v", counts)
	}
	// The secrets scope guards the raw token read and the database backup
	// export, browser download ticket and upload (a backup holds every stored
	// secret). Restore is further limited to web sessions by its handler.
	sort.Strings(secrets)
	if want := []string{"exportBackup", "prepareBackupDownload", "readApiTokenRaw", "restoreBackup"}; !reflect.DeepEqual(secrets, want) {
		t.Fatalf("secrets-scope operations = %q, want %q", secrets, want)
	}
	if _, err := Parse([]byte("openapi: 3.1.0\npaths:\n  /x:\n    get:\n      operationId: x\n")); err == nil {
		t.Fatal("an operation without x-scope must be refused")
	}
	if _, err := Parse([]byte("openapi: 3.1.0\npaths:\n  /x:\n    get:\n      operationId: x\n      x-scope: none\n")); err == nil {
		t.Fatal("x-scope none on an authenticated operation must be refused")
	}
	if _, err := Parse([]byte("openapi: 3.1.0\npaths:\n  /x:\n    get:\n      operationId: x\n      security: []\n      x-scope: read\n")); err == nil {
		t.Fatal("an anonymous operation must declare x-scope none")
	}
}

// The x-secret properties are pinned: adding one is a reviewed contract change
// (it changes what a bearer without the secrets scope reads).
func TestSecretPropertiesArePinned(t *testing.T) {
	want := []string{
		"FrozenDownloadInput.release_stable_item_key",
		"FrozenDownloadInput.source_locator_display",
		"ProviderGroupDiscoveryGroup.feed_url",
		"ProviderRSSPreviewSeed.download_url",
		"RSSCursor.value",
		"RSSLastGoodFacts.newest_stable_item_key",
		"RSSListSeedFacts.locator_display",
		"RSSSourceDraft.locator",
		"Release.stable_item_key",
		"SeedSearchSnapshotSeed.download_url",
		"SelectedReleaseIdentity.stable_item_key",
		"SelectedSeed.locator",
		"SourceFacts.identity_value",
	}
	if got := load(t).SecretProperties(); !reflect.DeepEqual(got, want) {
		t.Fatalf("x-secret properties changed:\n got %q\nwant %q", got, want)
	}
}

// Every MCP-withheld URL-bearing property is also a secret for REST, so a token
// without the secrets scope reads no passkey-bearing value on either surface.
// Only the payload digests are MCP-omitted without being secret.
func TestOmittedURLPropertiesAreSecret(t *testing.T) {
	table := load(t)
	components, _ := table.Document()["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	for name, raw := range schemas {
		schema, _ := raw.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		for property, node := range props {
			fields, _ := node.(map[string]any)
			if fields["x-mcp-omit"] != true || strings.Contains(property, "digest") {
				continue
			}
			secret := fields["x-secret"] == true
			if ref, ok := fields["$ref"].(string); ok && !secret {
				target := table.lookup(ref)
				targetProps, _ := target["properties"].(map[string]any)
				for _, child := range targetProps {
					if childFields, _ := child.(map[string]any); childFields["x-secret"] == true {
						secret = true
					}
				}
			}
			if !secret {
				t.Errorf("%s.%s is x-mcp-omit but not x-secret", name, property)
			}
		}
	}
}

// The operations whose responses can carry an x-secret value are pinned.
func TestSecretRevealingOperationsArePinned(t *testing.T) {
	var got []string
	for _, route := range load(t).Routes() {
		if route.RevealsSecrets() {
			got = append(got, route.OperationID)
		}
	}
	sort.Strings(got)
	want := []string{
		"discoverProviderEntryGroups",
		"fetchCustomSnapshot",
		"fetchOwnerCustomSnapshot",
		"fetchSeedSearchSnapshot",
		"getAnimeBusinessEdit",
		"getAnimeConfigCandidate",
		"getAnimeWorkspace",
		"getOneShotAcquisition",
		"getOneShotExecutionAttempt",
		"listAnimeConfigCandidates",
		"loadBusinessPresetIntoDraft",
		"patchAnimeConfigCandidate",
		"previewProviderEntryRSS",
		"readAnimeRSSListView",
		"readProviderEntryRSS",
		"readSeedSearchSnapshot",
		"saveAnimeConfigCandidate",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("secret-revealing operations changed:\n got %q\nwant %q", got, want)
	}
}

func TestMatchFindsTheServingOperation(t *testing.T) {
	table := load(t)
	cases := []struct {
		method, path, operation, scope string
	}{
		{http.MethodPost, "/api/v1/anime/rss-list-view", "readAnimeRSSListView", ScopeRead},
		{http.MethodGet, "/api/v1/anime-config-candidates/c-1", "getAnimeConfigCandidate", ScopeRead},
		{http.MethodPatch, "/api/v1/anime-config-candidates/c-1", "patchAnimeConfigCandidate", ScopeWrite},
		{http.MethodHead, "/api/v1/anime-config-candidates", "listAnimeConfigCandidates", ScopeRead},
		{http.MethodGet, "/api/v1/api-tokens/t-1/raw", "readApiTokenRaw", ScopeSecrets},
		{http.MethodGet, "/api/v1/backup/export", "exportBackup", ScopeSecrets},
		{http.MethodPost, "/api/v1/backup/restore", "restoreBackup", ScopeSecrets},
		{http.MethodPost, "/api/v1/backup/download-ticket", "prepareBackupDownload", ScopeSecrets},
		{http.MethodGet, "/api/v1/backup/download", "downloadPreparedBackup", ScopeNone},
		{http.MethodPost, "/api/v1/api-tokens", "createApiToken", ScopeWrite},
		{http.MethodPost, "/api/v1/auth/login", "login", ScopeNone},
		{http.MethodPost, "/api/v1/config/business-preset/load", "loadBusinessPresetIntoDraft", ScopeRead},
		{http.MethodPost, "/api/v1/plugin-targets/p-1/test", "testPluginTarget", ScopeWrite},
		{http.MethodPost, "/api/v1/config/connections/c%2F1/test", "testConnection", ScopeRead},
		// Percent-encoded literals reach the same handler through ServeMux.
		{http.MethodPatch, "/api/v1/anime-config-c%61ndidates/c-1", "patchAnimeConfigCandidate", ScopeWrite},
		{http.MethodDelete, "/api/v1/api-t%6Fkens/t-1", "revokeApiToken", ScopeWrite},
	}
	for _, tc := range cases {
		route, ok := table.Match(tc.method, tc.path)
		if !ok {
			t.Errorf("%s %s: no operation", tc.method, tc.path)
			continue
		}
		if route.OperationID != tc.operation || route.Scope != tc.scope {
			t.Errorf("%s %s: got %s/%s, want %s/%s", tc.method, tc.path, route.OperationID, route.Scope, tc.operation, tc.scope)
		}
	}
	for _, path := range []string{"/api/v1/mcp", "/api/v1/does-not-exist", "/api/v1/anime-config-candidates/c-1/extra/deep"} {
		if route, ok := table.Match(http.MethodPost, path); ok {
			t.Errorf("POST %s must not match an operation, got %s", path, route.OperationID)
		}
	}
}

func TestMaskSecretsReplacesEveryMarkedValue(t *testing.T) {
	table := load(t)
	route, _ := table.Match(http.MethodGet, "/api/v1/anime-config-candidates/c-1")
	body := `{"id":"c-1","version":3,"draft":{"origin":"custom","title":"T","rss_sources":[` +
		`{"source_kind":"custom_rss","locator":"https://pt.example/rss?passkey=abc","locator_display":"https://pt.example","group_name":"g","title":"t","episode_offset":0},` +
		`{"source_kind":"custom_rss","locator":"https://pt.example/rss/key/xyz","locator_display":null,"group_name":"g","title":"t","episode_offset":0}]}}`
	masked, changed, err := table.MaskSecrets(route, http.StatusOK, []byte(body))
	if err != nil || !changed {
		t.Fatalf("mask: changed=%v err=%v", changed, err)
	}
	text := string(masked)
	for _, leaked := range []string{"passkey=abc", "/key/xyz"} {
		if strings.Contains(text, leaked) {
			t.Fatalf("masked body still carries %q: %s", leaked, text)
		}
	}
	var decoded struct {
		Version json.Number `json:"version"`
		Draft   struct {
			RssSources []struct {
				Locator        string  `json:"locator"`
				LocatorDisplay *string `json:"locator_display"`
			} `json:"rss_sources"`
		} `json:"draft"`
	}
	if err := json.Unmarshal(masked, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version.String() != "3" || len(decoded.Draft.RssSources) != 2 {
		t.Fatalf("masking changed the shape: %s", text)
	}
	for _, source := range decoded.Draft.RssSources {
		if source.Locator != SecretPlaceholder {
			t.Fatalf("locator = %q, want the placeholder", source.Locator)
		}
	}
	if display := decoded.Draft.RssSources[0].LocatorDisplay; display == nil || *display != "https://pt.example" {
		t.Fatalf("the credential-free display must stay: %v", display)
	}

	// Error bodies and operations that reveal nothing pass unchanged.
	if _, changed, _ := table.MaskSecrets(route, http.StatusNotFound, []byte(`{"code":"NOT_FOUND","message":"x"}`)); changed {
		t.Fatal("an error body has no secret schema")
	}
	login, _ := table.Match(http.MethodPost, "/api/v1/auth/login")
	if login.RevealsSecrets() {
		t.Fatal("login reveals no secret field")
	}
	if _, _, err := table.MaskSecrets(route, http.StatusOK, []byte("not json")); err == nil {
		t.Fatal("an undecodable body must not pass through")
	}
}

func TestMaskSecretsFollowsNestedSchemas(t *testing.T) {
	table := load(t)
	route, _ := table.Match(http.MethodGet, "/api/v1/catalog/providers/mikan/entries/1/rss")
	if !route.RevealsSecrets() {
		t.Fatal("the stored feed snapshot carries download URLs")
	}
	body := `{"seeds":[{"download_url":"https://pt.example/dl/1?passkey=abc","title":"x","stable_seed_key":"k"}]}`
	masked, changed, err := table.MaskSecrets(route, http.StatusOK, []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(masked), "passkey") {
		t.Fatalf("download_url leaked (changed=%v): %s", changed, masked)
	}
}
