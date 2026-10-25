package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	terminal "bansoko/internal/acquisition/terminal/application"
	torrentparser "bansoko/internal/acquisition/torrent"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	animeport "bansoko/internal/anime/port"
	"bansoko/internal/apperrors"
	"bansoko/internal/attrrules"
	catalogapplication "bansoko/internal/catalog/application"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/events"
	"bansoko/internal/namingvars"
	"bansoko/internal/parser"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/postprocessing/naming"
	rssapplication "bansoko/internal/rss/application"
	"bansoko/internal/workitems"
)

// Finite one-shot seed acquisition (design: AcquireSeedOnce). One command
// downloads exactly one selected seed from an exact saved candidate revision
// as one finite acquisition cycle. It atomically creates a temporary
// origin=oneshot, state=stopped mount, one placeholder custom_rss source,
// exactly one selected seed Release + torrent input, one DownloadTask whose
// inputs freeze before asynchronous work begins, the
// one_shot_acquisitions marker owning the request replay record, and only
// that seed's qb_submit WorkItem. It never creates provider-group selections,
// ordinary RSS runtimes, periodic refresh work, or an ordinary
// release_evaluate WorkItem, and it never touches a persistent Agent skeleton
// or an existing stopped Subscription.

// oneShotReasonCode marks every Release created by this command so the
// placeholder source stays identifiable, mirroring reason_code="injected".
const oneShotReasonCode = "one_shot"

// oneShotCompletedReasonCode is the marker's terminal success reason code,
// shared with the retention drain so an adopted and a downloaded chain report
// the same terminal vocabulary.
const oneShotCompletedReasonCode = "one_shot_completed"

// oneShotAdoptedReason is the adopted chain's terminal reason: the exact
// selected entries already existed, so nothing was downloaded.
const oneShotAdoptedReason = "one-shot acquisition adopted the existing exact target set"

// AcquireSeedCallerKind distinguishes the two authorized entry points into the
// same typed application service. It is supplied by the transport lane (HTTP
// human callers default to human; the MCP/Agent adapter passes agent plus its
// authorization snapshot); it is not part of the {candidate_id, revision, selected_seed} body.
type AcquireSeedCallerKind string

const (
	// AcquireSeedCallerHuman is the catalog draft path: the seed must bind to
	// the current authorized provider preview context.
	AcquireSeedCallerHuman AcquireSeedCallerKind = "human"
	// AcquireSeedCallerAgent is the Agent adapter path: admission relies on the
	// explicit authorization snapshot plus strong content identities and never
	// depends on a server-side human preview session.
	AcquireSeedCallerAgent AcquireSeedCallerKind = "agent"
)

// AgentSeedAuthorization carries the explicit authorization context supplied
// by the Agent adapter for one one-shot admission. An agent request without it
// is rejected before any write; the persistent Agent skeleton is never read or
// mutated by this command.
type AgentSeedAuthorization struct {
	GrantedBy        string `json:"granted_by"`
	AuthorizationRef string `json:"authorization_ref"`
}

// AcquireSeedCaller identifies which authorized entry point is calling.
type AcquireSeedCaller struct {
	Kind  AcquireSeedCallerKind   `json:"kind"`
	Agent *AgentSeedAuthorization `json:"agent,omitempty"`
}

// SelectedSeed intentionally keeps its default JSON field names because the
// canonical one-shot request hash predates the durable input snapshot. The
// snapshot uses its own tagged wire value below, so adding a read model does
// not change replay identity for an existing request.
type SelectedSeed struct {
	ExpectedPayloadDigest *string
	InfoHash              *string
	Locator               string
	Provider              animedomain.ProviderKind
	ProviderEntryRef      string
	ProviderGroupRef      string
	PublishedAt           time.Time
	SizeBytes             *int64
	SnapshotContentHash   string
	SnapshotID            string
	SnapshotVersion       int64
	StableSeedKey         string
	Title                 string
	// AniBTTags is the seed's frozen AniBT structured tag set, resolved from
	// the verified preview snapshot at admission. It is `json:"-"` on purpose:
	// this type's default field names are the replay identity of requests that
	// predate the durable input snapshot, so a new read model must not enter
	// that hash. The snapshot wire value below carries it instead.
	AniBTTags *acquisitiondomain.StoredAniBTTags `json:"-"`
	// SnapshotKind names the kind of snapshot the seed belongs to. It is written
	// by the server (never read from a request body) so replay identity is
	// unchanged, and it is what lets a human adapter prove the seed came from a
	// keyword search instead of a feed.
	SnapshotKind string `json:"-"`
}

const maxOneShotInputSnapshotBytes = 262144

type OneShotInputSnapshot struct {
	CandidateID  string       `json:"candidate_id"`
	Revision     int64        `json:"revision"`
	SelectedSeed SelectedSeed `json:"selected_seed"`
	// EpisodeOffset is the one-shot's own episode offset, frozen from the
	// admission request. Retry reuses it through the placeholder link.
	EpisodeOffset     int                                     `json:"episode_offset"`
	EffectiveSections json.RawMessage                         `json:"effective_sections"`
	SectionVersions   map[string]int64                        `json:"section_versions"`
	SectionSchemas    map[string]string                       `json:"section_schemas"`
	EffectiveDigest   string                                  `json:"effective_digest"`
	ConfigIdentity    animeapplication.ConfigRevisionIdentity `json:"config_identity"`
	Parser            *parser.Provenance                      `json:"parser"`
	TargetFacts       json.RawMessage                         `json:"target_facts"`
	// InputSource marks the captured config input source: "" for the legacy
	// saved-revision spelling, "current_input" for a captured unsaved draft.
	InputSource AcquireSeedInputSource `json:"input_source,omitempty"`
	// DraftDigest and Draft carry the captured unsaved draft: the digest is the
	// canonical draft hash the replay identity is computed from, and Draft is
	// the readback provenance of exactly what the user submitted. Both are
	// empty for a saved-revision admission, whose draft lives in the revision.
	DraftDigest string          `json:"draft_digest,omitempty"`
	Draft       json.RawMessage `json:"draft,omitempty"`
}

// oneShotInputSnapshotJSON is the wire form of the stored snapshot. A
// saved-revision admission always carries candidate_id+revision; a
// current_input admission emits them as JSON null so the generated nullable
// read model round-trips cleanly.
type oneShotInputSnapshotJSON struct {
	CandidateID       *string                                 `json:"candidate_id"`
	Revision          *int64                                  `json:"revision"`
	SelectedSeed      oneShotSelectedSeedJSON                 `json:"selected_seed"`
	EpisodeOffset     int                                     `json:"episode_offset"`
	EffectiveSections json.RawMessage                         `json:"effective_sections"`
	SectionVersions   map[string]int64                        `json:"section_versions"`
	SectionSchemas    map[string]string                       `json:"section_schemas"`
	EffectiveDigest   string                                  `json:"effective_digest"`
	ConfigIdentity    animeapplication.ConfigRevisionIdentity `json:"config_identity"`
	Parser            *parser.Provenance                      `json:"parser"`
	TargetFacts       json.RawMessage                         `json:"target_facts"`
	InputSource       AcquireSeedInputSource                  `json:"input_source,omitempty"`
	DraftDigest       string                                  `json:"draft_digest,omitempty"`
	Draft             json.RawMessage                         `json:"draft,omitempty"`
}

type oneShotSelectedSeedJSON struct {
	ExpectedPayloadDigest *string                  `json:"expected_payload_digest,omitempty"`
	InfoHash              *string                  `json:"info_hash,omitempty"`
	Locator               string                   `json:"locator"`
	Provider              animedomain.ProviderKind `json:"provider"`
	ProviderEntryRef      string                   `json:"provider_entry_ref"`
	ProviderGroupRef      string                   `json:"provider_group_ref"`
	PublishedAt           time.Time                `json:"published_at"`
	SizeBytes             *int64                   `json:"size_bytes,omitempty"`
	SnapshotContentHash   string                   `json:"snapshot_content_hash,omitempty"`
	SnapshotID            string                   `json:"snapshot_id,omitempty"`
	SnapshotVersion       int64                    `json:"snapshot_version,omitempty"`
	StableSeedKey         string                   `json:"stable_seed_key,omitempty"`
	Title                 string                   `json:"title"`
	// AniBTTags is a read model of the verified snapshot, not a request fact:
	// it never enters the request hash and a replay compares it by identity
	// only. `omitempty` keeps every previously frozen snapshot byte-identical.
	AniBTTags *acquisitiondomain.StoredAniBTTags `json:"anibt_tags,omitempty"`
}

func (snapshot OneShotInputSnapshot) MarshalJSON() ([]byte, error) {
	wire := oneShotInputSnapshotJSON{
		SelectedSeed:      oneShotSelectedSeedJSONFromDomain(snapshot.SelectedSeed),
		EpisodeOffset:     snapshot.EpisodeOffset,
		EffectiveSections: snapshot.EffectiveSections,
		SectionVersions:   snapshot.SectionVersions,
		SectionSchemas:    snapshot.SectionSchemas,
		EffectiveDigest:   snapshot.EffectiveDigest,
		ConfigIdentity:    snapshot.ConfigIdentity,
		Parser:            snapshot.Parser,
		TargetFacts:       snapshot.TargetFacts,
		InputSource:       snapshot.InputSource,
		DraftDigest:       snapshot.DraftDigest,
		Draft:             snapshot.Draft,
	}
	if snapshot.CandidateID != "" {
		candidateID := snapshot.CandidateID
		wire.CandidateID = &candidateID
	}
	if snapshot.Revision > 0 {
		revision := snapshot.Revision
		wire.Revision = &revision
	}
	return json.Marshal(wire)
}

func (snapshot *OneShotInputSnapshot) UnmarshalJSON(raw []byte) error {
	var value oneShotInputSnapshotJSON
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if value.CandidateID != nil {
		snapshot.CandidateID = *value.CandidateID
	}
	if value.Revision != nil {
		snapshot.Revision = *value.Revision
	}
	snapshot.SelectedSeed = value.SelectedSeed.toDomain()
	snapshot.EpisodeOffset = value.EpisodeOffset
	snapshot.EffectiveSections = append(json.RawMessage(nil), value.EffectiveSections...)
	snapshot.SectionVersions = value.SectionVersions
	snapshot.SectionSchemas = value.SectionSchemas
	snapshot.EffectiveDigest = value.EffectiveDigest
	snapshot.ConfigIdentity = value.ConfigIdentity
	snapshot.Parser = value.Parser
	snapshot.TargetFacts = append(json.RawMessage(nil), value.TargetFacts...)
	snapshot.InputSource = value.InputSource
	snapshot.DraftDigest = value.DraftDigest
	snapshot.Draft = append(json.RawMessage(nil), value.Draft...)
	return nil
}

func oneShotSelectedSeedJSONFromDomain(seed SelectedSeed) oneShotSelectedSeedJSON {
	return oneShotSelectedSeedJSON{
		ExpectedPayloadDigest: seed.ExpectedPayloadDigest,
		InfoHash:              seed.InfoHash,
		Locator:               seed.Locator,
		Provider:              seed.Provider,
		ProviderEntryRef:      seed.ProviderEntryRef,
		ProviderGroupRef:      seed.ProviderGroupRef,
		PublishedAt:           seed.PublishedAt,
		SizeBytes:             seed.SizeBytes,
		SnapshotContentHash:   seed.SnapshotContentHash,
		SnapshotID:            seed.SnapshotID,
		SnapshotVersion:       seed.SnapshotVersion,
		StableSeedKey:         seed.StableSeedKey,
		Title:                 seed.Title,
		AniBTTags:             cloneStoredAniBTTags(seed.AniBTTags),
	}
}

func (seed oneShotSelectedSeedJSON) toDomain() SelectedSeed {
	return SelectedSeed{
		ExpectedPayloadDigest: seed.ExpectedPayloadDigest,
		InfoHash:              seed.InfoHash,
		Locator:               seed.Locator,
		Provider:              seed.Provider,
		ProviderEntryRef:      seed.ProviderEntryRef,
		ProviderGroupRef:      seed.ProviderGroupRef,
		PublishedAt:           seed.PublishedAt,
		SizeBytes:             seed.SizeBytes,
		SnapshotContentHash:   seed.SnapshotContentHash,
		SnapshotID:            seed.SnapshotID,
		SnapshotVersion:       seed.SnapshotVersion,
		StableSeedKey:         seed.StableSeedKey,
		Title:                 seed.Title,
		AniBTTags:             cloneStoredAniBTTags(seed.AniBTTags),
	}
}

// cloneStoredAniBTTags keeps a frozen tag block detached from the slice it was
// read from, the same way the source-facts encoder does.
func cloneStoredAniBTTags(stored *acquisitiondomain.StoredAniBTTags) *acquisitiondomain.StoredAniBTTags {
	if stored == nil {
		return nil
	}
	clone := *stored
	clone.Tags = append([]string{}, stored.Tags...)
	return &clone
}

