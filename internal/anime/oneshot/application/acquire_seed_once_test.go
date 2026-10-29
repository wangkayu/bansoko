package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	acquisitiontorrent "bansoko/internal/acquisition/torrent"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	animeport "bansoko/internal/anime/port"
	"bansoko/internal/apperrors"
	catalogapplication "bansoko/internal/catalog/application"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/parser"
	"bansoko/internal/testutil/animetest"
	"bansoko/internal/transport/httpapi/generated"
)

// Focused pure unit tests for the finite one-shot seed admission contract.
// They intentionally require no database: PostgreSQL integration coverage
// (admission atomicity, replay under lock, marker transitions) is owned by the
// main session's serial PG lane.

func validOneShotSeed() SelectedSeed {
	infoHash := strings.Repeat("a", 40)
	digest := strings.Repeat("b", 64)
	return SelectedSeed{
		Provider:              animedomain.ProviderKindAnibt,
		ProviderEntryRef:      "12345",
		ProviderGroupRef:      "Lilith-Raws",
		Locator:               "https://anibt.example/download/12345",
		Title:                 "[Lilith-Raws] Example S01 [Baha][WebDL 1080p]",
		PublishedAt:           time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC),
		SizeBytes:             animetest.Int64Ptr(1024),
		InfoHash:              &infoHash,
		ExpectedPayloadDigest: &digest,
	}
}

// fakeTorrentMetadata fabricates parsed torrent metadata so strong-identity
// comparison is testable without bencode fixtures.
func fakeTorrentMetadata(v1Hash, v2Hash string) acquisitiontorrent.Metadata {
	return acquisitiontorrent.Metadata{
		InfoHashV1:     v1Hash,
		InfoHashV2:     v2Hash,
		TorrentName:    "Example Torrent",
		TorrentVersion: "v1",
	}
}

func humanProviderDraft(t *testing.T, origin animedomain.SubscriptionOrigin, entryRef string) animedomain.AnimeDraft {
	t.Helper()
	generatedDraft := generated.AnimeDraft{
		Origin:         generated.SubscriptionOrigin(origin),
		OriginEntryRef: &entryRef,
		Title:          "Example Anime",
		Metadata:       generated.AnimeMetadataDraft{},
		Subgroups:      generated.SubgroupsSection{Entries: []generated.SubgroupScoreEntry{}},
		Rules:          oneShotDefaultRules(t),
	}
	draft, err := domainDraft(generatedDraft)
	if err != nil {
		t.Fatalf("map draft: %v", err)
	}
	return draft
}

func TestEncodeOneShotInputSnapshotPreservesCompleteInput(t *testing.T) {
	url := "https://torrent.example.test/seed.torrent"
	seed := validOneShotSeed()
	seed.Locator = url
	seed.SnapshotID = "snapshot-1"
	seed.SnapshotVersion = 7
	seed.SnapshotContentHash = "sha256:" + strings.Repeat("c", 64)
	seed.StableSeedKey = "stable-1"
	candidateID := "candidate-1"
	revision := int64(7)
	parserEvidence := parser.DescribeProvenance(parser.ParserRegex, "config-fingerprint")
	raw, err := encodeOneShotInputSnapshot(OneShotInputSnapshot{
		CandidateID:       candidateID,
		Revision:          revision,
		SelectedSeed:      seed,
		EffectiveSections: json.RawMessage(`{"rules":{"parser":{"mode":"replace"}}}`),
		SectionVersions:   map[string]int64{"rules.parser": 3},
		SectionSchemas:    map[string]string{"rules.parser": "v2"},
		EffectiveDigest:   "sha256:" + strings.Repeat("d", 64),
		ConfigIdentity:    animeapplication.ConfigRevisionIdentity{CandidateID: &candidateID, Revision: &revision, EffectiveDigest: "sha256:" + strings.Repeat("d", 64), IdentityKind: "candidate_revision"},
		Parser:            &parserEvidence,
		TargetFacts:       json.RawMessage(`{"configured":true}`),
	})
	if err != nil {
		t.Fatalf("encode snapshot: %v", err)
	}
	var decoded OneShotInputSnapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if decoded.CandidateID != candidateID || decoded.Revision != revision || decoded.SelectedSeed.Title != seed.Title || decoded.SelectedSeed.Locator != url || decoded.SelectedSeed.SnapshotID != seed.SnapshotID || decoded.SelectedSeed.StableSeedKey != seed.StableSeedKey || decoded.EffectiveDigest == "" || decoded.ConfigIdentity.IdentityKind != "candidate_revision" || decoded.Parser == nil || decoded.Parser.Kind != string(parser.ParserRegex) {
		t.Fatalf("snapshot lost complete input: %+v", decoded)
	}
	if decoded.EffectiveSections == nil || decoded.TargetFacts == nil || decoded.SectionVersions["rules.parser"] != 3 || decoded.SectionSchemas["rules.parser"] != "v2" {
		t.Fatalf("snapshot lost frozen configuration facts: %+v", decoded)
	}
	if len(raw) > maxOneShotInputSnapshotBytes || raw[0] != '{' {
		t.Fatalf("snapshot shape/size invalid: %d bytes", len(raw))
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("decode snapshot wire shape: %v", err)
	}
	for _, field := range []string{"candidate_id", "revision", "effective_sections", "section_versions", "section_schemas", "effective_digest", "config_identity", "parser", "target_facts"} {
		if _, ok := wire[field]; !ok {
			t.Fatalf("snapshot is missing %q", field)
		}
	}
	selectedRaw, ok := wire["selected_seed"]
	if !ok {
		t.Fatal("snapshot must use the public selected_seed field")
	}
	var selectedWire map[string]json.RawMessage
	if err := json.Unmarshal(selectedRaw, &selectedWire); err != nil {
		t.Fatalf("decode selected seed wire shape: %v", err)
	}
	for _, field := range []string{"locator", "snapshot_id", "snapshot_version", "snapshot_content_hash", "stable_seed_key"} {
		if _, ok := selectedWire[field]; !ok {
			t.Fatalf("snapshot selected_seed is missing %q", field)
		}
	}
	if _, ok := selectedWire["Locator"]; ok {
		t.Fatal("snapshot selected_seed must not expose Go field names")
	}
	canonicalSeed, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal canonical seed: %v", err)
	}
	var canonicalWire map[string]json.RawMessage
	if err := json.Unmarshal(canonicalSeed, &canonicalWire); err != nil {
		t.Fatalf("decode canonical seed: %v", err)
	}
	if _, ok := canonicalWire["Locator"]; !ok {
		t.Fatal("canonical selected seed JSON shape unexpectedly changed")
	}
	if _, ok := canonicalWire["locator"]; ok {
		t.Fatal("canonical selected seed JSON must remain independent of snapshot tags")
	}
}