func encodeOneShotInputSnapshot(snapshot OneShotInputSnapshot) (json.RawMessage, error) {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal one-shot input snapshot: %w", err)
	}
	if len(raw) == 0 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return nil, errors.New("one-shot input snapshot must be a JSON object")
	}
	if len(raw) > maxOneShotInputSnapshotBytes {
		return nil, fmt.Errorf("one-shot input snapshot exceeds %d bytes", maxOneShotInputSnapshotBytes)
	}
	return raw, nil
}

// AcquireSeedInputSource discriminates the two mutually exclusive config input
// sources of one finite one-shot admission. The empty value is the legacy
// saved-revision spelling: a body with candidate_id+revision and no
// discriminator keeps its original parsing, selection and replay identity.
type AcquireSeedInputSource string

const (
	// AcquireSeedInputSourceSavedRevision admits the seed against one exact
	// saved candidate revision (explicit spelling of the legacy body).
	AcquireSeedInputSourceSavedRevision AcquireSeedInputSource = "saved_revision"
	// AcquireSeedInputSourceCurrentInput captures the complete unsaved draft the
	// request carries. No candidate row is required; the resolved effective
	// sections and the current global parser default freeze into the input
	// snapshot as the execution identity.
	AcquireSeedInputSourceCurrentInput AcquireSeedInputSource = "current_input"
)

// AcquireSeedInput is the complete one-shot command input. The config input is
// one exact saved candidate revision (legacy and explicit saved_revision
// bodies) or the complete unsaved current_input draft; the selected seed is
// admitted against that captured configuration.
type AcquireSeedInput struct {
	CandidateID string                  `json:"candidate_id,omitempty"`
	Revision    int64                   `json:"revision,omitempty"`
	InputSource AcquireSeedInputSource  `json:"input_source,omitempty"`
	Draft       *animedomain.AnimeDraft `json:"draft,omitempty"`
	Seed        SelectedSeed            `json:"selected_seed"`
	// EpisodeOffset is the one-shot's only episode offset: an explicit request
	// value (0 when omitted), never read from an existing subscription, RSS
	// link or the submitted draft. It is written to the placeholder link,
	// frozen into the input snapshot and part of the replay identity.
	EpisodeOffset   int               `json:"episode_offset,omitempty"`
	Caller          AcquireSeedCaller `json:"caller"`
	EffectiveDigest string            `json:"-"`
}

// isCurrentInput reports whether this admission captures the request's unsaved
// draft instead of a saved candidate revision.
func (input AcquireSeedInput) isCurrentInput() bool {
	return input.InputSource == AcquireSeedInputSourceCurrentInput
}

// OneShotAdmissionResult is the transport-neutral handle returned after one-shot admission.
type OneShotAdmissionResult struct {
	// DownloadTaskID is nil when the selected seed's exact target set already
	// existed and the finite command adopted it: adoption creates no
	// DownloadTask, no qB work, no Artifact and no FileOperation.
	DownloadTaskID *string `json:"download_task_id"`
	ManagedAnimeID string  `json:"managed_anime_id"`
	OneShotID      string  `json:"one_shot_id"`
	ReleaseID      string  `json:"release_id"`
	RSSSourceID    string  `json:"rss_source_id"`
	State          string  `json:"state"`
	SubscriptionID string  `json:"subscription_id"`
}

// SetSeedPreviewSource wires the authorized preview source used by human seed
// binding. Unset, human admission fails closed with SEED_PREVIEW_UNAVAILABLE.
func (s *Service) SetSeedPreviewSource(source animeapplication.SeedPreviewSource) {
	if s != nil {
		s.seedPreviews = source
	}
}

// selectedSeedHasSnapshotIdentity reports whether a selected seed names one
// exact persisted snapshot. A human row always does; an agent or legacy body
// may not, and then admission keeps the bounded fetch.
func selectedSeedHasSnapshotIdentity(seed SelectedSeed) bool {
	return strings.TrimSpace(seed.SnapshotID) != "" &&
		seed.SnapshotVersion >= 1 &&
		strings.TrimSpace(seed.SnapshotContentHash) != ""
}

// readPersistedSeedPayload reads the exact persisted torrent bytes of one
// selected seed and re-parses them in-process. The bytes are the ones the read
// model rendered, so admission performs no provider request and cannot be
// redirected to different content between review and click. A missing, stale,
// legacy or mismatching snapshot is a typed refresh-required failure with zero
// writes.
func (s *Service) readPersistedSeedPayload(ctx context.Context, seed SelectedSeed) (animeapplication.TorrentPayload, error) {
	if s.seedPreviews == nil {
		return animeapplication.TorrentPayload{}, &apperrors.Business{Status: 503, Code: "SEED_PREVIEW_UNAVAILABLE", Message: "The persisted provider seed source is not configured."}
	}
	identity := animecatalogdomain.ProviderRSSPreviewIdentity{
		SnapshotID:      seed.SnapshotID,
		SnapshotVersion: seed.SnapshotVersion,
		ContentHash:     seed.SnapshotContentHash,
	}
	var stored animeapplication.SeedPayload
	var err error
	if seed.SnapshotKind == string(animecatalogdomain.FeedSnapshotKindSearch) {
		// A search seed's bytes are reached only through the search snapshot the
		// caller expanded from; a source without the search read fails closed.
		searchSource, ok := s.seedPreviews.(animeapplication.SearchSeedPreviewSource)
		if !ok {
			return animeapplication.TorrentPayload{}, &apperrors.Business{Status: 503, Code: "SEED_PREVIEW_UNAVAILABLE", Message: "Keyword search snapshots are not served by this server."}
		}
		snapshot, fetchErr := searchSource.FetchSearchSnapshotByID(ctx, string(seed.Provider), seed.ProviderEntryRef, seed.ProviderGroupRef, identity)
		if fetchErr != nil {
			if errors.Is(fetchErr, animeapplication.ErrProviderFeedSnapshotRefreshRequired) {
				return animeapplication.TorrentPayload{}, &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: animeapplication.SafeReason(fetchErr.Error())}
			}
			return animeapplication.TorrentPayload{}, fetchErr
		}
		var fact *animeapplication.SeedPreviewFact
		for index := range snapshot.Seeds {
			if snapshot.Seeds[index].StableSeedKey == seed.StableSeedKey {
				fact = &snapshot.Seeds[index]
				break
			}
		}
		if fact == nil {
			return animeapplication.TorrentPayload{}, &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: "所选种子已不在当前检索快照中，请重新检索后再试。"}
		}
		payloadSource, ok := s.seedPreviews.(animeapplication.SearchSeedPayloadSource)
		if !ok {
			return animeapplication.TorrentPayload{}, &apperrors.Business{Status: 503, Code: "SEED_PREVIEW_UNAVAILABLE", Message: "Keyword search seed payloads are not served by this server."}
		}
		payloadSeed, payloadErr := payloadSource.ReadSearchSeedPayload(ctx, string(seed.Provider), seed.ProviderEntryRef, seed.ProviderGroupRef, identity, seed.StableSeedKey)
		if payloadErr != nil {
			stored, err = animeapplication.SeedPayload{}, payloadErr
		} else {
			digest := ""
			if payloadSeed.PayloadDigest != nil {
				digest = *payloadSeed.PayloadDigest
			}
			stored = animeapplication.SeedPayload{Bytes: payloadSeed.Payload, PayloadDigest: digest, StableSeedKey: payloadSeed.StableSeedKey, Manifest: payloadSeed.Manifest}
			if payloadSeed.TorrentFilename != nil {
				stored.TorrentFilename = *payloadSeed.TorrentFilename
			}
		}
	} else {
		stored, err = s.seedPreviews.ReadSeedPayload(ctx, string(seed.Provider), seed.ProviderEntryRef, seed.ProviderGroupRef, identity, seed.StableSeedKey)
	}
	if err != nil {
		if errors.Is(err, animeapplication.ErrProviderFeedSnapshotRefreshRequired) {
			return animeapplication.TorrentPayload{}, &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: animeapplication.SafeReason(err.Error())}
		}
		return animeapplication.TorrentPayload{}, err
	}
	metadata, err := torrentparser.Parse(stored.Bytes)
	if err != nil {
		return animeapplication.TorrentPayload{}, &apperrors.Business{Status: 422, Code: "TORRENT_FETCH_FAILED", Message: animeapplication.SafeReason(err.Error())}
	}
	return animeapplication.TorrentPayload{Bytes: stored.Bytes, Metadata: metadata}, nil
}

func (s *Service) loadOneShotCandidateRevision(ctx context.Context, input AcquireSeedInput) (animeapplication.CandidateRevision, animedomain.ResolvedSections, animedomain.EffectiveConfig, error) {
	if strings.TrimSpace(input.CandidateID) == "" || input.Revision < 1 {
		return animeapplication.CandidateRevision{}, animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, s.candidateRevisionRequiredError()
	}
	candidate, err := s.candidateRevisions.GetAnimeConfigCandidate(ctx, input.CandidateID)
	if err != nil {
		return animeapplication.CandidateRevision{}, animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, err
	}
	if candidate.CurrentRevision == nil {
		return animeapplication.CandidateRevision{}, animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, s.candidateRevisionRequiredError()
	}
	if *candidate.CurrentRevision != input.Revision {
		return animeapplication.CandidateRevision{}, animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, s.candidateRevisionMismatchError(*candidate.CurrentRevision, input.Revision)
	}
	revision, err := s.candidateRevisions.ReadVerifiedAnimeConfigCandidateRevision(ctx, input.CandidateID, input.Revision)
	if err != nil {
		return animeapplication.CandidateRevision{}, animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, err
	}
	resolved, err := revision.Resolved()
	if err != nil {
		return animeapplication.CandidateRevision{}, animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, s.candidateDigestMismatchError()
	}
	if candidate.CurrentRevisionDigest != nil && *candidate.CurrentRevisionDigest != revision.EffectiveDigest {
		return animeapplication.CandidateRevision{}, animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, s.candidateDigestMismatchError()
	}
	effective, err := animedomain.EffectiveConfigFromSections(resolved)
	if err != nil {
		return animeapplication.CandidateRevision{}, animedomain.ResolvedSections{}, animedomain.EffectiveConfig{}, s.candidateDigestMismatchError()
	}
	return revision, resolved, effective, nil
}