func TestEncodeOneShotInputSnapshotRejectsOversize(t *testing.T) {
	candidateID := "candidate-1"
	revision := int64(1)
	_, err := encodeOneShotInputSnapshot(OneShotInputSnapshot{
		CandidateID:       candidateID,
		Revision:          revision,
		SelectedSeed:      validOneShotSeed(),
		EffectiveSections: json.RawMessage(`{"rules":{"naming":"` + strings.Repeat("x", maxOneShotInputSnapshotBytes) + `"}}`),
		ConfigIdentity:    animeapplication.ConfigRevisionIdentity{CandidateID: &candidateID, Revision: &revision, IdentityKind: "candidate_revision"},
	})
	// An over-bound snapshot is the typed business refusal, never an
	// untyped error the HTTP boundary would answer as INTERNAL.
	var business *apperrors.Business
	if !errors.As(err, &business) || business.Status != 422 || business.Code != "TORRENT_TOO_LARGE" {
		t.Fatalf("oversized snapshot error = %v, want the 422 TORRENT_TOO_LARGE refusal", err)
	}
}

func TestValidateSelectedSeedAcceptsCompleteSeed(t *testing.T) {
	if err := validateSelectedSeed(validOneShotSeed()); err != nil {
		t.Fatalf("complete seed rejected: %v", err)
	}
	// custom is a seed-provenance provider, not a discovery provider: a seed
	// read from a custom draft's frozen feed passes the same structural
	// contract as an anibt/mikan seed.
	custom := validOneShotSeed()
	custom.Provider = animedomain.ProviderKindCustom
	if err := validateSelectedSeed(custom); err != nil {
		t.Fatalf("custom seed rejected: %v", err)
	}
}

func TestValidateSelectedSeedRejectsStructuralViolations(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*SelectedSeed)
		code string
	}{
		{"unknown provider", func(s *SelectedSeed) { s.Provider = "unknown-provider" }, "SEED_INVALID"},
		{"empty entry ref", func(s *SelectedSeed) { s.ProviderEntryRef = " " }, "SEED_INVALID"},
		{"oversized entry ref", func(s *SelectedSeed) { s.ProviderEntryRef = strings.Repeat("x", 257) }, "SEED_INVALID"},
		{"empty group ref", func(s *SelectedSeed) { s.ProviderGroupRef = "" }, "SEED_INVALID"},
		{"empty title", func(s *SelectedSeed) { s.Title = "" }, "SEED_INVALID"},
		{"zero published", func(s *SelectedSeed) { s.PublishedAt = time.Time{} }, "SEED_INVALID"},
		{"negative size", func(s *SelectedSeed) { s.SizeBytes = animetest.Int64Ptr(-1) }, "SEED_INVALID"},
		{"unsafe locator scheme", func(s *SelectedSeed) { s.Locator = "ftp://anibt.example/download" }, "SEED_LOCATOR_UNSAFE"},
		{"private locator host", func(s *SelectedSeed) { s.Locator = "http://127.0.0.1:8080/torrent" }, "SEED_LOCATOR_UNSAFE"},
		{"short info hash", func(s *SelectedSeed) { s.InfoHash = animetest.StringPtr("abcd") }, "SEED_INVALID"},
		{"non-hex info hash", func(s *SelectedSeed) { s.InfoHash = animetest.StringPtr(strings.Repeat("z", 40)) }, "SEED_INVALID"},
		{"bad payload digest", func(s *SelectedSeed) { s.ExpectedPayloadDigest = animetest.StringPtr("nothex") }, "SEED_INVALID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := validOneShotSeed()
			tc.mut(&seed)
			err := validateSelectedSeed(seed)
			business, ok := err.(*apperrors.Business)
			if !ok {
				t.Fatalf("expected typed business error, got %v", err)
			}
			if business.Code != tc.code {
				t.Fatalf("code=%q, want %q", business.Code, tc.code)
			}
		})
	}
}

func TestSeedHasStrongIdentity(t *testing.T) {
	seed := validOneShotSeed()
	seed.InfoHash = nil
	seed.ExpectedPayloadDigest = nil
	if seedHasStrongIdentity(seed) {
		t.Fatal("seed without any strong identity must not pass")
	}
	seed.InfoHash = animetest.StringPtr(strings.Repeat("a", 40))
	if !seedHasStrongIdentity(seed) {
		t.Fatal("info_hash alone is a sufficient strong identity")
	}
	blank := ""
	seed.InfoHash = &blank
	if seedHasStrongIdentity(seed) {
		t.Fatal("a blank info_hash is not a strong identity")
	}
}

func TestVerifySeedStrongIdentities(t *testing.T) {
	payloadBytes := []byte("d4:infod4:name6:example")
	digest := animetest.SHA256Hex(payloadBytes)
	v1Hash := strings.Repeat("1", 40)
	v2Hash := strings.Repeat("2", 64)
	payload := animeapplication.TorrentPayload{
		Bytes:    payloadBytes,
		Metadata: fakeTorrentMetadata(v1Hash, v2Hash),
	}

	seed := validOneShotSeed()
	seed.InfoHash = animetest.StringPtr(v2Hash)
	seed.ExpectedPayloadDigest = animetest.StringPtr(digest)
	if err := verifySeedStrongIdentities(payload, seed); err != nil {
		t.Fatalf("matching identities rejected: %v", err)
	}

	seed.InfoHash = animetest.StringPtr(v1Hash)
	if err := verifySeedStrongIdentities(payload, seed); err != nil {
		t.Fatalf("v1 identity match rejected: %v", err)
	}

	seed.InfoHash = animetest.StringPtr(strings.Repeat("9", 40))
	err := verifySeedStrongIdentities(payload, seed)
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "SEED_IDENTITY_MISMATCH" {
		t.Fatalf("infohash mismatch not typed: %v", err)
	}

	seed.InfoHash = animetest.StringPtr(v1Hash)
	seed.ExpectedPayloadDigest = animetest.StringPtr(strings.Repeat("f", 64))
	err = verifySeedStrongIdentities(payload, seed)
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "SEED_IDENTITY_MISMATCH" {
		t.Fatalf("digest mismatch not typed: %v", err)
	}

	// A locator redirected to different content fails as an identity mismatch:
	// every supplied strong identity must match the refetched payload. A
	// sha256:-prefixed uppercase digest normalizes to the same identity.
	seed.ExpectedPayloadDigest = animetest.StringPtr("sha256:" + strings.ToUpper(digest))
	if err := verifySeedStrongIdentities(payload, seed); err != nil {
		t.Fatalf("normalized digest should match: %v", err)
	}
}

func TestValidateAcquireSeedCallerMatrix(t *testing.T) {
	seed := validOneShotSeed()
	savedRevisions := AcquireSeedInput{CandidateID: "candidate-1", Revision: 3}

	human := AcquireSeedCaller{Kind: AcquireSeedCallerHuman}
	if err := validateAcquireSeedCaller(human, humanProviderDraft(t, animedomain.SubscriptionOriginAnibt, seed.ProviderEntryRef), seed, savedRevisions); err != nil {
		t.Fatalf("human provider draft rejected: %v", err)
	}

	err := validateAcquireSeedCaller(human, humanProviderDraft(t, animedomain.SubscriptionOriginMikan, seed.ProviderEntryRef), seed, savedRevisions)
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "ONE_SHOT_CALLER_INVALID" {
		t.Fatalf("provider mismatch not typed: %v", err)
	}

	wrongEntry := humanProviderDraft(t, animedomain.SubscriptionOriginAnibt, "99999")
	err = validateAcquireSeedCaller(human, wrongEntry, seed, savedRevisions)
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "ONE_SHOT_CALLER_INVALID" {
		t.Fatalf("entry-ref mismatch not typed: %v", err)
	}

	// A human caller never carries the agent form: an origin=oneshot draft is
	// admitted only through the agent caller with an authorization snapshot.
	err = validateAcquireSeedCaller(human, agentDraftWithLabel(t), seed, savedRevisions)
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "ONE_SHOT_CALLER_INVALID" {
		t.Fatalf("human agent-form draft not typed: %v", err)
	}

	agentDraft := agentDraftWithLabel(t)
	err = validateAcquireSeedCaller(AcquireSeedCaller{Kind: AcquireSeedCallerAgent}, agentDraft, seed, savedRevisions)
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "AGENT_AUTHORIZATION_REQUIRED" {
		t.Fatalf("agent without authorization snapshot must be rejected: %v", err)
	}

	authorized := AcquireSeedCaller{Kind: AcquireSeedCallerAgent, Agent: &AgentSeedAuthorization{GrantedBy: "mcp-adapter", AuthorizationRef: "auth-123"}}
	if err := validateAcquireSeedCaller(authorized, agentDraft, seed, savedRevisions); err != nil {
		t.Fatalf("authorized agent snapshot rejected: %v", err)
	}

	// An agent caller cannot smuggle a provider draft past the binding matrix,
	// and an oversized authorization reference is rejected.
	err = validateAcquireSeedCaller(authorized, humanProviderDraft(t, animedomain.SubscriptionOriginMikan, seed.ProviderEntryRef), seed, savedRevisions)
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "ONE_SHOT_CALLER_INVALID" {
		t.Fatalf("agent caller with provider draft not typed: %v", err)
	}
	oversized := AcquireSeedCaller{Kind: AcquireSeedCallerAgent, Agent: &AgentSeedAuthorization{GrantedBy: "mcp-adapter", AuthorizationRef: strings.Repeat("x", 257)}}
	err = validateAcquireSeedCaller(oversized, agentDraft, seed, savedRevisions)
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "AGENT_AUTHORIZATION_REQUIRED" {
		t.Fatalf("oversized authorization reference not rejected: %v", err)
	}
}