// AcquireSeedOnce admits one finite one-shot seed acquisition. Canonicalize ->
// replay/conflict under the marker identity -> fetch/bencode-validate outside
// the transaction -> verify every declared strong identity -> bind human seeds
// to the current authorized preview -> commit the temporary owner graph and
// marker atomically.
func (s *Service) AcquireSeedOnce(ctx context.Context, input AcquireSeedInput, requestIdempotencyKey string) (OneShotAdmissionResult, error) {
	if err := animeapplication.ValidateIdempotencyKey(requestIdempotencyKey); err != nil {
		return OneShotAdmissionResult{}, err
	}
	if s == nil || s.oneShotMarkers == nil {
		return OneShotAdmissionResult{}, errors.New("one-shot marker repository is not configured")
	}
	// A compatible legacy input is internalized before any hash, comparison or
	// write, so the canonical `sha256:<hex>` form is the only form that ever
	// participates in replay identity or stored evidence.
	input.Seed = canonicalSelectedSeed(input.Seed)
	if err := validateOneShotEpisodeOffset(input.EpisodeOffset); err != nil {
		return OneShotAdmissionResult{}, err
	}

	// The input variants are mutually exclusive; a mixed or mis-discriminated
	// body is a structural request error before any config read or marker
	// lookup, so a malformed request can never be mistaken for a replay.
	var currentDraft *animedomain.AnimeDraft
	if input.isCurrentInput() {
		if input.Draft == nil {
			return OneShotAdmissionResult{}, inputSourceInvalidError("input_source=current_input requires the complete draft")
		}
		if strings.TrimSpace(input.CandidateID) != "" || input.Revision != 0 {
			return OneShotAdmissionResult{}, inputSourceInvalidError("a current_input admission must not carry candidate_id or revision")
		}
		currentDraft = input.Draft
		if input.Caller.Kind == AcquireSeedCallerAgent {
			return OneShotAdmissionResult{}, &apperrors.Business{
				Status: 422, Code: "ONE_SHOT_CALLER_INVALID",
				Message: "Agent one-shot admission requires an exact saved candidate revision; current_input admission is human-only.",
			}
		}
	} else {
		switch input.InputSource {
		case "", AcquireSeedInputSourceSavedRevision:
			if input.Draft != nil {
				return OneShotAdmissionResult{}, inputSourceInvalidError("a saved-revision admission must not carry a draft")
			}
		default:
			return OneShotAdmissionResult{}, inputSourceInvalidError("input_source must be current_input or saved_revision")
		}
	}
	// A seed without locator is a snapshot reference. Its shape is a structural
	// request error, checked before any marker lookup like the input variants.
	reference := isSeedReference(input.Seed)
	if reference {
		if err := validateSeedReference(input.Seed); err != nil {
			return OneShotAdmissionResult{}, err
		}
	}

	// Replay is marker-owned. Read the marker before loading the candidate so a
	// completed admission remains replayable after candidate deletion or cleanup.
	existing, err := s.oneShotMarkers.GetByRequestIdempotencyKey(ctx, requestIdempotencyKey)
	if err != nil {
		return OneShotAdmissionResult{}, err
	}
	if existing != nil {
		if reference {
			// A replayed reference resolves against the seed the marker froze,
			// never the feed snapshot: the exact replay returns the original
			// handle even after that snapshot was superseded.
			frozen, err := frozenSeedForReference(*existing, input.Seed)
			if err != nil {
				return OneShotAdmissionResult{}, err
			}
			input.Seed = frozen
		} else {
			input.Seed = replaySeedWithExpandedIdentity(*existing, input)
		}
		requestHash, err := oneShotReplayRequestHash(*existing, input, requestIdempotencyKey)
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		return oneShotReplayDecision(*existing, requestHash)
	}
	// A new admission resolves a reference from the exact persisted snapshot
	// here, at the entry point: from now on the seed is the locator form for
	// the same snapshot seed, so validation, hashing, freezing and replay are
	// exactly those of a locator-form request.
	if reference {
		resolved, err := s.resolveSeedReference(ctx, input.Seed)
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		input.Seed = resolved
	}

	var (
		saved *savedOneShotRevision
		draft animedomain.AnimeDraft
	)
	if input.isCurrentInput() {
		draft = *currentDraft
		if violations := animedomain.ValidateDraft(draft); len(violations) > 0 {
			return OneShotAdmissionResult{}, animeapplication.DraftInvalidError(violations)
		}
	} else {
		// A new saved-revision admission requires an exact saved revision. The
		// candidate is read only after the marker preflight; the transaction
		// locks it again before any owner row is written.
		revision, resolved, effective, err := s.loadOneShotCandidateRevision(ctx, input)
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		input.EffectiveDigest = revision.EffectiveDigest
		draft = revision.Draft
		saved = &savedOneShotRevision{revision: revision, resolved: resolved, effective: effective}
	}
	if err := validateSelectedSeed(input.Seed); err != nil {
		return OneShotAdmissionResult{}, err
	}
	if err := validateAcquireSeedCaller(input.Caller, draft, input.Seed, input); err != nil {
		return OneShotAdmissionResult{}, err
	}
	// A human seed carries an exact persisted snapshot identity, so its torrent
	// bytes are read from that snapshot (or the expansion cache that serves its
	// locator) instead of being fetched from the provider again. The bytes the
	// user reviewed are then the bytes the downloader receives, and a provider
	// change between review and click cannot substitute different content. An
	// agent admission has no snapshot binding and keeps the shared bounded
	// fetcher (below, after the request hash).
	var payload animeapplication.TorrentPayload
	persisted := input.Caller.Kind == AcquireSeedCallerHuman && selectedSeedHasSnapshotIdentity(input.Seed)
	// boundSeed is the seed exactly as the snapshot published it; the preview
	// binding below compares against that, not against an identity the server
	// derived from the expanded torrent.
	boundSeed := input.Seed
	if persisted {
		stored, err := s.readPersistedSeedPayload(ctx, input.Seed)
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		payload = stored
		if !seedHasStrongIdentity(input.Seed) {
			// The feed published no identity. Under lazy expansion the expanded
			// torrent is the seed's identity: its infohash is computed from the
			// exact cached bytes this admission freezes, so it is bound before the
			// request is hashed and frozen with the marker.
			if hash := payloadInfoHash(payload); hash != "" {
				input.Seed.InfoHash = &hash
			}
		}
	}
	if !seedHasStrongIdentity(input.Seed) {
		return OneShotAdmissionResult{}, &apperrors.Business{
			Status: 422, Code: "SEED_IDENTITY_REQUIRED",
			Message: "A one-shot seed requires a canonical info_hash or a SHA-256 expected_payload_digest.",
		}
	}

	var requestHash string
	if input.isCurrentInput() {
		// The current-input replay identity is request-derived only (idempotency
		// key + canonical draft digest + selected seed): a replay must never be
		// re-resolved against changed globals, so no resolved value participates.
		draftDigest, err := animeapplication.CanonicalAnimeDraftHash(*currentDraft)
		if err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("hash one-shot current input: %w", err)
		}
		requestHash, err = canonicalOneShotCurrentInputRequestHash(draftDigest, input, requestIdempotencyKey)
		if err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("hash one-shot request: %w", err)
		}
	} else {
		var err error
		requestHash, err = canonicalOneShotRequestHash(input, requestIdempotencyKey)
		if err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("hash one-shot request: %w", err)
		}
	}

	if !persisted {
		if s.torrentFetcher == nil {
			return OneShotAdmissionResult{}, &apperrors.Business{Status: 503, Code: "TORRENT_FETCHER_NOT_CONFIGURED", Message: "The torrent payload fetcher is not configured."}
		}
		fetched, err := s.torrentFetcher.FetchPayload(ctx, input.Seed.Locator)
		if err != nil {
			return OneShotAdmissionResult{}, &apperrors.Business{Status: 422, Code: "TORRENT_FETCH_FAILED", Message: animeapplication.SafeReason(err.Error())}
		}
		payload = fetched
	}
	if err := verifySeedStrongIdentities(payload, input.Seed); err != nil {
		return OneShotAdmissionResult{}, err
	}

	// Human seeds must still exist in the current authorized preview snapshot
	// for their entry/group; a replaced or superseded snapshot requires a fresh
	// seed-preview refresh before retrying. Agent admission never depends on it.
	// The matched fact also restores the seed's AniBT tag set, which the request
	// never carries (see verifyHumanSeedPreviewBinding). It is bound after the
	// request hash is computed, so replay identity is unchanged.
	if input.Caller.Kind == AcquireSeedCallerHuman {
		fact, err := s.verifyHumanSeedPreviewBinding(ctx, boundSeed)
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		input.Seed.AniBTTags = cloneStoredAniBTTags(fact.AniBTTags)
	}

	return s.admitOneShotSeed(ctx, input, requestIdempotencyKey, requestHash, payload, saved)
}

// savedOneShotRevision carries the pre-transaction saved-revision resolution
// into the admission transaction, which re-locks and re-verifies it before any
// owner row is written. It is nil for a current_input admission.
type savedOneShotRevision struct {
	revision  animeapplication.CandidateRevision
	resolved  animedomain.ResolvedSections
	effective animedomain.EffectiveConfig
}

func inputSourceInvalidError(detail string) error {
	return &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: detail}
}

// admitOneShotSeed commits the temporary owner graph, the marker, and the
// single qb_submit WorkItem in one transaction. Any validation, uniqueness, or
// marker-write failure rolls back every row.
func (s *Service) admitOneShotSeed(
	ctx context.Context,
	input AcquireSeedInput,
	key, requestHash string,
	payload animeapplication.TorrentPayload,
	saved *savedOneShotRevision,
) (OneShotAdmissionResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("begin one-shot admission: %w", err)
	}
	defer tx.Rollback()

	// Serialize same-key admissions, then decide replay against the locked
	// marker row (never from a pre-flight read).
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("lock one-shot idempotency key: %w", err)
	}
	existing, err := s.oneShotMarkers.LockByRequestIdempotencyKey(ctx, txscope.Wrap(tx), key)
	if err != nil {
		return OneShotAdmissionResult{}, err
	}
	if existing != nil {
		result, err := oneShotReplayDecision(*existing, requestHash)
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("commit one-shot replay: %w", err)
		}
		return result, nil
	}

	// The captured configuration this admission evaluates with. A
	// saved-revision admission re-locks the candidate and re-verifies the exact
	// revision under the transaction; a current_input admission resolves the
	// request draft against the globals and the global parser-policy default
	// inside the transaction, so the captured snapshot is one consistent
	// database read and no candidate row is required.
	var (
		draft           animedomain.AnimeDraft
		resolved        animedomain.ResolvedSections
		effective       animedomain.EffectiveConfig
		parserPolicy    animedomain.ParserPolicyEnvelope
		configIdentity  animeapplication.ConfigRevisionIdentity
		effectiveDigest string
	)
	if saved != nil {
		var candidateCurrentRevision sql.NullInt64
		var candidateCurrentDigest sql.NullString
		var candidateStatus string
		var candidateActivated sql.NullString
		if err := tx.QueryRowContext(ctx, `
SELECT current_revision, current_revision_digest, status, activated_managed_anime_id
FROM anime_config_candidates
WHERE id = $1
FOR UPDATE`, input.CandidateID).Scan(&candidateCurrentRevision, &candidateCurrentDigest, &candidateStatus, &candidateActivated); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return OneShotAdmissionResult{}, &apperrors.NotFound{Resource: "anime_config_candidate", ID: input.CandidateID}
			}
			return OneShotAdmissionResult{}, fmt.Errorf("lock one-shot candidate: %w", err)
		}
		if candidateActivated.Valid || candidateStatus != "working" {
			return OneShotAdmissionResult{}, s.candidateNotWorkingError()
		}
		if !candidateCurrentRevision.Valid || candidateCurrentRevision.Int64 != input.Revision || !candidateCurrentDigest.Valid || candidateCurrentDigest.String != saved.revision.EffectiveDigest {
			return OneShotAdmissionResult{}, s.candidateRevisionMismatchError(candidateCurrentRevision.Int64, input.Revision)
		}
		lockedRevision, databaseDigest, err := s.candidateRevisions.LoadCandidateRevisionTx(ctx, tx, input.CandidateID, input.Revision)
		if err != nil {
			return OneShotAdmissionResult{}, s.candidateDigestMismatchError()
		}
		if lockedRevision.EffectiveDigest != saved.revision.EffectiveDigest || databaseDigest != lockedRevision.DraftDigest {
			return OneShotAdmissionResult{}, s.candidateDigestMismatchError()
		}
		draft, resolved, effective = saved.revision.Draft, saved.resolved, saved.effective
		parserPolicy = saved.revision.ParserPolicy
		effectiveDigest = saved.revision.EffectiveDigest
		candidateID := input.CandidateID
		revisionNumber := input.Revision
		configIdentity = animeapplication.ConfigRevisionIdentity{CandidateID: &candidateID, Revision: &revisionNumber, EffectiveDigest: effectiveDigest, IdentityKind: "candidate_revision"}
	} else {
		draft = *input.Draft
		// The submitted configuration must be the frozen one it claims to be. A
		// draft whose content no longer digests to business_digest was edited (or
		// assembled) after the freeze, and admitting it would run a configuration
		// the user never reviewed. This is the same 422 a candidate save answers,
		// and it runs before any owner or qB write.
		if err := animeapplication.VerifySubmittedBusinessDigest(draft); err != nil {
			return OneShotAdmissionResult{}, err
		}
		// Admitting a submitted draft persists its resolved configuration, so it
		// validates naming vars exactly like a candidate save.
		vars, err := animeapplication.NamingVarsSnapshot("rules." + animedomain.SectionNamingRules)
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		var (
			sections animedomain.ResolvedSections
			envelope animedomain.ParserPolicyEnvelope
		)
		// The draft resolves from its own complete section values and frozen
		// parser envelope, exactly as saving and previewing it do, and reads no
		// Global. A draft without its business snapshot fails closed.
		if draft.Business == nil {
			return OneShotAdmissionResult{}, &apperrors.Business{Status: 422, Code: "BUSINESS_SNAPSHOT_INVALID", Message: "draft must carry a complete business snapshot"}
		}
		sections, err = animedomain.ResolveDraftSectionsFromBusiness(draft, vars)
		if err != nil {
			return OneShotAdmissionResult{}, animeapplication.SectionResolutionError(err)
		}
		envelope = draft.Business.Parser
		resolved = sections
		effective, err = animedomain.EffectiveConfigFromSections(resolved)
		if err != nil {
			return OneShotAdmissionResult{}, s.candidateDigestMismatchError()
		}
		effectiveDigest, err = resolved.EffectiveDigest()
		if err != nil {
			return OneShotAdmissionResult{}, s.candidateDigestMismatchError()
		}
		parserPolicy = envelope
		configIdentity = animeapplication.ConfigRevisionIdentity{EffectiveDigest: effectiveDigest, IdentityKind: "current_input"}
	}

	var pending terminal.PendingHandle
	var nativeAcceptor *terminal.OneShotAcceptor

	// A one-shot names its seed directly: it is never bound by the captured
	// configuration's group selection. The seed's existence is proven by its
	// exact persisted snapshot (verifyHumanSeedPreviewBinding) before this
	// transaction opened.
	seed := input.Seed

	// The seed's group is recorded by its display name, the same fact a normal
	// subscription stores and every naming render reads back as anime.group; the
	// provider group identity names nothing a person sees. One resolved value
	// feeds the placeholder source, the release evaluation and the target plan,
	// so the frozen source facts and the rendered names agree. It resolves
	// before the first write: a seed whose group has no recorded name refuses
	// the command without creating any owner row.
	var groupName string
	if seed.SnapshotKind == string(animecatalogdomain.FeedSnapshotKindSearch) {
		// A keyword search seed has no catalog entry and no provider group, so
		// there is no recorded group observation to read a display name from. The
		// name comes from the one source that can state it for such a seed: the
		// selected release title's own group marker, read by the pinned title
		// parser. It is a display fact, never a group identity. When the title has
		// no marker the placeholder records the same stable "unknown" label every
		// other group-less source uses, instead of inventing a name or reusing the
		// search-scope digest.
		groupName = searchSeedGroupDisplayName(seed)
	} else {
		var err error
		groupName, err = sourceGroupDisplayName(ctx, tx, draft, seed)
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
	}

	animeID := uuid.NewString()
	title := strings.TrimSpace(draft.Title)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO managed_anime(id, title, title_override)
		VALUES ($1, $2, $3)`, animeID, title, animeapplication.FixedTitle(draft.Title, draft.TitleOverride)); err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("create one-shot managed anime: %w", err)
	}

	subscriptionID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO subscriptions(id, managed_anime_id, origin, origin_entry_ref, instance_label, state)
VALUES ($1, $2, 'oneshot', NULL, $3, 'stopped')`,
		subscriptionID, animeID, oneShotInstanceLabel(draft)); err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("create one-shot subscription mount: %w", err)
	}
	if _, err := s.provisioner.InitializeAnimeOwnerRowsTx(ctx, tx, animeID, draft.Metadata); err != nil {
		return OneShotAdmissionResult{}, err
	}

	// The one-shot instance takes every axis from the business snapshot its
	// input froze (the saved revision's, or the submitted draft's), through
	// the same provisioning path as Subscribe and activation. Global is never
	// read; an input without a snapshot fails closed.
	business := draft.Business
	if saved != nil {
		business = saved.revision.Business
	}
	if business == nil {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 422, Code: "BUSINESS_SNAPSHOT_INVALID", Message: "one-shot input must carry a complete business snapshot"}
	}
	if err := s.provisioner.ProvisionOwnerSetTx(ctx, tx, animeID, resolved, *business); err != nil {
		return OneShotAdmissionResult{}, err
	}
	// The instance stores its own tag_filters like every other per-anime
	// section: the draft's value, or the code default for a draft that carries
	// none (a one-shot never filters by tags at admission). Global is not read.
	tagFilters, err := animedomain.ResolveDraftTagFilters(draft)
	if err != nil {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 422, Code: "OVERRIDE_MODE_INVALID", Message: err.Error()}
	}
	tagFiltersValue, err := json.Marshal(tagFilters.Rules)
	if err != nil {
		return OneShotAdmissionResult{}, err
	}
	if err := animeapplication.UpsertOverride(ctx, tx, animeID, "rules", animedomain.SectionTagFilters, animedomain.OverrideValue{Value: tagFiltersValue}); err != nil {
		return OneShotAdmissionResult{}, err
	}

	// Exactly one placeholder custom_rss source for the selected seed. It has
	// no runtime row, so ordinary RSS refresh dispatch skips it; the draft's
	// own source list (agent snapshots may carry zero or more) is deliberately
	// not persisted: the mount owns exactly this placeholder. It carries the
	// one-shot request's own episode_offset, which acceptance reads back as the
	// link offset.
	episodeOffset := input.EpisodeOffset
	sourceID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO rss_sources(id, subscription_id, source_kind, locator, locator_display, group_name, title, episode_offset)
VALUES ($1, $2, 'custom_rss', $3, $4, $5, $6, $7)`,
		sourceID, subscriptionID, seed.Locator, animedomain.LocatorDisplay(seed.Locator),
		groupName, strings.TrimSpace(seed.Title), episodeOffset); err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("create one-shot placeholder source: %w", err)
	}

	// The title-parse tags freeze now: the target plan below and the persisted
	// source facts read the same set.
	item := rssapplication.FreezeTitleTags(buildOneShotFeedItem(payload, seed))
	hold := animedomain.MetadataHoldFromBindings(draft.Metadata.Bgm, draft.Metadata.Tmdb)
	evalInput := animedomain.SourceEvaluationInput{Index: 0, Kind: string(animedomain.SourceKindCustomRss), GroupName: groupName, Offset: episodeOffset}
	// The input's business snapshot pins the exact attr rules its preview used;
	// admission evaluates with those, never the process's current rule set.
	ref := business.Programs.AttrRules
	rules, err := attrrules.LoadExact(ctx, ref.Version, ref.Digest, func(ctx context.Context, fingerprint string) ([]byte, error) {
		var raw []byte
		err := tx.QueryRowContext(ctx, `SELECT content FROM attr_rule_versions WHERE fingerprint=$1`, fingerprint).Scan(&raw)
		return raw, err
	})
	if err != nil {
		return OneShotAdmissionResult{}, err
	}
	// One selected parser/profile governs the whole finite command: the same
	// evidence feeds the verdict, the target/naming plan and the frozen input
	// snapshot, so no second parser runs. The saved-revision selection is built
	// from the exact frozen revision (kind from the frozen parser-policy
	// envelope, episode patterns from the resolved parser section); the
	// current-input selection is built from the same envelope the candidate save
	// would freeze, read inside this transaction. Both resolve with the one-shot
	// request's own episode_offset. A non-scalar
	// identity fails the admission closed with a typed error instead of
	// guessing an episode.
	var selection acquisitiontarget.ParserSelectionInput
	if saved != nil {
		selection, err = saved.revision.ParserSelectionInput(resolved)
		if err != nil {
			return OneShotAdmissionResult{}, s.candidateDigestMismatchError()
		}
	} else {
		mediaKind, season, seasonBound := animedomain.FrozenMediaBinding(draft.Metadata.Tmdb, draft.Metadata.MediaKindOverride)
		selection = acquisitiontarget.ParserSelectionInput{
			Kind:            parserPolicy.Kind,
			EpisodePatterns: effective.Rules.Parser.EpisodePatterns,
			MediaKind:       mediaKind,
			Season:          season,
			SeasonBound:     seasonBound,
			PolicyVersion:   parserPolicy.PolicyVersion,
			ProfileVersion:  effective.Versions[animedomain.SectionParser],
		}
	}
	selection.Offset = episodeOffset
	if err := validateOneShotFilterGate(item, effective); err != nil {
		return OneShotAdmissionResult{}, err
	}
	evidence, evidenceErr := acquisitiontarget.ResolveParserEvidence(ctx, selection, item.Title)
	if evidenceErr != nil {
		return OneShotAdmissionResult{}, &apperrors.Business{
			Status: 422, Code: "PARSER_SELECTION_UNAVAILABLE",
			Message: "the instance selected parser/profile is unavailable: " + strings.TrimSpace(evidenceErr.Error()),
		}
	}
	evaluation := animedomain.EvaluateFeedItemWithIdentity(item, evalInput, effective, hold, rules, evidence.Identity)
	// The item's identity falls back to the seed's torrent URL, which can carry
	// a tracker key: the release stores the digested form, as RSS refresh does.
	_, evaluation.StableItemKey = animedomain.PublicStableItemKey(item)
	releaseID := uuid.NewString()
	frozenVars, varsErr := freezeBusinessVarsTx(ctx, tx, *business, item, animeID, releaseID, naming.VarsEpisode(evidence.NamingFacts()))
	if varsErr != nil {
		status := 422
		if namingvars.IsInterrupted(varsErr) {
			status = 503 // transient: nothing frozen, the command may be retried
		}
		return OneShotAdmissionResult{}, &apperrors.Business{Status: status, Code: "NAMING_VARS_UNAVAILABLE", Message: varsErr.Error()}
	}
	evaluation.FrozenVars = &frozenVars

	// Shared pre-download target admission. The same planner, probe, root,
	// collision and adoption rules ordinary RSS evaluation uses run before any
	// qB work is admitted. The admission is decoupled from media-library
	// reachability and naming completeness: a target plan that fails for a
	// naming or library condition does not refuse the command — the frozen
	// diagnostic travels with the input and the accepted chain carries the
	// fault in its post-processing stages, where it is retryable and manually
	// recoverable. Only a real target-state refusal (collision policy) stays a
	// synchronous typed rejection, and adoption stays a success.
	var targetPlan acquisitiontarget.PlanResult
	var targetSetDigest string
	// adoptedTarget marks the already-satisfied exact target set. It is a
	// successful terminal outcome with no download chain, never a refusal: the
	// filesystem already holds the exact selected entries, so the finite
	// command records the frozen target facts and stops.
	adoptedTarget := false
	targetPlanFailed := false
	route, err := oneShotUnclassifiedRoute(*business)
	if err != nil {
		return OneShotAdmissionResult{}, err
	}
	// The frozen sections carry no download_completion; a one-shot takes the
	// Global default and freezes it into the batch plan.
	if effective.DownloadCompletion, err = oneShotDownloadCompletion(ctx, tx); err != nil {
		return OneShotAdmissionResult{}, err
	}
	if s.targetAcquisitionConfigured {
		parserKind := string(evidence.Selection.Kind())
		planned := evidence
		targetPlan = planOneShotTargets(ctx, s.targetProbe, oneShotTargetRequest(releaseID, draft, groupName, episodeOffset, parserKind, effective, item, evaluation, &planned, route))
		switch targetPlan.Admission.Admission {
		case acquisitiontarget.AdmissionDownload:
		case acquisitiontarget.AdmissionAdopted:
			adoptedTarget = true
		default:
			if oneShotTargetRefusesAdmission(targetPlan) {
				return OneShotAdmissionResult{}, oneShotTargetFailure(targetPlan)
			}
			targetPlanFailed = true
		}
		item.ReleaseFiles = targetPlan.ReleaseFiles
		if !targetPlanFailed {
			targetSetDigest = targetPlan.Set.Digest
		}
	}

	provenance := evidence.Provenance()
	parserEvidence := &provenance
	effectiveSectionsRaw, err := json.Marshal(resolved.Sections)
	if err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("encode one-shot effective sections: %w", err)
	}
	targetFactsRaw, err := encodeOneShotTargetFacts(targetPlan, s.targetAcquisitionConfigured, input)
	if err != nil {
		return OneShotAdmissionResult{}, err
	}
	inputSnapshot := OneShotInputSnapshot{
		SelectedSeed:      seed,
		EpisodeOffset:     episodeOffset,
		EffectiveSections: effectiveSectionsRaw,
		SectionVersions:   resolved.Versions,
		SectionSchemas:    resolved.SchemaVersions,
		EffectiveDigest:   effectiveDigest,
		ConfigIdentity:    configIdentity,
		Parser:            parserEvidence,
		TargetFacts:       targetFactsRaw,
	}
	if input.isCurrentInput() {
		draftRaw, err := json.Marshal(draft)
		if err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("encode one-shot current input draft: %w", err)
		}
		draftDigest, err := animeapplication.CanonicalAnimeDraftHash(draft)
		if err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("hash one-shot current input draft: %w", err)
		}
		inputSnapshot.InputSource = AcquireSeedInputSourceCurrentInput
		inputSnapshot.Draft = draftRaw
		inputSnapshot.DraftDigest = draftDigest
	} else {
		inputSnapshot.CandidateID = input.CandidateID
		inputSnapshot.Revision = input.Revision
	}
	inputSnapshotRaw, err := encodeOneShotInputSnapshot(inputSnapshot)
	if err != nil {
		return OneShotAdmissionResult{}, err
	}

	rawFacts, err := rssapplication.EncodeFeedFacts(item)
	if err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("encode one-shot feed facts: %w", err)
	}
	internalFacts, err := rssapplication.EncodeInternalFeedFacts(item)
	if err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("encode one-shot internal facts: %w", err)
	}
	_, _, sourceFacts, err := rssapplication.EncodeSourceFacts(item, evalInput.GroupName)
	if err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("encode one-shot source facts: %w", err)
	}
	parsedFacts, err := rssapplication.EncodeParsedFacts(evaluation)
	if err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("encode one-shot parsed facts: %w", err)
	}

	// The Release verdict mirrors the ordinary RSS owner writer: an adopted
	// exact target set is a successful terminal Release state, not a refusal.
	releaseVerdict := string(evaluation.Verdict)
	releaseReasonCode := oneShotReasonCode
	releaseReasonDetail := "one-shot seed acquisition"
	if adoptedTarget {
		releaseVerdict = string(animedomain.ReleaseVerdict("adopted_existing"))
		releaseReasonCode = "all_targets_exact_size"
		releaseReasonDetail = "all rendered targets already exist with the exact entry sizes"
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO releases(
    id, rss_source_id, stable_item_key, raw_facts, source_facts, parsed_facts,
    internal_facts, verdict, reason_code, reason_detail, evaluation_version
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		releaseID, sourceID, evaluation.StableItemKey, rawFacts, sourceFacts, parsedFacts, internalFacts,
		releaseVerdict, releaseReasonCode, releaseReasonDetail, animedomain.EvaluatorVersion); err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("create one-shot release: %w", err)
	}
	// The Release media fact snapshot freezes in this admission transaction
	// through the same writer RSS evaluation uses, from the one evidence this
	// admission resolved (the request's own episode_offset, the bound media
	// kind/season). Artifact inspection resolves every file's identity from it.
	var bgmWorkID, tmdbWorkID string
	if draft.Metadata.Bgm != nil {
		bgmWorkID = draft.Metadata.Bgm.WorkId
	}
	if draft.Metadata.Tmdb != nil {
		tmdbWorkID = draft.Metadata.Tmdb.WorkId
	}
	// The batch plan freezes with it: the admission's own plan when target
	// planning ran, otherwise the plain classification under the route.
	snapshotInput := acquisitiontarget.ReleaseMediaSnapshotInput{
		ReleaseID: releaseID, Title: item.Title, BGMWorkID: bgmWorkID, TMDBWorkID: tmdbWorkID, Evidence: evidence,
		Files: item.ReleaseFiles, FileFilter: effective.Rules.FileFilter, UnclassifiedRoute: route,
		DownloadCompletion: string(effective.DownloadCompletion.Rules.Action), DownloadCompletionSeedLimits: effective.DownloadCompletion.Rules.SeedLimits,
	}
	if targetPlan.BatchPlan.Version != "" {
		frozenPlan := targetPlan.BatchPlan
		snapshotInput.Plan = &frozenPlan
	}
	if err := acquisitiontarget.FreezeReleaseMediaSnapshotTx(ctx, txscope.Wrap(tx), s.mediaFacts, snapshotInput); err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("freeze one-shot release media facts: %w", err)
	}

	// The complete TargetSet and its observations freeze before the single
	// DownloadTask admission that needs them. A decoupled failed plan has no
	// set to freeze: nothing is persisted and the plan stage later replans.
	if s.targetAcquisitionConfigured && !targetPlanFailed && len(targetPlan.Set.Targets) > 0 {
		if err := s.targetStore.ReplaceSet(ctx, txscope.Wrap(tx), targetPlan.Set); err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("persist one-shot target set: %w", err)
		}
		for _, observation := range targetPlan.Observations {
			if err := s.targetStore.UpsertObservation(ctx, txscope.Wrap(tx), observation); err != nil {
				return OneShotAdmissionResult{}, fmt.Errorf("persist one-shot target observation: %w", err)
			}
		}
	}

	filename, err := rssapplication.TorrentInputFilename(payload.Metadata.TorrentName, payload.Metadata.InfoHashV1)
	if err != nil {
		return OneShotAdmissionResult{}, err
	}
	if err := rssapplication.PersistTorrentInput(ctx, txscope.Wrap(tx), releaseID, payload.Bytes, payload.Metadata, seed.Locator, filename); err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("persist one-shot torrent input: %w", err)
	}

	oneShotID := uuid.NewString()
	correlationID := "corr-" + uuid.NewString()
	// Attempts model: the admission creates attempt #1 atomically with
	// the marker. An adopted acquisition's attempt #1 is already terminal (its
	// outcome evidence is immutable from birth); an admitted download chain's
	// attempt #1 stays running until the settlement dual-write terminates it.
	inputDigest := sha256.Sum256(inputSnapshotRaw)
	attemptID := uuid.NewString()
	attempt := animeport.OneShotAttemptRecord{
		ID:            attemptID,
		AcquisitionID: oneShotID,
		Sequence:      1,
		Mode:          animeport.OneShotAttemptModeInitial,
		InputSnapshot: inputSnapshotRaw,
		InputDigest:   "sha256:" + hex.EncodeToString(inputDigest[:]),
		State:         "running",
		Version:       1,
	}
	marker := animeport.OneShotMarkerRecord{
		ID:                      oneShotID,
		ManagedAnimeID:          animeID,
		SubscriptionID:          subscriptionID,
		RSSSourceID:             sourceID,
		ReleaseID:               releaseID,
		CorrelationID:           correlationID,
		Request_Idempotency_Key: key,
		RequestHash:             requestHash,
		InputSnapshot:           inputSnapshotRaw,
		AdmissionEpoch:          1,
		ActiveEffectCount:       0,
		ActiveEffectLeases:      json.RawMessage("[]"),
		Version:                 1,
		CurrentAttemptID:        attemptID,
	}
	result := OneShotAdmissionResult{
		OneShotID:      oneShotID,
		State:          string(OneShotAcquisitionStateRunning),
		ManagedAnimeID: animeID,
		SubscriptionID: subscriptionID,
		RSSSourceID:    sourceID,
		ReleaseID:      releaseID,
	}
	if adoptedTarget {
		// The exact selected entries are already on disk with the exact sizes.
		// The finite command records the frozen target facts and finishes
		// immediately: it reads no qB connection and creates no DownloadTask,
		// qb_submit WorkItem, Artifact or FileOperation. The marker is created
		// already terminal so no drain is required to settle a chain that never
		// had asynchronous work; attempt #1 is terminal from birth.
		outcome := string(OneShotTerminalOutcomeSucceeded)
		reasonCode := oneShotCompletedReasonCode
		reason := oneShotAdoptedReason
		marker.State = string(OneShotAcquisitionStateRetentionPending)
		marker.TerminalOutcome = &outcome
		marker.TerminalReasonCode = &reasonCode
		marker.TerminalReason = &reason
		marker.RetentionDays = workitems.DefaultRetentionDays
		result.State = marker.State
		now := time.Now().UTC()
		attempt.State = "succeeded"
		attempt.OutcomeCode = &reasonCode
		attempt.OutcomeReason = &reason
		attempt.TerminalAt = &now
	} else {
		// The accepted business is the input's frozen snapshot (checked non-nil
		// above), never a fresh read of Global.
		nativeBusiness := *business
		nativeBusiness.Sections, nativeBusiness.SectionVersions, nativeBusiness.SectionSchemas = resolved.Sections, resolved.Versions, resolved.SchemaVersions
		nativeBusiness.Parser = parserPolicy
		nativeBusiness, err = nativeBusiness.Freeze()
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		var qbConnectionID string
		err = tx.QueryRowContext(ctx, `