// TestValidateHumanCustomSeedBinding pins the custom one-shot admission terms:
// a saved-revision seed must belong to the revision's own frozen feed, whose
// scope is derived from the exact candidate revision identity; a current-input
// admission (the one-shot's own unsaved configuration) binds any custom seed,
// whose persisted snapshot is checked separately.
func TestValidateHumanCustomSeedBinding(t *testing.T) {
	draft := oneShotCustomDraft(t)
	// A custom draft has no origin_entry_ref (domain-enforced); the binding
	// identity is derived, never supplied.
	draft.OriginEntryRef = nil
	if violations := animedomain.ValidateDraft(draft); len(violations) > 0 {
		t.Fatalf("custom draft fixture is not valid: %v", violations)
	}
	source := draft.RssSources[0]
	entryRef, groupRef := catalogapplication.CustomSnapshotScope("candidate-1#3", source.Locator, source.GroupName)

	customSeed := func() SelectedSeed {
		seed := validOneShotSeed()
		seed.Provider = animedomain.ProviderKindCustom
		seed.ProviderEntryRef = entryRef
		seed.ProviderGroupRef = groupRef
		return seed
	}
	human := AcquireSeedCaller{Kind: AcquireSeedCallerHuman}
	savedRevision := AcquireSeedInput{CandidateID: "candidate-1", Revision: 3}

	if err := validateSelectedSeed(customSeed()); err != nil {
		t.Fatalf("custom seed rejected by the structural contract: %v", err)
	}
	if err := validateAcquireSeedCaller(human, draft, customSeed(), savedRevision); err != nil {
		t.Fatalf("custom seed from the draft's own feed rejected: %v", err)
	}

	assertStale := func(label string, err error) {
		t.Helper()
		if business, ok := err.(*apperrors.Business); !ok || business.Code != "SEED_PREVIEW_STALE" || business.Status != 422 {
			t.Fatalf("%s not a typed SEED_PREVIEW_STALE 422: %v", label, err)
		}
	}

	// A seed from another custom feed is not this draft's seed.
	otherSource := customSeed()
	otherSource.ProviderEntryRef = "custom-source:" + strings.Repeat("0", 64)
	assertStale("foreign custom source", validateAcquireSeedCaller(human, draft, otherSource, savedRevision))

	otherGroup := customSeed()
	otherGroup.ProviderGroupRef = "custom-group:" + strings.Repeat("0", 64)
	assertStale("foreign custom group", validateAcquireSeedCaller(human, draft, otherGroup, savedRevision))

	// The same feed under a different revision identity is a different scope.
	assertStale("different revision", validateAcquireSeedCaller(human, draft, customSeed(), AcquireSeedInput{CandidateID: "candidate-1", Revision: 4}))

	// A custom draft never binds a discovery-provider seed.
	assertStale("provider seed for a custom draft", validateAcquireSeedCaller(human, draft, validOneShotSeed(), savedRevision))

	// A current-input admission is the one-shot's own unsaved configuration:
	// no saved scope applies, so any custom seed binds (its persisted snapshot
	// proves it exists), while a provider seed still does not.
	currentInput := AcquireSeedInput{InputSource: AcquireSeedInputSourceCurrentInput, Draft: &draft}
	if err := validateAcquireSeedCaller(human, draft, otherSource, currentInput); err != nil {
		t.Fatalf("current input custom seed rejected: %v", err)
	}
	assertStale("current input provider seed", validateAcquireSeedCaller(human, draft, validOneShotSeed(), currentInput))

	// The agent form is unchanged: an agent still needs its own draft and
	// authorization snapshot, and never gains a custom binding shortcut.
	if err := validateAcquireSeedCaller(AcquireSeedCaller{Kind: AcquireSeedCallerAgent}, draft, customSeed(), savedRevision); err == nil {
		t.Fatal("agent caller with a custom draft must be rejected")
	}
}