SELECT id FROM connections
WHERE kind = 'qbittorrent' AND state = 'configured'
ORDER BY name, id
LIMIT 1`).Scan(&qbConnectionID)
		if err == sql.ErrNoRows {
			return OneShotAdmissionResult{}, &apperrors.Business{Status: 409, Code: "QB_CONNECTION_NOT_CONFIGURED", Message: "No configured qBittorrent connection is available."}
		}
		if err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("read one-shot qbittorrent connection: %w", err)
		}

		// Read back the persisted torrent-input identity so the frozen input
		// carries exactly what the downloader decodes (same contract as the
		// RSS acceptance). Freshly inserted owners are version 1.
		var torrentInputID, torrentPayloadDigest, torrentParserVersion, torrentVersion string
		err = tx.QueryRowContext(ctx, `
SELECT id, payload_digest, parser_version, torrent_version
FROM release_torrent_inputs
WHERE release_id = $1`, releaseID).Scan(&torrentInputID, &torrentPayloadDigest, &torrentParserVersion, &torrentVersion)
		if err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("read one-shot torrent input identity: %w", err)
		}
		frozenInputRaw, err := json.Marshal(struct {
			ReleaseStableItemKey string                                    `json:"release_stable_item_key"`
			SelectedRelease      *animeapplication.SelectedReleaseIdentity `json:"selected_release"`
			SelectedSeed         oneShotSelectedSeedJSON                   `json:"selected_seed"`
			TargetFacts          json.RawMessage                           `json:"target_facts"`
			TargetSetDigest      string                                    `json:"target_set_digest,omitempty"`
			SourceLocatorDisplay string                                    `json:"source_locator_display"`
			TorrentInputID       string                                    `json:"torrent_input_id"`
			TorrentPayloadDigest string                                    `json:"torrent_payload_digest"`
			TorrentFilename      string                                    `json:"torrent_filename"`
			TorrentParserVersion string                                    `json:"torrent_parser_version"`
			TorrentVersion       string                                    `json:"torrent_version"`
			InfoHashV1           string                                    `json:"info_hash_v1,omitempty"`
			InfoHashV2           string                                    `json:"info_hash_v2,omitempty"`
			ReleaseFiles         json.RawMessage                           `json:"release_files,omitempty"`
		}{
			ReleaseStableItemKey: evaluation.StableItemKey,
			SelectedRelease: &animeapplication.SelectedReleaseIdentity{
				ReleaseID: releaseID, RSSSourceID: sourceID, StableItemKey: evaluation.StableItemKey,
				RawFacts: rawFacts, SourceFacts: sourceFacts, InternalFacts: internalFacts,
				ReleaseVersion: 1, SubscriptionID: subscriptionID, ManagedAnimeID: animeID,
			},
			SelectedSeed:         oneShotSelectedSeedJSONFromDomain(seed),
			TargetFacts:          targetFactsRaw,
			TargetSetDigest:      targetSetDigest,
			SourceLocatorDisplay: animedomain.LocatorDisplay(seed.Locator),
			TorrentInputID:       torrentInputID,
			TorrentPayloadDigest: torrentPayloadDigest,
			TorrentFilename:      filename,
			TorrentParserVersion: torrentParserVersion,
			TorrentVersion:       torrentVersion,
			InfoHashV1:           payload.Metadata.InfoHashV1,
			InfoHashV2:           payload.Metadata.InfoHashV2,
			ReleaseFiles:         releaseFilesFromInternalFacts(internalFacts),
		})
		if err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("encode one-shot frozen input: %w", err)
		}
		evaluatorVersion := animedomain.EvaluatorVersion
		frozenPolicyRaw, err := json.Marshal(animeapplication.FrozenPolicySnapshot{ConfigIdentity: configIdentity,
			EffectiveDigest:   effectiveDigest,
			EffectiveSections: effectiveSectionsRaw,
			SectionVersions:   resolved.Versions,
			SectionSchemas:    resolved.SchemaVersions,
			RulesVersion:      1,
			MetadataVersion:   1,
			EvaluatorVersion:  &evaluatorVersion,
			Vars:              evaluation.FrozenVars,
			Parser:            parserEvidence})
		if err != nil {
			return OneShotAdmissionResult{}, fmt.Errorf("encode one-shot frozen policy: %w", err)
		}

		var releaseVersion int64
		if err := tx.QueryRowContext(ctx, `SELECT version FROM releases WHERE id=$1`, releaseID).Scan(&releaseVersion); err != nil {
			return OneShotAdmissionResult{}, err
		}
		nativeAcceptor, err = terminal.NewOneShotAcceptor(s.db, s.workItems)
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		prepared, err := nativeAcceptor.Prepare(ctx, txscope.Wrap(tx), terminal.OneShotTask{EntryTask: terminal.EntryTask{ReleaseID: releaseID, ExpectedVersion: releaseVersion, FrozenInput: frozenInputRaw, FrozenPolicy: frozenPolicyRaw, CorrelationID: correlationID, QbConnectionID: qbConnectionID, MaxAttempts: animeapplication.DefaultMaxAttempts}, RequestKey: key, CandidateID: input.CandidateID, Revision: input.Revision, Draft: draft, Business: nativeBusiness, RequestHash: requestHash, Evidence: evidence, OneShot: workitems.OneShotRef{MarkerID: oneShotID, CorrelationID: correlationID, AdmissionEpoch: 1}})
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		pending, err = nativeAcceptor.Accept(ctx, txscope.Wrap(tx), prepared)
		if err != nil {
			return OneShotAdmissionResult{}, err
		}
		taskID := pending.TaskID()
		if _, err := tx.ExecContext(ctx, `UPDATE releases SET version=version+1,updated_at=NOW() WHERE id=$1 AND download_task_id=$2`, releaseID, taskID); err != nil {
			return OneShotAdmissionResult{}, err
		}
		result.DownloadTaskID = &taskID
		marker.DownloadTaskID = &taskID
		marker.CorrelationID = correlationID
		marker.State = string(OneShotAcquisitionStateRunning)
	}
	snapshot, err := json.Marshal(result)
	if err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("encode one-shot result snapshot: %w", err)
	}
	marker.ResultSnapshot = snapshot
	if err := s.oneShotMarkers.InsertAttemptTx(ctx, txscope.Wrap(tx), attempt); err != nil {
		return OneShotAdmissionResult{}, err
	}
	if err := s.oneShotMarkers.Insert(ctx, txscope.Wrap(tx), marker); err != nil {
		return OneShotAdmissionResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 503, Code: "ACCEPTANCE_UNKNOWN", Message: "commit_not_confirmed"}
	}
	if nativeAcceptor != nil {
		if _, err := nativeAcceptor.Committed(pending); err != nil {
			return OneShotAdmissionResult{}, err
		}
	}
	s.events.publish(events.TargetRSS)
	return result, nil
}

// ReadOneShotAcquisition answers with the owner-first view while the temporary
// owner exists and with the retained terminal outcome/reason after product-
// owner reclamation, until marker purge removes the replay record (then the
// typed 404 ends polling). Reads stay side-effect free and never expose lease
// tokens, fencing tokens, raw WorkItem payloads, secrets, or private paths.
func (s *Service) ReadOneShotAcquisition(ctx context.Context, oneShotID string) (OneShotAcquisitionView, error) {
	if s == nil || s.oneShotMarkers == nil {
		return OneShotAcquisitionView{}, errors.New("one-shot marker repository is not configured")
	}
	record, err := s.oneShotMarkers.GetByID(ctx, oneShotID)
	if err != nil {
		if err == sql.ErrNoRows {
			return OneShotAcquisitionView{}, &apperrors.NotFound{Resource: "one_shot_acquisition", ID: oneShotID}
		}
		return OneShotAcquisitionView{}, err
	}
	view := OneShotAcquisitionView{
		OneShotID:          record.ID,
		InputSnapshot:      decodeOneShotInputSnapshot(record.InputSnapshot),
		State:              OneShotAcquisitionState(record.State),
		TerminalOutcome:    oneShotOutcomePointer(record.TerminalOutcome),
		TerminalReasonCode: record.TerminalReasonCode,
		TerminalReason:     record.TerminalReason,
		ManagedAnimeID:     record.ManagedAnimeID,
		SubscriptionID:     record.SubscriptionID,
		RSSSourceID:        record.RSSSourceID,
		ReleaseID:          record.ReleaseID,
		DownloadTaskID:     record.DownloadTaskID,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
		Version:            record.Version,
	}
	// Owner views exist only while the temporary owner graph does. After
	// reclamation (cleaned) the anchors stay but the rows are gone. An adopted
	// acquisition has no download task at all, so there is no owner chain to
	// read and the empty owner views are the correct answer.
	if record.State != string(OneShotAcquisitionStateCleaned) && record.DownloadTaskID != nil {
		tasks, err := s.readOneShotDownloadTasks(ctx, record.SubscriptionID)
		if err != nil {
			return OneShotAcquisitionView{}, err
		}
		for index := range tasks {
			if tasks[index].ID == *record.DownloadTaskID {
				view.DownloadTask = &tasks[index]
				break
			}
		}
		artifacts, err := s.readOneShotArtifacts(ctx, record.SubscriptionID)
		if err != nil {
			return OneShotAcquisitionView{}, err
		}
		for index := range artifacts {
			if artifacts[index].DownloadTaskID == *record.DownloadTaskID {
				view.Artifact = &artifacts[index]
				operations, err := s.readOneShotFileOperations(ctx, record.SubscriptionID)
				if err != nil {
					return OneShotAcquisitionView{}, err
				}
				for opIndex := range operations {
					if operations[opIndex].ArtifactID == artifacts[index].ID {
						view.FileOperation = &operations[opIndex]
						break
					}
				}
				break
			}
		}
	}
	// The first failed stage of the anchored chain.
	view.CurrentAttemptID = oneShotPointerString(record.CurrentAttemptID)
	view.FailedStage = oneShotFailedStage(view, s.oneShotPlanFailed(ctx, record))
	return view, nil
}

func oneShotPointerString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// oneShotPlanFailed reports whether the anchored artifact's post-processing
// plan stage failed (naming failures surface as the planning stage; the
// verified artifact fact itself stays intact).
func (s *Service) oneShotPlanFailed(ctx context.Context, record *animeport.OneShotMarkerRecord) bool {
	if s == nil || s.db == nil || record == nil || record.DownloadTaskID == nil {
		return false
	}
	var plans int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM postprocessing_plans plan
JOIN artifacts a ON a.id = plan.artifact_id
WHERE a.download_task_id = $1 AND plan.state = 'failed'`, *record.DownloadTaskID).Scan(&plans); err != nil {
		return false
	}
	return plans > 0
}

func oneShotOutcomePointer(value *string) *OneShotTerminalOutcome {
	if value == nil {
		return nil
	}
	outcome := OneShotTerminalOutcome(*value)
	return &outcome
}

// optionalStringPointerEqual compares two optional identities by value; both
// nil is equal. An adopted one-shot has no download task on either side.
func optionalStringPointerEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// releaseFilesFromInternalFacts passes the stored release_files through in the
// same shape the evaluation path writes.
func releaseFilesFromInternalFacts(internalFacts []byte) json.RawMessage {
	if len(internalFacts) == 0 {
		return nil
	}
	var stored struct {
		ReleaseFiles json.RawMessage `json:"release_files"`
	}
	if err := json.Unmarshal(internalFacts, &stored); err != nil {
		return nil
	}
	if len(stored.ReleaseFiles) == 0 || string(stored.ReleaseFiles) == "null" {
		return nil
	}
	return stored.ReleaseFiles
}

// oneShotReplayDecision decides marker-owned replay against the locked marker
// row: the same canonical request returns the original handle; a different
// body or selected seed under the same key is a typed conflict. Snapshot
// integrity is checked against the locked row's anchors.
func oneShotReplayDecision(record animeport.OneShotMarkerRecord, requestHash string) (OneShotAdmissionResult, error) {
	if record.RequestHash != requestHash {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different one-shot request."}
	}
	var result OneShotAdmissionResult
	if err := json.Unmarshal(record.ResultSnapshot, &result); err != nil {
		return OneShotAdmissionResult{}, fmt.Errorf("decode one-shot replay snapshot: %w", err)
	}
	if result.OneShotID != record.ID ||
		result.ManagedAnimeID != record.ManagedAnimeID ||
		result.SubscriptionID != record.SubscriptionID ||
		result.RSSSourceID != record.RSSSourceID ||
		result.ReleaseID != record.ReleaseID ||
		!optionalStringPointerEqual(result.DownloadTaskID, record.DownloadTaskID) {
		return OneShotAdmissionResult{}, &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The one-shot replay record is inconsistent with its anchors."}
	}
	return result, nil
}

// IsTemporaryOneShotMount encodes the reclamation-selection rule as code: a
// subscription shape qualifies as a temporary one-shot mount ONLY when a
// marker row exists for it. origin/state alone never qualify, so persistent
// one-shot skeletons (origin=oneshot, no marker) and stopped Subscriptions (any
// origin, no marker) are never reclaimable through the one-shot flow.
func IsTemporaryOneShotMount(origin, state string, markerExists bool) bool {
	_ = origin // provenance fact only; deliberately not a predicate
	_ = state  // automation-intent fact only; deliberately not a predicate
	return markerExists
}

// ErrOneShotReclaiming is the typed worker outcome when the marker left
// `running` or the claimed admission epoch is stale. The canonical sentinel
// lives in workitems; this alias keeps the application vocabulary stable.
var ErrOneShotReclaiming = workitems.ErrOneShotReclaiming