func TestCanonicalOneShotRequestHash(t *testing.T) {
	input := AcquireSeedInput{
		CandidateID:     "candidate-1",
		Revision:        3,
		EffectiveDigest: "sha256:" + strings.Repeat("d", 64),
		Seed:            validOneShotSeed(),
		Caller:          AcquireSeedCaller{Kind: AcquireSeedCallerHuman},
	}
	first, err := canonicalOneShotRequestHash(input, "0123456789abcdef")
	if err != nil {
		t.Fatalf("hash failed: %v", err)
	}
	second, err := canonicalOneShotRequestHash(input, "0123456789abcdef")
	if err != nil {
		t.Fatalf("hash failed: %v", err)
	}
	if first != second {
		t.Fatal("same canonical request must produce the same hash")
	}

	changed := input
	changed.Seed.Locator += "?different=1"
	third, err := canonicalOneShotRequestHash(changed, "0123456789abcdef")
	if err != nil {
		t.Fatalf("hash failed: %v", err)
	}
	if first == third {
		t.Fatal("a changed selected seed must change the canonical hash")
	}
}

func TestOneShotReplayDecision(t *testing.T) {
	result := OneShotAdmissionResult{
		OneShotID:      "osh-1",
		State:          "running",
		ManagedAnimeID: "anime-1",
		SubscriptionID: "sub-1",
		RSSSourceID:    "src-1",
		ReleaseID:      "rel-1",
		DownloadTaskID: animetest.StringPtr("task-1"),
	}
	snapshot, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	record := animeport.OneShotMarkerRecord{
		ID:             result.OneShotID,
		ManagedAnimeID: result.ManagedAnimeID,
		SubscriptionID: result.SubscriptionID,
		RSSSourceID:    result.RSSSourceID,
		ReleaseID:      result.ReleaseID,
		DownloadTaskID: result.DownloadTaskID,
		RequestHash:    "sha256:aaa",
		ResultSnapshot: snapshot,
	}

	replayed, err := oneShotReplayDecision(record, "sha256:aaa")
	if err != nil {
		t.Fatalf("same-body replay rejected: %v", err)
	}
	if replayed.OneShotID != result.OneShotID || replayed.DownloadTaskID == nil || *replayed.DownloadTaskID != *result.DownloadTaskID {
		t.Fatal("replay must return the original handle verbatim")
	}

	_, err = oneShotReplayDecision(record, "sha256:bbb")
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("changed body under same key must be a conflict: %v", err)
	}

	tampered := record
	tampered.ReleaseID = "other-release"
	_, err = oneShotReplayDecision(tampered, "sha256:aaa")
	if business, ok := err.(*apperrors.Business); !ok || business.Code != "IDEMPOTENCY_KEY_REUSED" {
		t.Fatalf("anchor/snapshot inconsistency must be a conflict: %v", err)
	}
}

func TestIsTemporaryOneShotMount(t *testing.T) {
	// origin=oneshot + state=stopped alone never qualifies...
	if IsTemporaryOneShotMount("oneshot", "stopped", false) {
		t.Fatal("persistent Agent skeleton must never be a temporary mount")
	}
	if IsTemporaryOneShotMount("custom", "stopped", false) {
		t.Fatal("stopped persistent subscription must never be a temporary mount")
	}
	// ...only the marker authorizes reclamation of its own graph.
	if !IsTemporaryOneShotMount("oneshot", "stopped", true) {
		t.Fatal("marker-created mount must qualify")
	}
}

func TestOneShotInstanceLabelBoundsAndDerives(t *testing.T) {
	label := "my-agent-label"
	draft := agentDraftWithLabel(t)
	draft.InstanceLabel = &label
	if got := oneShotInstanceLabel(draft); got != label {
		t.Fatalf("label=%q, want %q", got, label)
	}

	derived := oneShotInstanceLabel(humanProviderDraft(t, animedomain.SubscriptionOriginAnibt, "1"))
	if derived == "" || len([]rune(derived)) > 256 {
		t.Fatalf("derived label must be non-empty and bounded: %q", derived)
	}

	long := humanProviderDraft(t, animedomain.SubscriptionOriginAnibt, "1")
	long.Title = strings.Repeat("x", 400)
	if got := len([]rune(oneShotInstanceLabel(long))); got > 256 {
		t.Fatalf("derived label exceeds 256 runes: %d", got)
	}
}

func TestValidateDraftAllowsAgentSnapshotWithoutSources(t *testing.T) {
	draft := agentDraftWithLabel(t)
	if violations := animedomain.ValidateDraft(draft); len(violations) != 0 {
		t.Fatalf("agent snapshot draft invalid: %+v", violations)
	}
}