// verifyHumanSeedPreviewBinding proves the selected seed belongs to the exact
// persisted provider feed snapshot identity it declares. It reads that identity
// directly (never "latest"), then  snapshot whose
// provider/entry/group, version and content hash all match and that still
// contains the seed's exact facts. A seed absent from the snapshot is stale or
// was copied from another entry/group: typed SEED_PREVIEW_STALE with no writes
// and a required re-preview before retry. Provider unavailability fails closed
// (503) instead of skipping the check.
//
// The matched snapshot fact is returned because it is the only carrier of the
// seed's AniBT tag set: the caller-supplied locator-form seed cannot carry it
// (the tags are provider XML, not title text, and never part of the request
// contract), so the verified snapshot is where admission reads it.
func (s *Service) verifyHumanSeedPreviewBinding(ctx context.Context, seed SelectedSeed) (animeapplication.SeedPreviewFact, error) {
	if s.seedPreviews == nil {
		return animeapplication.SeedPreviewFact{}, &apperrors.Business{Status: 503, Code: "SEED_PREVIEW_UNAVAILABLE", Message: "Provider seed preview is not configured on this server."}
	}
	if strings.TrimSpace(seed.SnapshotID) == "" || seed.SnapshotVersion < 1 || strings.TrimSpace(seed.SnapshotContentHash) == "" {
		return animeapplication.SeedPreviewFact{}, &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: "所选种子缺少服务端预览快照身份，请重新打开「查看种子」后再试。"}
	}
	identity := animecatalogdomain.ProviderRSSPreviewIdentity{SnapshotID: seed.SnapshotID, SnapshotVersion: seed.SnapshotVersion, ContentHash: seed.SnapshotContentHash}
	var snapshot animeapplication.SeedPreviewSnapshot
	var err error
	if seed.SnapshotKind == string(animecatalogdomain.FeedSnapshotKindSearch) {
		// A search seed is proven against its search snapshot, never against a
		// feed: the source must serve search snapshots and the row must be one.
		searchSource, ok := s.seedPreviews.(animeapplication.SearchSeedPreviewSource)
		if !ok {
			return animeapplication.SeedPreviewFact{}, &apperrors.Business{Status: 503, Code: "SEED_PREVIEW_UNAVAILABLE", Message: "Keyword search snapshots are not served by this server."}
		}
		snapshot, err = searchSource.FetchSearchSnapshotByID(ctx, string(seed.Provider), seed.ProviderEntryRef, seed.ProviderGroupRef, identity)
	} else {
		snapshot, err = s.seedPreviews.FetchEntryGroupSnapshotByID(ctx, string(seed.Provider), seed.ProviderEntryRef, seed.ProviderGroupRef, identity)
	}
	if err != nil {
		if errors.Is(err, animeapplication.ErrProviderFeedSnapshotRefreshRequired) {
			return animeapplication.SeedPreviewFact{}, &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: "所选种子已不在当前预览快照中，请重新打开「查看种子」后再试。"}
		}
		return animeapplication.SeedPreviewFact{}, &apperrors.Business{Status: 503, Code: "SEED_PREVIEW_UNAVAILABLE", Message: animeapplication.SafeReason(err.Error())}
	}
	if snapshot.Freshness != "fresh" || snapshot.Provider != string(seed.Provider) || snapshot.ProviderEntryRef != seed.ProviderEntryRef || snapshot.ProviderGroupRef != seed.ProviderGroupRef || snapshot.SnapshotID != seed.SnapshotID || snapshot.SnapshotVersion != seed.SnapshotVersion || snapshot.ContentHash != seed.SnapshotContentHash {
		return animeapplication.SeedPreviewFact{}, &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: "所选种子已不在当前预览快照中，请重新打开「查看种子」后再试。"}
	}
	for _, fact := range snapshot.Seeds {
		if fact.DownloadURL != seed.Locator || fact.StableSeedKey != seed.StableSeedKey || fact.Title != seed.Title || !fact.PublishedAt.Equal(seed.PublishedAt) || !optionalInt64Equal(fact.SizeBytes, seed.SizeBytes) || fact.InfoHash != optionalStringValue(seed.InfoHash) || normalizeDigestHex(fact.ExpectedPayloadDigest) != normalizeDigestHex(optionalStringValue(seed.ExpectedPayloadDigest)) {
			continue
		}
		return fact, nil
	}
	return animeapplication.SeedPreviewFact{}, &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: "所选种子已不在当前预览快照中，请重新打开「查看种子」后再试。"}
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalInt64Equal(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// validateSelectedSeed enforces the typed structural contract for one
// selected seed. Strong-identity presence is checked separately so the
// SEED_IDENTITY_REQUIRED code stays precise.
func validateSelectedSeed(seed SelectedSeed) error {
	switch seed.Provider {
	case animedomain.ProviderKindAnibt, animedomain.ProviderKindMikan, animedomain.ProviderKindCustom:
	default:
		return &apperrors.Business{Status: 422, Code: "SEED_INVALID", Message: "seed provider must be anibt, mikan or custom."}
	}
	if strings.TrimSpace(seed.ProviderEntryRef) == "" || len(seed.ProviderEntryRef) > 256 {
		return &apperrors.Business{Status: 422, Code: "SEED_INVALID", Message: "seed provider_entry_ref is required and must be at most 256 characters."}
	}
	if strings.TrimSpace(seed.ProviderGroupRef) == "" || len(seed.ProviderGroupRef) > 256 {
		return &apperrors.Business{Status: 422, Code: "SEED_INVALID", Message: "seed provider_group_ref is required and must be at most 256 characters."}
	}
	if strings.TrimSpace(seed.Title) == "" || len(seed.Title) > 512 {
		return &apperrors.Business{Status: 422, Code: "SEED_INVALID", Message: "seed title is required and must be at most 512 characters."}
	}
	if seed.PublishedAt.IsZero() {
		return &apperrors.Business{Status: 422, Code: "SEED_INVALID", Message: "seed published_at is required."}
	}
	if seed.SizeBytes != nil && *seed.SizeBytes < 0 {
		return &apperrors.Business{Status: 422, Code: "SEED_INVALID", Message: "seed size_bytes must be >= 0 when provided."}
	}
	if _, err := animedomain.LocatorURL(seed.Locator); err != nil {
		return &apperrors.Business{Status: 422, Code: "SEED_LOCATOR_UNSAFE", Message: "seed locator must be a bounded absolute HTTP(S) URL passing transport safety."}
	}
	if seed.InfoHash != nil && validateCanonicalInfoHash(*seed.InfoHash) != nil {
		return &apperrors.Business{Status: 422, Code: "SEED_INVALID", Message: "seed info_hash must be canonical 40- or 64-character hex."}
	}
	if seed.ExpectedPayloadDigest != nil && validateSHA256Hex(normalizeDigestHex(*seed.ExpectedPayloadDigest)) != nil {
		return &apperrors.Business{Status: 422, Code: "SEED_INVALID", Message: "seed expected_payload_digest must be a SHA-256 hex digest."}
	}
	return nil
}

// validateAcquireSeedCaller enforces the caller/draft binding matrix:
//
//	human + discovery-provider draft   -> bound to the entry's current preview
//	human + custom draft                -> saved revision: its derived custom scope;
//	                                       current input: any persisted custom seed
//	agent  + agent draft + authorization snapshot -> admitted on strong identities
//
// A custom draft has no origin_entry_ref: its group identity is derived from
// the draft's own frozen locator and group under the exact configuration
// identity, so the seed must name that derived custom-source/custom-group
// pair. Persistent Agent skeletons are never touched, and a human caller
// never gains the agent form.
func validateAcquireSeedCaller(caller AcquireSeedCaller, draft animedomain.AnimeDraft, seed SelectedSeed, input AcquireSeedInput) error {
	switch caller.Kind {
	case AcquireSeedCallerHuman:
		if draft.Origin == animedomain.SubscriptionOriginCustom {
			return validateHumanCustomSeedBinding(draft, seed, input)
		}
		if seed.SnapshotKind == string(animecatalogdomain.FeedSnapshotKindSearch) {
			// The narrow search exception: a keyword search has no catalog entry to
			// bind a draft to, so the search one-shot carries a complete
			// origin=oneshot draft of its own (the form the server already accepts
			// for an agent draft, here reached by a human capability) plus the exact
			// search snapshot. Everything after this check still runs: the seed's
			// exact snapshot identity, its strong content identity and the payload
			// verification. A search seed can never be admitted against a
			// candidate revision, and a non-search seed can never use this form.
			return validateHumanSearchSeedBinding(draft, seed, input)
		}
		if draft.Origin == animedomain.SubscriptionOriginOneshot {
			return &apperrors.Business{Status: 422, Code: "ONE_SHOT_CALLER_INVALID", Message: "A human one-shot request carries a custom or discovery-provider draft; an origin=oneshot draft is the agent form."}
		}
		if string(draft.Origin) != string(seed.Provider) {
			return &apperrors.Business{Status: 422, Code: "ONE_SHOT_CALLER_INVALID", Message: "The draft origin/provider must equal the selected seed provider."}
		}
		if draft.OriginEntryRef == nil || *draft.OriginEntryRef != seed.ProviderEntryRef {
			return &apperrors.Business{Status: 422, Code: "ONE_SHOT_CALLER_INVALID", Message: "The draft origin_entry_ref must equal the selected seed provider_entry_ref."}
		}
		return nil
	case AcquireSeedCallerAgent:
		if draft.Origin != animedomain.SubscriptionOriginOneshot {
			return &apperrors.Business{Status: 422, Code: "ONE_SHOT_CALLER_INVALID", Message: "Agent one-shot requests must carry a complete origin=oneshot draft snapshot."}
		}
		if caller.Agent == nil ||
			strings.TrimSpace(caller.Agent.GrantedBy) == "" || len(caller.Agent.GrantedBy) > 128 ||
			strings.TrimSpace(caller.Agent.AuthorizationRef) == "" || len(caller.Agent.AuthorizationRef) > 256 {
			return &apperrors.Business{Status: 422, Code: "AGENT_AUTHORIZATION_REQUIRED", Message: "Agent one-shot requests require an explicit authorization snapshot."}
		}
		return nil
	default:
		return &apperrors.Business{Status: 422, Code: "ONE_SHOT_CALLER_INVALID", Message: "caller.kind must be human or agent."}
	}
}

// validateHumanCustomSeedBinding binds a human custom seed. A custom draft has
// exactly one custom_rss source (domain-enforced) and no origin_entry_ref.
//
// A saved-revision admission runs that exact revision, so the seed must come
// from its own frozen feed: the custom-source and custom-group refs are the
// SHA-256 of [`<candidate_id>#<revision>`, locator, group], and a seed naming
// any other pair is not from this revision's feed.
//
// A current-input admission is a one-shot's own unsaved configuration (edited
// in the one-shot dialog and never written back to the preset), so no saved
// scope exists to derive. A one-shot names its seed directly: the seed must
// be a custom seed, and its exact persisted snapshot proves it exists
// (verifyHumanSeedPreviewBinding, before any write).
func validateHumanCustomSeedBinding(draft animedomain.AnimeDraft, seed SelectedSeed, input AcquireSeedInput) error {
	if seed.Provider != animedomain.ProviderKindCustom {
		return customSeedBindingError()
	}
	if len(draft.RssSources) != 1 || draft.RssSources[0].SourceKind != animedomain.SourceKindCustomRss {
		return customSeedBindingError()
	}
	if input.isCurrentInput() {
		return nil
	}
	if strings.TrimSpace(input.CandidateID) == "" || input.Revision < 1 {
		return customSeedBindingError()
	}
	source := draft.RssSources[0]
	entryRef, groupRef := catalogapplication.CustomSnapshotScope(fmt.Sprintf("%s#%d", input.CandidateID, input.Revision), source.Locator, source.GroupName)
	if seed.ProviderEntryRef != entryRef || seed.ProviderGroupRef != groupRef {
		return customSeedBindingError()
	}
	return nil
}

// customSeedBindingError is the one typed refusal of a custom seed that does
// not belong to the carried draft's own frozen feed.
func customSeedBindingError() error {
	return &apperrors.Business{Status: 422, Code: "SEED_PREVIEW_STALE", Message: "所选种子不属于当前配置的自定义来源快照，请重新打开「查看种子」后再试。"}
}

// payloadInfoHash is the canonical infohash of one parsed torrent payload:
// v1 when the torrent has one, else v2.
func payloadInfoHash(payload animeapplication.TorrentPayload) string {
	for _, candidate := range []string{payload.Metadata.InfoHashV1, payload.Metadata.InfoHashV2} {
		if value := strings.ToLower(strings.TrimSpace(candidate)); validateCanonicalInfoHash(value) == nil {
			return value
		}
	}
	return ""
}

// replaySeedWithExpandedIdentity lets a locator-form replay of a seed whose feed
// published no identity match its marker: admission bound the expanded
// torrent's infohash into the frozen seed, which the replayed request cannot
// carry. Only that one server-derived field is taken from the frozen seed;
// every other field still has to match for the replay hash to agree.
func replaySeedWithExpandedIdentity(record animeport.OneShotMarkerRecord, input AcquireSeedInput) SelectedSeed {
	seed := input.Seed
	if input.Caller.Kind != AcquireSeedCallerHuman || seedHasStrongIdentity(seed) || !selectedSeedHasSnapshotIdentity(seed) {
		return seed
	}
	snapshot := decodeOneShotInputSnapshot(record.InputSnapshot)
	if snapshot == nil || snapshot.SelectedSeed.InfoHash == nil || snapshot.SelectedSeed.ExpectedPayloadDigest != nil {
		return seed
	}
	value := *snapshot.SelectedSeed.InfoHash
	seed.InfoHash = &value
	return seed
}

// seedHasStrongIdentity reports whether at least one strong content identity
// was supplied. Bencode validity alone never admits a seed.
func seedHasStrongIdentity(seed SelectedSeed) bool {
	return (seed.InfoHash != nil && strings.TrimSpace(*seed.InfoHash) != "") ||
		(seed.ExpectedPayloadDigest != nil && strings.TrimSpace(*seed.ExpectedPayloadDigest) != "")
}

// verifySeedStrongIdentities exact-compares every supplied strong identity
// against the server-refetched payload: the payload SHA-256 against
// expected_payload_digest, and the canonical torrent infohashes (v1 or v2)
// against info_hash. A locator redirected to different content therefore fails
// as an identity mismatch before any write.
func verifySeedStrongIdentities(payload animeapplication.TorrentPayload, seed SelectedSeed) error {
	if seed.ExpectedPayloadDigest != nil && strings.TrimSpace(*seed.ExpectedPayloadDigest) != "" {
		digest := sha256.Sum256(payload.Bytes)
		computed := hex.EncodeToString(digest[:])
		if computed != normalizeDigestHex(*seed.ExpectedPayloadDigest) {
			return &apperrors.Business{Status: 422, Code: "SEED_IDENTITY_MISMATCH", Message: "The fetched torrent payload does not match the expected SHA-256 digest."}
		}
	}
	if seed.InfoHash != nil && strings.TrimSpace(*seed.InfoHash) != "" {
		expected := strings.ToLower(strings.TrimSpace(*seed.InfoHash))
		if payload.Metadata.InfoHashV1 != expected && payload.Metadata.InfoHashV2 != expected {
			return &apperrors.Business{Status: 422, Code: "SEED_IDENTITY_MISMATCH", Message: "The fetched torrent infohash does not match the selected seed identity."}
		}
	}
	return nil
}

// validateCanonicalInfoHash accepts BitTorrent v1 (40 hex) and v2 (64 hex)
// infohashes in lowercase hex form.
func validateCanonicalInfoHash(value string) error {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if len(normalized) != 40 && len(normalized) != 64 {
		return fmt.Errorf("info hash must be 40 or 64 hex characters")
	}
	for _, r := range normalized {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return fmt.Errorf("info hash must be hex")
		}
	}
	return nil
}

func validateSHA256Hex(value string) error {
	if len(value) != 64 {
		return fmt.Errorf("digest must be 64 hex characters")
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return fmt.Errorf("digest must be hex")
		}
	}
	return nil
}

// normalizeDigestHex accepts an optional sha256: prefix and normalizes case.
func normalizeDigestHex(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	return strings.TrimPrefix(trimmed, "sha256:")
}

// canonicalSelectedSeed internalizes a compatible strong-identity spelling as
// the canonical `sha256:<64 hex>` wire form. A bare 64-hex legacy digest is
// accepted at the boundary for compatibility but is never advertised in the
// public contract; an empty digest clears the optional identity.
func canonicalSelectedSeed(seed SelectedSeed) SelectedSeed {
	// Every entry path freezes the same routing fact from the refs; a locator-form
	// request and its reference form therefore cannot diverge before admission.
	seed.SnapshotKind = string(catalogapplication.SnapshotKindFromRefs(seed.ProviderEntryRef, seed.ProviderGroupRef))
	if seed.ExpectedPayloadDigest == nil {
		return seed
	}
	trimmed := strings.TrimSpace(*seed.ExpectedPayloadDigest)
	if trimmed == "" {
		seed.ExpectedPayloadDigest = nil
		return seed
	}
	canonical := "sha256:" + normalizeDigestHex(trimmed)
	seed.ExpectedPayloadDigest = &canonical
	return seed
}

// oneShotCanonicalRequest is the deterministic replay identity: the HTTP
// idempotency key plus the exact saved candidate revision and selected seed.
type oneShotCanonicalRequest struct {
	Kind            string       `json:"kind"`
	IdempotencyKey  string       `json:"idempotency_key"`
	CandidateID     string       `json:"candidate_id"`
	Revision        int64        `json:"revision"`
	EffectiveDigest string       `json:"effective_digest"`
	SelectedSeed    SelectedSeed `json:"selected_seed"`
	// EpisodeOffset is omitted at 0 so an offset-free request keeps its
	// replay identity; any other value is a different request.
	EpisodeOffset int `json:"episode_offset,omitempty"`
}

func canonicalOneShotRequestHash(input AcquireSeedInput, key string) (string, error) {
	return animeapplication.CanonicalJSONHash(oneShotCanonicalRequest{
		Kind:            "acquire_seed_once",
		IdempotencyKey:  key,
		CandidateID:     input.CandidateID,
		Revision:        input.Revision,
		EffectiveDigest: input.EffectiveDigest,
		SelectedSeed:    input.Seed,
		EpisodeOffset:   input.EpisodeOffset,
	})
}

// oneShotCurrentInputCanonicalRequest is the replay identity of a captured
// unsaved-draft admission. It is deliberately request-derived only: the
// idempotency key, the canonical draft digest and the selected seed. No
// resolved value (globals, effective digest) participates, so a replay is
// always decided against the originally captured input, never re-resolved
// against changed configuration.
type oneShotCurrentInputCanonicalRequest struct {
	Kind           string       `json:"kind"`
	IdempotencyKey string       `json:"idempotency_key"`
	DraftDigest    string       `json:"draft_digest"`
	SelectedSeed   SelectedSeed `json:"selected_seed"`
	EpisodeOffset  int          `json:"episode_offset,omitempty"`
}

func canonicalOneShotCurrentInputRequestHash(draftDigest string, input AcquireSeedInput, key string) (string, error) {
	return animeapplication.CanonicalJSONHash(oneShotCurrentInputCanonicalRequest{
		Kind:           "acquire_seed_once_current_input",
		IdempotencyKey: key,
		DraftDigest:    draftDigest,
		SelectedSeed:   input.Seed,
		EpisodeOffset:  input.EpisodeOffset,
	})
}

func oneShotReplayRequestHash(record animeport.OneShotMarkerRecord, input AcquireSeedInput, key string) (string, error) {
	snapshot := decodeOneShotInputSnapshot(record.InputSnapshot)
	if snapshot == nil {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The one-shot replay record has no valid frozen input snapshot."}
	}
	if snapshot.InputSource == AcquireSeedInputSourceCurrentInput {
		return oneShotCurrentInputReplayRequestHash(*snapshot, input, key)
	}
	return oneShotSavedRevisionReplayRequestHash(*snapshot, input, key)
}

// oneShotCurrentInputReplayRequestHash recomputes a captured-input replay
// identity: the request's draft must hash to the stored draft digest and the
// selected seed must match; the stored digest then fixes the request hash.
func oneShotCurrentInputReplayRequestHash(snapshot OneShotInputSnapshot, input AcquireSeedInput, key string) (string, error) {
	if strings.TrimSpace(snapshot.DraftDigest) == "" || len(snapshot.Draft) == 0 {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The one-shot replay record has no captured draft."}
	}
	if !input.isCurrentInput() || input.Draft == nil {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a captured current_input draft."}
	}
	requestDigest, err := animeapplication.CanonicalAnimeDraftHash(*input.Draft)
	if err != nil {
		return "", fmt.Errorf("hash replayed one-shot draft: %w", err)
	}
	if requestDigest != snapshot.DraftDigest {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different captured draft."}
	}
	storedSeedHash, err := animeapplication.CanonicalJSONHash(snapshot.SelectedSeed)
	if err != nil {
		return "", fmt.Errorf("hash stored one-shot selected seed: %w", err)
	}
	requestSeedHash, err := animeapplication.CanonicalJSONHash(input.Seed)
	if err != nil {
		return "", fmt.Errorf("hash replayed one-shot selected seed: %w", err)
	}
	if storedSeedHash != requestSeedHash {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different selected seed."}
	}
	if snapshot.EpisodeOffset != input.EpisodeOffset {
		return "", oneShotOffsetReusedError()
	}
	return canonicalOneShotCurrentInputRequestHash(snapshot.DraftDigest, input, key)
}

// oneShotSavedRevisionReplayRequestHash is the unchanged legacy replay
// identity: the canonical saved candidate revision and selected seed.
func oneShotSavedRevisionReplayRequestHash(snapshot OneShotInputSnapshot, input AcquireSeedInput, key string) (string, error) {
	if strings.TrimSpace(snapshot.CandidateID) == "" || snapshot.Revision < 1 || strings.TrimSpace(snapshot.EffectiveDigest) == "" {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The one-shot replay record has no valid frozen input snapshot."}
	}
	if snapshot.CandidateID != input.CandidateID || snapshot.Revision != input.Revision {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different saved candidate revision."}
	}
	storedSeedHash, err := animeapplication.CanonicalJSONHash(snapshot.SelectedSeed)
	if err != nil {
		return "", fmt.Errorf("hash stored one-shot selected seed: %w", err)
	}
	requestSeedHash, err := animeapplication.CanonicalJSONHash(input.Seed)
	if err != nil {
		return "", fmt.Errorf("hash replayed one-shot selected seed: %w", err)
	}
	if storedSeedHash != requestSeedHash {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different selected seed."}
	}
	if snapshot.EpisodeOffset != input.EpisodeOffset {
		return "", oneShotOffsetReusedError()
	}
	if input.EffectiveDigest != "" && input.EffectiveDigest != snapshot.EffectiveDigest {
		return "", &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different effective configuration."}
	}
	input.EffectiveDigest = snapshot.EffectiveDigest
	return canonicalOneShotRequestHash(input, key)
}

// validateOneShotEpisodeOffset applies the shared episode-offset range to a
// one-shot request: out of range is a typed 422 before any read or write.
func validateOneShotEpisodeOffset(offset int) error {
	if animedomain.EpisodeOffsetInRange(offset) {
		return nil
	}
	return &apperrors.Business{Status: 422, Code: "OFFSET_OUT_OF_RANGE", Message: animedomain.EpisodeOffsetRangeMessage() + "."}
}

func oneShotOffsetReusedError() error {
	return &apperrors.Business{Status: 409, Code: "IDEMPOTENCY_KEY_REUSED", Message: "The idempotency key was already used with a different episode_offset."}
}

// oneShotInstanceLabel derives the display-only instance label required by the
// origin=oneshot mount constraint. Agent drafts carry their own label; human
// provider drafts derive one from the draft title. The label is never an
// execution key.
func oneShotInstanceLabel(draft animedomain.AnimeDraft) string {
	if draft.InstanceLabel != nil {
		if trimmed := strings.TrimSpace(*draft.InstanceLabel); trimmed != "" {
			return truncateRunes(trimmed, 256)
		}
	}
	return truncateRunes("one-shot "+strings.TrimSpace(draft.Title), 256)
}

func truncateRunes(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > max {
		runes = runes[:max]
	}
	if len(runes) == 0 {
		return "one-shot acquisition"
	}
	return string(runes)
}

func encodeOneShotTargetFacts(plan acquisitiontarget.PlanResult, configured bool, input AcquireSeedInput) (json.RawMessage, error) {
	if !configured {
		return json.RawMessage(`{"admission":"download_required","targets":[],"observations":[]}`), nil
	}
	value := struct {
		Admission       string                          `json:"admission"`
		ReasonCode      *string                         `json:"reason_code,omitempty"`
		Reason          *string                         `json:"reason,omitempty"`
		TargetSetDigest *string                         `json:"target_set_digest,omitempty"`
		Targets         []acquisitiontarget.Spec        `json:"targets"`
		Observations    []acquisitiontarget.Observation `json:"observations"`
	}{
		Admission:    string(plan.Admission.Admission),
		Targets:      plan.Set.Targets,
		Observations: plan.Observations,
	}
	// ReasonCode is the typed planner admission reason (e.g.
	// target_not_absolute, parser_identity_unresolved); Reason keeps the
	// human-readable plan detail. Both travel with the frozen input so the
	// post-processing stages surface the exact condition without re-planning.
	if reasonCode := strings.TrimSpace(plan.Admission.Reason); reasonCode != "" {
		value.ReasonCode = &reasonCode
	}
	if plan.PlanReason != "" {
		reason := plan.PlanReason
		value.Reason = &reason
	}
	if plan.Set.Digest != "" {
		digest := plan.Set.Digest
		value.TargetSetDigest = &digest
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode one-shot target facts: %w", err)
	}
	return raw, nil
}

// buildOneShotFeedItem constructs the FeedItem equivalent of the fetched
// torrent so the shared fact encoders and evaluator produce the same shapes as
// RSS refresh. User-facing title/publication come from the selected seed
// snapshot; hashes/files come from the parsed payload.
func buildOneShotFeedItem(payload animeapplication.TorrentPayload, seed SelectedSeed) acquisitiondomain.FeedItem {
	meta := payload.Metadata
	infoHash := meta.InfoHashV1
	if infoHash == "" {
		infoHash = meta.InfoHashV2
	}
	releaseFiles := make([]acquisitiondomain.ReleaseFile, 0, len(meta.Files))
	for _, file := range meta.Files {
		size := file.SizeBytes
		releaseFiles = append(releaseFiles, acquisitiondomain.ReleaseFile{
			SourcePath: file.Path,
			Filename:   file.Name,
			SizeBytes:  &size,
			MediaKind:  file.MediaKind,
		})
	}
	published := seed.PublishedAt
	if published.IsZero() {
		published = time.Now().UTC()
	}
	return acquisitiondomain.FeedItem{
		Title:             strings.TrimSpace(seed.Title),
		Enclosure:         seed.Locator,
		EnclosureType:     "application/x-bittorrent",
		EnclosureLength:   meta.TotalSize,
		TorrentURL:        seed.Locator,
		TorrentInfoHash:   infoHash,
		TorrentInfoHashV1: meta.InfoHashV1,
		TorrentInfoHashV2: meta.InfoHashV2,
		TorrentVersion:    meta.TorrentVersion,
		TorrentFilename:   meta.TorrentName,
		TorrentSizeBytes:  meta.TotalSize,
		TorrentPayload:    payload.Bytes,
		Published:         published,
		ReleaseFiles:      releaseFiles,
		// The AniBT tag set comes from the verified preview snapshot, not from
		// the payload: the tags are provider XML. Rebuilding the item without it
		// would freeze empty source facts and fail every `anibt.*` filter that
		// the subscribed path passes. The provider is carried for the same
		// reason: IsAniBTRelease also reads it, so a snapshot observed before the
		// tag column existed still evaluates as the AniBT release it is.
		Provider:  string(seed.Provider),
		AniBTTags: acquisitiondomain.AniBTTagSetFromStored(seed.AniBTTags),
	}
}

// validateHumanSearchSeedBinding is the narrow, search-only exception to the
// human caller matrix. A keyword search snapshot has no catalog entry and no
// provider group, so no existing draft binding can describe it. The exception
// admits exactly one shape:
//
//   - the seed names a `search` snapshot (never a feed or custom one),
//   - the draft is a complete origin=oneshot draft captured as current_input
//     (never a saved candidate revision), and
//   - the seed's two refs are the derived hashed search scope, not an entry or a
//     group.
//
// It does not relax the provider or custom rules for any other seed, does not
// give a human the agent authorization form, and leaves the exact snapshot
// identity, the strong content identity, the expansion and the payload
// verification afterwards untouched.
func validateHumanSearchSeedBinding(draft animedomain.AnimeDraft, seed SelectedSeed, input AcquireSeedInput) error {
	if draft.Origin != animedomain.SubscriptionOriginOneshot {
		return &apperrors.Business{Status: 422, Code: "ONE_SHOT_CALLER_INVALID", Message: "A keyword search one-shot requires the complete origin=oneshot draft of the selected release."}
	}
	if !input.isCurrentInput() {
		return &apperrors.Business{Status: 422, Code: "ONE_SHOT_CALLER_INVALID", Message: "A keyword search one-shot is a current_input admission; it cannot bind to a saved candidate revision."}
	}
	if seed.Provider != animedomain.ProviderKindAnibt && seed.Provider != animedomain.ProviderKindMikan {
		return &apperrors.Business{Status: 422, Code: "ONE_SHOT_CALLER_INVALID", Message: "A keyword search seed names a discovery provider."}
	}
	if !strings.HasPrefix(seed.ProviderEntryRef, animecatalogdomain.SearchSnapshotScopePrefix) ||
		!strings.HasPrefix(seed.ProviderGroupRef, animecatalogdomain.SearchSnapshotQueryPrefix) {
		return &apperrors.Business{Status: 422, Code: "ONE_SHOT_CALLER_INVALID", Message: "A keyword search seed carries the derived search scope of its snapshot."}
	}
	return nil
}