func oneShotCustomDraft(t *testing.T) animedomain.AnimeDraft {
	t.Helper()
	locator := "https://pt.example/rss?passkey=secret"
	label := "Custom"
	draft := humanProviderDraft(t, animedomain.SubscriptionOriginCustom, "")
	draft.InstanceLabel = &label
	draft.RssSources = []animedomain.RSSSourceDraft{{
		SourceKind: animedomain.SourceKindCustomRss,
		Locator:    locator,
		GroupName:  "group",
		Title:      "title",
	}}
	return draft
}

func agentDraftWithLabel(t *testing.T) animedomain.AnimeDraft {
	t.Helper()
	label := "agent-mount"
	draft := humanProviderDraft(t, animedomain.SubscriptionOriginOneshot, "")
	draft.OriginEntryRef = nil
	draft.InstanceLabel = &label
	return draft
}

// --- Human seed-preview binding (T5) -----------------------------------------

type fakeSeedPreviewSource struct {
	snapshot     animeapplication.SeedPreviewSnapshot
	err          error
	calls        int
	payload      animeapplication.SeedPayload
	payloadErr   error
	payloadCalls int
	lastIdentity animecatalogdomain.ProviderRSSPreviewIdentity
	lastProvider string
	lastEntryRef string
	lastGroupRef string
	lastSeedKey  string
}

func (f *fakeSeedPreviewSource) FetchEntryGroupSnapshotByID(_ context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity) (animeapplication.SeedPreviewSnapshot, error) {
	f.calls++
	f.lastIdentity = identity
	f.lastProvider, f.lastEntryRef, f.lastGroupRef = provider, entryRef, groupRef
	return f.snapshot, f.err
}

func (f *fakeSeedPreviewSource) ReadSeedPayload(_ context.Context, provider, entryRef, groupRef string, identity animecatalogdomain.ProviderRSSPreviewIdentity, stableSeedKey string) (animeapplication.SeedPayload, error) {
	f.payloadCalls++
	f.lastIdentity = identity
	f.lastProvider, f.lastEntryRef, f.lastGroupRef = provider, entryRef, groupRef
	f.lastSeedKey = stableSeedKey
	return f.payload, f.payloadErr
}

func boundOneShotSeed() SelectedSeed {
	seed := validOneShotSeed()
	seed.SnapshotID = "snap-1"
	seed.SnapshotVersion = 3
	seed.SnapshotContentHash = "sha256:" + strings.Repeat("c", 64)
	seed.StableSeedKey = "seed-01"
	return seed
}

func matchingSeedPreviewSnapshot(seed SelectedSeed) animeapplication.SeedPreviewSnapshot {
	fact := animeapplication.SeedPreviewFact{
		DownloadURL:   seed.Locator,
		InfoHash:      optionalStringValue(seed.InfoHash),
		PublishedAt:   seed.PublishedAt,
		SizeBytes:     seed.SizeBytes,
		StableSeedKey: seed.StableSeedKey,
		Title:         seed.Title,
	}
	if seed.ExpectedPayloadDigest != nil {
		fact.ExpectedPayloadDigest = *seed.ExpectedPayloadDigest
	}
	return animeapplication.SeedPreviewSnapshot{
		Provider:         string(seed.Provider),
		ProviderEntryRef: seed.ProviderEntryRef,
		ProviderGroupRef: seed.ProviderGroupRef,
		SnapshotID:       seed.SnapshotID,
		SnapshotVersion:  seed.SnapshotVersion,
		ContentHash:      seed.SnapshotContentHash,
		Freshness:        "fresh",
		Seeds:            []animeapplication.SeedPreviewFact{fact},
	}
}

func TestVerifyHumanSeedPreviewBinding(t *testing.T) {
	seed := boundOneShotSeed()
	cases := []struct {
		name     string
		source   *fakeSeedPreviewSource
		mut      func(*SelectedSeed)
		wantCode string
	}{
		{name: "fresh matching snapshot admits", source: &fakeSeedPreviewSource{snapshot: matchingSeedPreviewSnapshot(boundOneShotSeed())}},
		{name: "stale snapshot rejected", source: &fakeSeedPreviewSource{snapshot: func() animeapplication.SeedPreviewSnapshot {
			snap := matchingSeedPreviewSnapshot(boundOneShotSeed())
			snap.Freshness = "stale"
			return snap
		}()}, wantCode: "SEED_PREVIEW_STALE"},
		{name: "missing freshness rejected", source: &fakeSeedPreviewSource{snapshot: func() animeapplication.SeedPreviewSnapshot {
			snap := matchingSeedPreviewSnapshot(boundOneShotSeed())
			snap.Freshness = "missing"
			return snap
		}()}, wantCode: "SEED_PREVIEW_STALE"},
		{name: "snapshot id mismatch rejected", source: &fakeSeedPreviewSource{snapshot: func() animeapplication.SeedPreviewSnapshot {
			snap := matchingSeedPreviewSnapshot(boundOneShotSeed())
			snap.SnapshotID = "snap-other"
			return snap
		}()}, wantCode: "SEED_PREVIEW_STALE"},
		{name: "snapshot version mismatch rejected", source: &fakeSeedPreviewSource{snapshot: func() animeapplication.SeedPreviewSnapshot {
			snap := matchingSeedPreviewSnapshot(boundOneShotSeed())
			snap.SnapshotVersion = 4
			return snap
		}()}, wantCode: "SEED_PREVIEW_STALE"},
		{name: "content hash mismatch rejected", source: &fakeSeedPreviewSource{snapshot: func() animeapplication.SeedPreviewSnapshot {
			snap := matchingSeedPreviewSnapshot(boundOneShotSeed())
			snap.ContentHash = "sha256:" + strings.Repeat("d", 64)
			return snap
		}()}, wantCode: "SEED_PREVIEW_STALE"},
		{name: "seed absent from snapshot rejected", source: &fakeSeedPreviewSource{snapshot: func() animeapplication.SeedPreviewSnapshot {
			snap := matchingSeedPreviewSnapshot(boundOneShotSeed())
			snap.Seeds = nil
			return snap
		}()}, wantCode: "SEED_PREVIEW_STALE"},
		{name: "seed identity drift rejected", source: &fakeSeedPreviewSource{snapshot: func() animeapplication.SeedPreviewSnapshot {
			snap := matchingSeedPreviewSnapshot(boundOneShotSeed())
			snap.Seeds[0].Title = "Different Title"
			return snap
		}()}, wantCode: "SEED_PREVIEW_STALE"},
		{name: "source failure fails closed", source: &fakeSeedPreviewSource{err: errors.New("store down")}, wantCode: "SEED_PREVIEW_UNAVAILABLE"},
		{name: "exact identity missing fails closed as stale", source: &fakeSeedPreviewSource{err: fmt.Errorf("%w: purged", animeapplication.ErrProviderFeedSnapshotRefreshRequired)}, wantCode: "SEED_PREVIEW_STALE"},
		{name: "missing snapshot identity rejected before fetch", source: &fakeSeedPreviewSource{}, mut: func(s *SelectedSeed) { s.SnapshotID = "" }, wantCode: "SEED_PREVIEW_STALE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current := seed
			if tc.mut != nil {
				tc.mut(&current)
			}
			service := &Service{seedPreviews: tc.source}
			_, err := service.verifyHumanSeedPreviewBinding(context.Background(), current)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("expected admission, got %v", err)
				}
				return
			}
			business, ok := err.(*apperrors.Business)
			if !ok || business.Code != tc.wantCode {
				t.Fatalf("error = %v, want code %q", err, tc.wantCode)
			}
		})
	}

	t.Run("digest-only seed binds on the canonical wire form", func(t *testing.T) {
		seed := boundOneShotSeed()
		seed.InfoHash = nil
		canonical := "sha256:" + strings.Repeat("e", 64)
		seed.ExpectedPayloadDigest = &canonical
		source := &fakeSeedPreviewSource{snapshot: matchingSeedPreviewSnapshot(seed)}
		service := &Service{seedPreviews: source}
		if _, err := service.verifyHumanSeedPreviewBinding(context.Background(), seed); err != nil {
			t.Fatalf("digest-only binding rejected: %v", err)
		}
	})

	t.Run("bare hex digest normalizes against the canonical snapshot fact", func(t *testing.T) {
		seed := boundOneShotSeed()
		seed.InfoHash = nil
		canonical := "sha256:" + strings.Repeat("f", 64)
		seed.ExpectedPayloadDigest = &canonical
		snapshot := matchingSeedPreviewSnapshot(seed)
		snapshot.Seeds[0].ExpectedPayloadDigest = strings.Repeat("f", 64)
		source := &fakeSeedPreviewSource{snapshot: snapshot}
		service := &Service{seedPreviews: source}
		if _, err := service.verifyHumanSeedPreviewBinding(context.Background(), seed); err != nil {
			t.Fatalf("normalized digest binding rejected: %v", err)
		}
	})

	t.Run("tampered digest rejected", func(t *testing.T) {
		seed := boundOneShotSeed()
		seed.InfoHash = nil
		other := seed
		tampered := "sha256:" + strings.Repeat("9", 64)
		other.ExpectedPayloadDigest = &tampered
		source := &fakeSeedPreviewSource{snapshot: matchingSeedPreviewSnapshot(other)}
		service := &Service{seedPreviews: source}
		if _, err := service.verifyHumanSeedPreviewBinding(context.Background(), seed); err == nil {
			t.Fatal("a tampered digest must be rejected")
		}
	})

	t.Run("missing snapshot identity performs no fetch", func(t *testing.T) {
		source := &fakeSeedPreviewSource{}
		service := &Service{seedPreviews: source}
		current := boundOneShotSeed()
		current.SnapshotVersion = 0
		if _, err := service.verifyHumanSeedPreviewBinding(context.Background(), current); err == nil {
			t.Fatal("expected rejection for zero snapshot version")
		}
		if source.calls != 0 {
			t.Fatalf("source fetched %d times for an identity-less seed", source.calls)
		}
	})

	t.Run("reads the exact declared snapshot identity, never latest", func(t *testing.T) {
		seed := boundOneShotSeed()
		source := &fakeSeedPreviewSource{snapshot: matchingSeedPreviewSnapshot(seed)}
		service := &Service{seedPreviews: source}
		if _, err := service.verifyHumanSeedPreviewBinding(context.Background(), seed); err != nil {
			t.Fatalf("expected admission, got %v", err)
		}
		if source.calls != 1 {
			t.Fatalf("source calls = %d, want 1", source.calls)
		}
		if source.lastIdentity.SnapshotID != seed.SnapshotID || source.lastIdentity.SnapshotVersion != seed.SnapshotVersion || source.lastIdentity.ContentHash != seed.SnapshotContentHash {
			t.Fatalf("source identity = %+v, want the seed's exact identity", source.lastIdentity)
		}
		if source.lastProvider != string(seed.Provider) || source.lastEntryRef != seed.ProviderEntryRef || source.lastGroupRef != seed.ProviderGroupRef {
			t.Fatalf("source scope = %s/%s/%s, want the seed scope", source.lastProvider, source.lastEntryRef, source.lastGroupRef)
		}
	})

	t.Run("unconfigured source fails closed", func(t *testing.T) {
		service := &Service{}
		_, err := service.verifyHumanSeedPreviewBinding(context.Background(), boundOneShotSeed())
		business, ok := err.(*apperrors.Business)
		if !ok || business.Code != "SEED_PREVIEW_UNAVAILABLE" {
			t.Fatalf("error = %v, want SEED_PREVIEW_UNAVAILABLE", err)
		}
	})
}

// The canonical wire form of a strong payload identity is `sha256:<64 hex>`.
// A legacy bare-hex input stays accepted at the boundary but is internalized
// before it can participate in replay identity or stored evidence.
func TestCanonicalSelectedSeedNormalizesTheDigestWireForm(t *testing.T) {
	hex64 := strings.Repeat("a", 64)
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "canonical passthrough", in: "sha256:" + hex64, want: "sha256:" + hex64},
		{name: "legacy bare hex", in: strings.Repeat("b", 64), want: "sha256:" + strings.Repeat("b", 64)},
		{name: "uppercase prefix and hex", in: "SHA256:" + strings.Repeat("C", 64), want: "sha256:" + strings.Repeat("c", 64)},
		{name: "surrounding whitespace", in: "  " + strings.Repeat("d", 64) + "  ", want: "sha256:" + strings.Repeat("d", 64)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := test.in
			got := canonicalSelectedSeed(SelectedSeed{ExpectedPayloadDigest: &value})
			if got.ExpectedPayloadDigest == nil || *got.ExpectedPayloadDigest != test.want {
				t.Fatalf("digest = %v, want %q", got.ExpectedPayloadDigest, test.want)
			}
		})
	}
	blank := "   "
	if got := canonicalSelectedSeed(SelectedSeed{ExpectedPayloadDigest: &blank}); got.ExpectedPayloadDigest != nil {
		t.Fatalf("blank digest must clear the optional identity, got %q", *got.ExpectedPayloadDigest)
	}
	if got := canonicalSelectedSeed(SelectedSeed{}); got.ExpectedPayloadDigest != nil {
		t.Fatal("an absent digest must stay absent")
	}
}

// An adopted one-shot has no download task on either side of the replay
// comparison; a marker that claims one while the stored result says null is an
// inconsistent replay record.
func TestOneShotReplayDecisionAcceptsAdoptedHandle(t *testing.T) {
	result := OneShotAdmissionResult{
		OneShotID:      "osh-adopted",
		State:          string(OneShotAcquisitionStateReclaimPending),
		ManagedAnimeID: "anime-1",
		SubscriptionID: "sub-1",
		RSSSourceID:    "src-1",
		ReleaseID:      "rel-1",
	}
	snapshot, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal adopted snapshot: %v", err)
	}
	record := animeport.OneShotMarkerRecord{
		ID:             result.OneShotID,
		ManagedAnimeID: result.ManagedAnimeID,
		SubscriptionID: result.SubscriptionID,
		RSSSourceID:    result.RSSSourceID,
		ReleaseID:      result.ReleaseID,
		RequestHash:    "sha256:aaa",
		ResultSnapshot: snapshot,
	}
	replayed, err := oneShotReplayDecision(record, "sha256:aaa")
	if err != nil {
		t.Fatalf("adopted replay rejected: %v", err)
	}
	if replayed.DownloadTaskID != nil {
		t.Fatalf("adopted replay fabricated a download task: %v", *replayed.DownloadTaskID)
	}
	if replayed.State != string(OneShotAcquisitionStateReclaimPending) {
		t.Fatalf("adopted replay state = %q, want reclaim_pending", replayed.State)
	}
	taskID := "task-inconsistent"
	record.DownloadTaskID = &taskID
	if _, err := oneShotReplayDecision(record, "sha256:aaa"); err == nil {
		t.Fatal("marker/result download-task disagreement must be a conflict")
	}
}
