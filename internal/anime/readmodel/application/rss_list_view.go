package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	acquisitiontarget "bansoko/internal/acquisition/target"
	"bansoko/internal/anime/animeconfig"
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"bansoko/internal/attrrules"
	"bansoko/internal/batchplan"
	catalogapplication "bansoko/internal/catalog/application"
	animecatalogdomain "bansoko/internal/catalog/domain"
	"bansoko/internal/filekinds"
	"bansoko/internal/namingvars"
	varspersistence "bansoko/internal/namingvars/adapter/persistence"
	"bansoko/internal/postprocessing/naming"
	providerdomain "bansoko/internal/provider/domain"
	rssapplication "bansoko/internal/rss/application"
	selectionapplication "bansoko/internal/selection/application"
	selectiondomain "bansoko/internal/selection/domain"

	"github.com/lib/pq"
)

// This file composes the one RSS page read model. It is a read-only join of two
// things that already exist separately: the exact persisted provider feed
// snapshots (catalog facts) and the parser/release/file/naming evaluation (anime
// pipeline). Nothing here writes, and nothing here contacts a provider.
//
// The join is composed once on the server because the browser cannot do it
// reliably: the flat preview result identifies a source only by array position,
// which is not an identity across refreshes or groups.

// ReadRSSListViewInput selects one configuration identity to read.
type ReadRSSListViewInput struct {
	InputSource string
	Draft       *animedomain.AnimeDraft
	CandidateID string
	Revision    int64
	Feed        []PreviewFeedSnapshotRef
	// Observation, for current_input only, names the persisted provider group
	// observation whose groups the preview lists next to the configured ones.
	Observation *RSSListObservationRef
	// RulesPreview carries the unsaved owner rules patch for the owner_draft
	// read mode. It is the live HTTP read-model input; the retired flat rules
	// preview route no longer exists.
	RulesPreview *RulesPreviewInput
	// MetadataPreview, for owner_draft only, is the complete unsaved metadata
	// value the read overlays on the persisted metadata. It is never written.
	MetadataPreview *MetadataPreviewInput
	ManagedAnimeID  string
	LimitPerGroup   int
	// PreviewEpisodeOffset is the read's own episode offset: a preview-only
	// shift applied to a group that has no offset of its own, so a non-zero
	// offset previews exactly what a one-shot carrying that offset renders. A
	// group's own offset (a persisted link's stored value, or the matching
	// draft source) always wins, so this input never rewrites a stored offset
	// and never becomes a second offset rule. Owner modes ignore it for the
	// same reason. 0 (the default) means no shift.
	PreviewEpisodeOffset int
	// OneShot, for current_input only, reads the draft as a one-shot browse: a
	// one-shot names its seed directly, so every listed group (configured and
	// observed) carries its snapshot and its seeds whatever the draft's group
	// selection says, every group previews with PreviewEpisodeOffset (the
	// one-shot request's own offset, never a draft source's), and a custom
	// draft binds the exact persisted snapshot it names instead of a scope
	// derived from a saved revision. The read stays pure.
	OneShot bool
	// FileSelections, for a one-shot read only, are preview-only manual file
	// selections per seed: the named seeds render exactly as a download with
	// that file_selection would place them.
	FileSelections []RSSListFileSelectionInput
}

type RSSListView struct {
	Mode                 string
	ConfigIdentity       string
	MetadataHold         animedomain.MetadataHold
	Versions             RSSListViewVersions
	GeneratedAt          time.Time
	Groups               []RSSListGroup
	UnusedFeedIdentities []PreviewFeedSnapshotRef
	LimitPerGroup        int
	Truncated            bool
	// Observation reports the observation the view was composed with; nil when
	// none was asked for.
	Observation *RSSListObservation
}

// RSSListViewVersions records the derivations the whole view was produced by.
type RSSListViewVersions struct {
	Parser        string
	Evaluator     string
	Renderer      string
	Safety        string
	TorrentParser string
}

// RSSListGroup is one subgroup with its feed state, selection and seeds.
type RSSListGroup struct {
	Scope       RSSListGroupScope
	DisplayName string
	Score       int
	Selection   RSSListGroupSelection
	Offset      RSSListOffset
	Feed        RSSListFeedStatus
	Items       []RSSListItem
}

// RSSListGroupScope is the full identity of one subgroup. Display name, score
// and array position are never identity.
type RSSListGroupScope struct {
	Provider         string
	ProviderEntryRef string
	ProviderGroupRef string
	RSSSourceID      string
}

// RSSListGroupSelection is the server-derived selection state.
type RSSListGroupSelection struct {
	EffectiveSelected bool
	SelectionMode     string
	// Source says whether the group is configured or only observed (RSSListSelectionSource*).
	Source string
	// ReasonCode is the group-selection decision's own reason for this state
	// (selectiondomain.Reason*), so the browser explains an unselected group
	// instead of reconstructing the rules from the booleans. Nil for a group
	// the decision engine did not cover, such as a materialized RSS source.
	ReasonCode *string
}

// RSSListOffset is the episode offset of one RSS link (or of the draft source
// that will become it). There is no inheritance; 0 means no offset.
//
// Editable reports the offset patch's own precondition: only a persisted RSS
// link whose ManagedAnime is still active accepts a save, so draft/preview rows
// without a persisted link and retired anime are read-only.
type RSSListOffset struct {
	Value    int
	Editable bool
	// Source is the provenance of Value (RSSListOffsetSource*).
	Source string
}

// Offset provenance: where the effective offset a view resolved came from.
const (
	// RSSListOffsetSourceLink is a persisted RSS link's stored offset.
	RSSListOffsetSourceLink = "link"
	// RSSListOffsetSourceDraftSource is the matching draft source's offset.
	RSSListOffsetSourceDraftSource = "draft_source"
	// RSSListOffsetSourcePreviewInput is the read input's preview-only offset,
	// used only because the group has no offset of its own.
	RSSListOffsetSourcePreviewInput = "preview_input"
	// RSSListOffsetSourceDefault is no offset anywhere (value 0).
	RSSListOffsetSourceDefault = "default"
)

// rssListOffsetEditable is the lifecycle fence the offset patch takes
// (ownership.PatchRSSSourceOffset locks the ManagedAnime and requires the active
// state). A row with no persisted link never reaches that patch, so its caller
// passes the empty state and stays read-only.
func rssListOffsetEditable(lifecycleState string) bool {
	return lifecycleState == "active"
}

// RSSListFeedStatus is the persisted snapshot state of one group.
type RSSListFeedStatus struct {
	State           string
	SnapshotID      string
	SnapshotVersion int64
	ContentHash     string
	ErrorCode       string
	ErrorReason     string
}

// RSSListItem is one seed with its torrent structure, evaluation and files.
type RSSListItem struct {
	SnapshotID     string
	StableSeedKey  string
	Seed           RSSListSeedFacts
	Torrent        RSSListTorrentState
	Evaluation     RSSListEvaluation
	Files          []RSSListFile
	DownloadAction RSSListDownloadAction
	// LibraryOverlap is the read-only library fact of this seed: whether the
	// library already holds the targets this seed would write.
	LibraryOverlap LibraryOverlap
	// ManifestFiles, for a one-shot read only, lists every manifest file of an
	// expanded seed for the file picker; nil otherwise.
	ManifestFiles []RSSListManifestFile
}

// Library overlap verdicts.
const (
	RSSListLibraryOverlapPresent      = "present"
	RSSListLibraryOverlapAbsent       = "absent"
	RSSListLibraryOverlapUndetermined = "undetermined"
)

// Library overlap reasons. They name why an overlap could not be decided; the
// undetermined verdict is never folded into absent.
const (
	// RSSListLibraryOverlapReasonNoTarget is a file this seed would place whose
	// rendered target is unknown, so no comparison is possible.
	RSSListLibraryOverlapReasonNoTarget = "no_rendered_targets"
	// RSSListLibraryOverlapReasonUnavailable means the library facts could not be
	// read at all; the answer is unknown, never "not held".
	RSSListLibraryOverlapReasonUnavailable = "library_unavailable"
)

// LibraryOverlap is the read-only overlap of one seed's write set with the
// library. It reads active verified placements and writes nothing: no
// placement, no path reservation, no WorkItem. pending_files counts the files
// this seed would still have to write, which is a fact about the library, not a
// promise of a write.
//
// Undetermined is a first-class answer: PresentFiles, PendingFiles and every
// per-file InLibrary are nil then, so an undecidable overlap is never reported
// as "not in the library".
type LibraryOverlap struct {
	Verdict      string
	PresentFiles *int
	PendingFiles *int
	ReasonCode   *string
}

// RSSListSeedFacts are the displayable facts of one feed seed.
type RSSListSeedFacts struct {
	Title                 string
	PublishedAt           *time.Time
	SizeBytes             *int64
	InfoHash              string
	ExpectedPayloadDigest string
	LocatorDisplay        string
	// AniBTTags is the frozen AniBT tag set; nil for non-AniBT releases.
	AniBTTags *[]string
	// TitleTags is the title-parse tag set the title row evaluates (frozen
	// for a persisted release, otherwise read from the seed title).
	TitleTags *[]string
}

// RSSListTorrentState is the torrent acquisition state of one seed.
type RSSListTorrentState struct {
	State       string
	ErrorCode   string
	ErrorReason string
	Manifest    *animecatalogdomain.TorrentManifest
}

// RSSListEvaluation is the parsed and filtered verdict of one seed.
type RSSListEvaluation struct {
	State        string
	Verdict      string
	ReasonCode   string
	ReasonDetail string
	ParsedFacts  *animedomain.ParsedReleaseFacts
}

// A file that failed one of the two hard gates (below the torrent's first level,
// or neither video nor subtitle) is not a candidate and never reaches this
// projection. The plan and the download cleanup still classify it by the same
// codes; only the published row list starts after the gates.
//
// RSSListFile is one projected file with its own verdict and rendered target.
//
// SizeBytes and MediaKind are what the seed's exact torrent manifest declares.
// They stay nil until that manifest is known (and a file an owner row lists
// from its parsed release carries them directly): an unknown file is reported
// as unknown rather than as a 0-byte file of kind "other".
type RSSListFile struct {
	FileRef        string
	Ordinal        int
	SourcePath     string
	Filename       string
	SizeBytes      *int64
	MediaKind      *string
	Language       string
	Included       bool
	ReasonCode     string
	RenderedTarget string
	// EmptyFacts are the naming facts that rendered empty in RenderedTarget.
	EmptyFacts  []string
	RenderError string
	Safety      string
	// BoundVideoSourcePath is the source path of the video a bound subtitle
	// follows. It is the server's own decision (the frozen plan's, or the
	// manifest projection's), empty for a video and for a subtitle bound to no
	// video, so a reader lists the subtitle under that video instead of binding
	// it again itself.
	BoundVideoSourcePath string
	// SubLang, SubLangTag and SubLangSource are a subtitle's resolved
	// language (sub.lang code, its one BCP 47 tag, and file | release); empty
	// for a video and for a subtitle with none. Read-only evidence.
	SubLang       string
	SubLangTag    string
	SubLangSource string
	// InLibrary reports whether the library already holds this file's rendered
	// target under an active verified placement. It is nil when this file has no
	// usable rendered target, and nil for every file while the seed's overlap
	// verdict is undetermined, so an undecidable file is never reported false.
	InLibrary *bool
	// LibraryPath is where an imported file of an owner release is now: the
	// current path of the placement (or companion) holding it, relative to
	// the library. Nil for a file not in the library and outside owner mode.
	LibraryPath *string
	// LibraryNewPath is the path the anime's current naming rules render for
	// that file when it differs from LibraryPath (pending alignment), from
	// the files dialog's renderer; nil otherwise.
	LibraryNewPath *string
}

// RSSListDownloadAction is the server-derived download capability of one item.
type RSSListDownloadAction struct {
	Allowed    bool
	Kind       string
	ReasonCode string
	Reason     string
}

// Torrent state of one item. RSSListTorrentPending is the view's own addition to
// the catalog's acquisition states: the snapshot recorded not_applicable because
// previews no longer fetch torrents, and the seed has not been expanded yet.
// It is a state, not a failure: a failed expansion leaves no cache row and the
// row renders pending again.
const RSSListTorrentPending = "pending"

// Evaluation states of one item.
const (
	RSSListEvaluationEvaluated   = "evaluated"
	RSSListEvaluationNotSelected = "not_selected"
	RSSListEvaluationUnavailable = "unavailable"
	// RSSListEvaluationPending is the evaluation of a seed that was never
	// expanded and that the feed-layer filter did not reject: the list plans
	// and renders nothing for it until it is expanded.
	RSSListEvaluationPending = "pending"
)
const (
	RSSListFeedFresh            = "fresh"
	RSSListFeedStale            = "stale"
	RSSListFeedMissing          = "missing"
	RSSListFeedSuperseded       = "superseded"
	RSSListFeedCorrupt          = "corrupt"
	RSSListFeedRefreshRequired  = "refresh_required"
	RSSListFeedSnapshotRequired = "snapshot_required"
)

// Download action kinds of one item.
const (
	RSSListDownloadOneShot = "one_shot"
	RSSListDownloadRetry   = "retry"
	RSSListDownloadNone    = "none"
)

// Download blocked reasons. They are the server's decision, never a browser
// guess: the frontend renders the reason instead of re-deriving the policy.
const (
	RSSListReasonGroupNotSelected   = "group_not_selected"
	RSSListReasonSnapshotRefreshReq = "snapshot_refresh_required"
	// RSSListReasonStrongIdentityMissing blocks an expanded seed that still has
	// no strong identity: the feed published none and the fetched torrent's
	// infohash cannot be computed.
	RSSListReasonStrongIdentityMissing = "strong_identity_missing"
	// RSSListReasonSeedIdentityMismatch blocks a seed whose feed infohash
	// disagrees with the infohash of the torrent its locator returned.
	RSSListReasonSeedIdentityMismatch = "seed_identity_mismatch"
	// RSSListReasonSeedNotExpanded blocks a download whose seed was never
	// expanded. The gates above it (fresh snapshot, selected group) already
	// passed, so the row is a candidate: expanding it is the expected next step
	// (and, for a feed without an infohash, what establishes its identity), and
	// the kind stays none because nothing is delivered.
	RSSListReasonSeedNotExpanded       = "seed_not_expanded"
	RSSListReasonPublishedAtMissing    = "published_at_missing"
	RSSListReasonTorrentUnavailable    = "torrent_payload_unavailable"
	RSSListReasonEvaluationUnavailable = "evaluation_unavailable"
	RSSListReasonUnsupportedOrigin     = "unsupported_origin"
)

// ReadAnimeRSSListView composes the RSS page read model for one configuration
// identity. It is side-effect free: no product write, no snapshot write and no
// provider or torrent HTTP request. A provider group whose exact snapshot is
// missing, stale or unreadable is reported per group and never silently
// replaced by the latest snapshot.
func (s *Service) ReadAnimeRSSListView(ctx context.Context, input ReadRSSListViewInput) (RSSListView, error) {
	if input.PreviewEpisodeOffset < -1000 || input.PreviewEpisodeOffset > 1000 {
		return RSSListView{}, &apperrors.Business{Status: 422, Code: "OFFSET_OUT_OF_RANGE", Message: "episode_offset must be between -1000 and 1000."}
	}
	if input.OneShot && input.InputSource != "current_input" {
		return RSSListView{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "one_shot is valid only with input_source=current_input."}
	}
	if len(input.FileSelections) > 0 && !input.OneShot {
		return RSSListView{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "file_selections apply to one_shot reads only."}
	}
	switch input.InputSource {
	case "current_input":
		if input.Draft == nil {
			return RSSListView{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "input_source=current_input requires a draft."}
		}
		scope, err := draftCustomSnapshotScope(*input.Draft, input)
		if err != nil {
			return RSSListView{}, err
		}
		return s.readRSSListViewForDraft(ctx, *input.Draft, input.Feed, input.Observation, input.PreviewEpisodeOffset, input.OneShot, scope, newRSSListFileSelections(input.FileSelections))
	case "candidate_revision":
		if strings.TrimSpace(input.CandidateID) == "" || input.Revision < 1 {
			return RSSListView{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "input_source=candidate_revision requires candidate_id and revision."}
		}
		return s.readRSSListViewForRevision(ctx, input.CandidateID, input.Revision, input.Feed, input.PreviewEpisodeOffset)
	case "owner", "owner_draft":
		if strings.TrimSpace(input.ManagedAnimeID) == "" {
			return RSSListView{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "owner modes require managed_anime_id."}
		}
		if input.InputSource == "owner_draft" && input.RulesPreview == nil && input.MetadataPreview == nil {
			return RSSListView{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "owner_draft requires rules_preview, metadata_preview, or both."}
		}
		if input.InputSource != "owner_draft" && (input.RulesPreview != nil || input.MetadataPreview != nil) {
			return RSSListView{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "rules_preview and metadata_preview apply to owner_draft only."}
		}
		if input.MetadataPreview != nil {
			if err := validateMetadataPreview(*input.MetadataPreview); err != nil {
				return RSSListView{}, err
			}
		}
		return s.readRSSListViewForOwner(ctx, input)
	default:
		return RSSListView{}, &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "input_source must be current_input, candidate_revision, owner, or owner_draft."}
	}
}

// draftCustomSnapshotScope is the snapshot scope identity of a custom draft's
// test bench: the saved preset revision (candidate_id + revision) whose
// explicit fetch (POST /catalog/custom/feed-snapshots:fetch) persisted the
// source snapshot. It is the draft counterpart of the pending owner's
// OwnerCustomSnapshotIdentity, and like it binds the locator and group the
// draft names, so an unsaved source edit cannot read the saved source's
// snapshot. Without a saved revision the bench lists its single group as
// snapshot_required. The scope applies to a custom draft only.
func draftCustomSnapshotScope(draft animedomain.AnimeDraft, input ReadRSSListViewInput) (string, error) {
	candidateID := strings.TrimSpace(input.CandidateID)
	if candidateID == "" && input.Revision == 0 {
		return "", nil
	}
	if candidateID == "" || input.Revision < 1 {
		return "", &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "current_input candidate_id and revision go together."}
	}
	if draft.Origin != animedomain.SubscriptionOriginCustom || input.OneShot {
		return "", &apperrors.Business{Status: 400, Code: "INPUT_SOURCE_INVALID", Message: "current_input candidate_id and revision scope a custom draft's source snapshot only."}
	}
	return catalogapplication.CandidateCustomSnapshotIdentity(candidateID, input.Revision), nil
}

func (s *Service) readRSSListViewForDraft(ctx context.Context, draft animedomain.AnimeDraft, feed []PreviewFeedSnapshotRef, observation *RSSListObservationRef, previewOffset int, oneShot bool, customScope string, fileSelections rssListFileSelections) (RSSListView, error) {
	if violations := animedomain.ValidateDraft(draft); len(violations) > 0 {
		return RSSListView{}, draftInvalidError(violations)
	}
	effective, err := s.resolveDraftEffective(ctx, draft)
	if err != nil {
		return RSSListView{}, err
	}
	selection, err := s.draftParserSelectionInput(ctx, draft, effective)
	if err != nil {
		return RSSListView{}, err
	}
	identity, err := canonicalAnimeDraftHash(draft)
	if err != nil {
		return RSSListView{}, err
	}
	configIdentity := "draft:" + identity
	effective, sources, used, _, err := s.applyDraftObservation(ctx, draft, effective, observation)
	if err != nil {
		return RSSListView{}, err
	}
	if used != nil && used.State == RSSListObservationUsed {
		// The observation changes which groups the view lists, so it is part of the
		// identity: two views of one draft over different observations differ.
		configIdentity = fmt.Sprintf("%s#observation:%d", configIdentity, observation.SnapshotVersion)
	}
	if oneShot {
		effective = oneShotBrowseSelection(draft, effective)
		configIdentity += "#one_shot"
		if digest := fileSelections.identity(); digest != "" {
			configIdentity += "#files:" + digest
		}
	}
	if customScope == "" {
		return s.composeRSSListViewFor(ctx, "current_input", configIdentity, draft, effective, selection, feed, sources, used, previewOffset, oneShot, fileSelections)
	}
	// The custom projection derives its snapshot scope from the compose
	// identity; the view keeps the draft's own identity, scoped by the preset
	// revision whose snapshot it reads.
	view, err := s.composeRSSListViewFor(ctx, "current_input", customScope, draft, effective, selection, feed, sources, used, previewOffset, oneShot, fileSelections)
	if err != nil {
		return RSSListView{}, err
	}
	view.ConfigIdentity = configIdentity + "#custom:" + customScope
	return view, nil
}

// applyDraftObservation lists a retained persisted group observation next to
// the draft's configured groups: the observed groups join the effective
// selection, and a provider draft's groups are ranked by the draft's own
// max_rss and manual entries under the live site policy, with or without an
// observation. A custom draft, without an observation, is left as resolved.
// The last map is each ranked group ref's decision.
func (s *Service) applyDraftObservation(ctx context.Context, draft animedomain.AnimeDraft, effective animedomain.EffectiveConfig, observation *RSSListObservationRef) (animedomain.EffectiveConfig, map[string]string, *RSSListObservation, map[string]selectiondomain.GroupSelectionDecision, error) {
	providerDraft := draft.Origin == animedomain.SubscriptionOriginAnibt || draft.Origin == animedomain.SubscriptionOriginMikan
	var observed []ObservedGroupFact
	var used *RSSListObservation
	if observation != nil {
		if err := validateObservationMatchesDraft(draft, *observation); err != nil {
			return effective, nil, nil, nil, err
		}
		persisted, retained, err := s.readGroupObservation(ctx, *observation)
		if err != nil {
			return effective, nil, nil, nil, err
		}
		used = &RSSListObservation{State: RSSListObservationNotRetained, Provider: observation.Provider, ProviderEntryRef: observation.ProviderEntryRef, SnapshotVersion: observation.SnapshotVersion}
		if retained {
			observed = persisted.Groups
			observedAt := persisted.ObservedAt
			used.State, used.ObservedAt, used.GroupCount = RSSListObservationUsed, &observedAt, len(persisted.Groups)
		}
	}
	if !providerDraft {
		return effective, nil, used, nil, nil
	}
	site, err := s.readSitePolicy(ctx)
	if err != nil {
		return effective, nil, nil, nil, err
	}
	effective, sources, decisions, err := applyObservedGroupsWithSite(draft, effective, observed, site)
	if err != nil {
		return effective, nil, nil, nil, err
	}
	if used == nil || used.State != RSSListObservationUsed {
		// Without a retained observation every listed group is configured;
		// the source map marks only an observation's own groups.
		sources = nil
	}
	return effective, sources, used, decisions, nil
}

// readSitePolicy reads the live Global site policy; a read model without a
// database (a pure unit composition) uses the code default.
func (s *Service) readSitePolicy(ctx context.Context) (selectionapplication.SitePolicy, error) {
	if s.db == nil {
		return selectionapplication.DefaultSitePolicy(), nil
	}
	return selectionapplication.ReadSitePolicy(ctx, s.db)
}

// oneShotBrowseSelection lists every group of the draft's provider as
// selected. A one-shot names its seed directly and is never bound by group
// selection, so a one-shot browse reads every configured or observed group's
// snapshot. It changes only this read's projection; the draft is untouched.
func oneShotBrowseSelection(draft animedomain.AnimeDraft, effective animedomain.EffectiveConfig) animedomain.EffectiveConfig {
	provider := animedomain.ProviderKind(draft.Origin)
	entries := make([]animedomain.SubgroupScoreEntry, 0, len(effective.Selection.Entries)+len(draft.Subgroups.Entries))
	seen := make(map[string]struct{}, len(effective.Selection.Entries))
	for _, entry := range effective.Selection.Entries {
		if entry.Provider == provider {
			entry.EffectiveSelected = true
			seen[entry.GroupRef] = struct{}{}
		}
		entries = append(entries, entry)
	}
	for _, entry := range draft.Subgroups.Entries {
		if entry.Provider != provider {
			continue
		}
		if _, ok := seen[entry.GroupRef]; ok {
			continue
		}
		seen[entry.GroupRef] = struct{}{}
		entry.EffectiveSelected = true
		entries = append(entries, entry)
	}
	effective.Selection.Entries = entries
	return effective
}

// resolveDraftEffective resolves the configuration a draft previews with: the
// complete section values the draft stores, exactly as saving it freezes them,
// plus its tag_filters value. Global is never read for a draft.
func (s *Service) resolveDraftEffective(ctx context.Context, draft animedomain.AnimeDraft) (animedomain.EffectiveConfig, error) {
	effective, err := animedomain.ResolveDraftEffective(draft)
	if err != nil {
		return animedomain.EffectiveConfig{}, sectionResolutionError(err)
	}
	return effective, nil
}

func (s *Service) readRSSListViewForRevision(ctx context.Context, candidateID string, revision int64, feed []PreviewFeedSnapshotRef, previewOffset int) (RSSListView, error) {
	row, err := s.ReadVerifiedAnimeConfigCandidateRevision(ctx, candidateID, revision)
	if err != nil {
		return RSSListView{}, err
	}
	effective, err := row.EffectiveConfig()
	if err != nil {
		return RSSListView{}, err
	}
	// tag_filters is not frozen in the revision's six sections: it is the
	// revision draft's own value, as the activated instance will store it.
	if effective.TagFilters, err = animedomain.ResolveDraftTagFilters(row.Draft); err != nil {
		return RSSListView{}, sectionResolutionError(err)
	}
	resolved, err := row.Resolved()
	if err != nil {
		return RSSListView{}, err
	}
	selection, err := row.ParserSelectionInput(resolved)
	if err != nil {
		return RSSListView{}, err
	}
	identity := catalogapplication.CandidateCustomSnapshotIdentity(candidateID, revision)
	return s.composeRSSListView(ctx, "candidate_revision", identity, row.Draft, effective, selection, feed, previewOffset)
}

func (s *Service) readRSSListViewForOwner(ctx context.Context, input ReadRSSListViewInput) (RSSListView, error) {
	limit := input.LimitPerGroup
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return RSSListView{}, &apperrors.Business{Status: 400, Code: "LIMIT_PER_GROUP_INVALID", Message: "limit_per_group must be between 1 and 500."}
	}
	// The list renders at most limit releases per source (one more tells that the
	// source is truncated) and none of the download, artifact or target sections,
	// so the read stops there instead of loading the owner's whole history.
	workspace, err := s.readAnimeWorkspace(ctx, input.ManagedAnimeID, workspaceReadOptions{releasesPerSource: limit + 1, releasesOnly: true, varsEvidence: true, fileIdentity: true})
	if err != nil {
		return RSSListView{}, err
	}
	subgroupsOverride, rulesOverrides, err := s.readOverrides(ctx, input.ManagedAnimeID)
	if err != nil {
		return RSSListView{}, err
	}
	if input.MetadataPreview != nil {
		// The unsaved metadata replaces the read's copy of the persisted value:
		// the hold, the bench draft and the parser binding all read from it.
		overlayWorkspaceMetadata(&workspace, *input.MetadataPreview)
	}
	if input.RulesPreview != nil {
		patched, violations := animedomain.RulesPreviewSectionOverrides(input.RulesPreview.Sections)
		if len(violations) != 0 {
			return RSSListView{}, draftInvalidError(violations)
		}
		// An unsaved patch that omits the optional tag_filters section
		// previews against the stored override, exactly as a save would.
		for _, optional := range []string{animedomain.SectionTagFilters} {
			if _, present := patched[optional]; !present {
				if stored, ok := rulesOverrides[optional]; ok {
					patched[optional] = stored
				}
			}
		}
		rulesOverrides = patched
	}
	effective, err := animedomain.ResolveEffective(subgroupsOverride, rulesOverrides)
	if err != nil {
		return RSSListView{}, sectionResolutionError(err)
	}
	if effective.DownloadCompletion, err = animeconfig.LoadDownloadCompletion(ctx, s.db); err != nil {
		return RSSListView{}, err
	}
	if pendingBenchApplies(workspace) {
		return s.composePendingOwnerBench(ctx, input.InputSource, workspace, effective, rulesOverrides, input.MetadataPreview, input.Feed, limit)
	}
	return s.composeOwnerRSSListView(ctx, input.InputSource, workspace, effective, rulesOverrides, input.MetadataPreview, limit)
}

// ownerDraftDigest is the identity of an owner_draft read's unsaved inputs:
// the patched rules and, when given, the unsaved metadata value.
func ownerDraftDigest(rulesOverrides map[string]animedomain.OverrideValue, metadataPreview *MetadataPreviewInput) (string, error) {
	digest, err := canonicalJSONHash(struct {
		Rules    map[string]animedomain.OverrideValue `json:"rules"`
		Metadata *MetadataPreviewInput                `json:"metadata"`
	}{rulesOverrides, metadataPreview})
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(digest, "sha256:"), nil
}

func (s *Service) composeOwnerRSSListView(ctx context.Context, mode string, workspace AnimeWorkspace, effective animedomain.EffectiveConfig, rulesOverrides map[string]animedomain.OverrideValue, metadataPreview *MetadataPreviewInput, limit int) (RSSListView, error) {
	rulesVersion := workspace.Rules.Version
	identity := fmt.Sprintf("owner:%s@%d", workspace.ManagedAnime.ID, rulesVersion)
	if mode == "owner_draft" {
		digest, err := ownerDraftDigest(rulesOverrides, metadataPreview)
		if err != nil {
			return RSSListView{}, err
		}
		identity = fmt.Sprintf("owner_draft:%s@%d#%s", workspace.ManagedAnime.ID, rulesVersion, digest)
	}
	bgm, tmdb := metadataPatchBindingsForDomain(workspace.Metadata.BGM, workspace.Metadata.TMDB)
	domainHold := animedomain.MetadataHoldFromBindings(bgm, tmdb)
	view := RSSListView{Mode: mode, ConfigIdentity: identity, MetadataHold: domainHold, GeneratedAt: time.Now().UTC(), Groups: []RSSListGroup{}, UnusedFeedIdentities: []PreviewFeedSnapshotRef{}, LimitPerGroup: limit,
		Versions: rssListVersions()}
	rules, _, err := attrrules.Current()
	if err != nil {
		return RSSListView{}, err
	}
	// The instance Vars program is the same one RSS admission freezes for a
	// subscribed anime (target_acquisition). Without it every owner-mode file
	// rendered "exact frozen Vars evaluation is required" (B8 live). A missing
	// program keeps the typed naming_vars_unavailable outcome per file.
	var varsProgram *namingvars.Program
	varsAnime := namingvars.AnimeEvidence{AnimeID: workspace.ManagedAnime.ID}
	if s != nil && s.db != nil {
		if program, loadErr := varspersistence.LoadInstanceProgram(ctx, s.db, workspace.ManagedAnime.ID); loadErr == nil {
			varsProgram = &program
			// The anime evidence acceptance freezes for this subscription,
			// loaded once for every release of the view.
			if varsAnime, err = varspersistence.LoadAnimeEvidence(ctx, s.db, workspace.ManagedAnime.ID); err != nil {
				return RSSListView{}, err
			}
			if metadataPreview != nil {
				overlayVarsAnimeEvidence(&varsAnime, *metadataPreview)
				if err := varspersistence.FillWorkTitles(ctx, s.db, &varsAnime); err != nil {
					return RSSListView{}, err
				}
			}
		}
	}
	unknownRoute, err := s.ownerUnknownRoute(ctx, workspace.ManagedAnime.ID)
	if err != nil {
		return RSSListView{}, err
	}
	// Every group in this path is a persisted RSS link, so the offset is editable
	// exactly while its ManagedAnime is active.
	offsetEditable := rssListOffsetEditable(workspace.Subscription.LifecycleState)
	sourcesByID := make(map[string]RSSSource, len(workspace.RSSSources))
	groups := make(map[string]*RSSListGroup, len(workspace.RSSSources))
	runtimesBySource := make(map[string]RSSSourceRuntime, len(workspace.RSSSourceRuntimes))
	for _, runtime := range workspace.RSSSourceRuntimes {
		runtimesBySource[runtime.RSSSourceID] = runtime
	}
	for _, src := range workspace.RSSSources {
		provider, entryRef, groupRef := "custom", "", src.GroupName
		if p := src.ProviderProvenance; p != nil {
			provider, entryRef, groupRef = string(p.Provider), p.ProviderEntryRef, p.ProviderGroupRef
		}
		sourcesByID[src.ID] = src
		group := RSSListGroup{Scope: RSSListGroupScope{Provider: provider, ProviderEntryRef: entryRef, ProviderGroupRef: groupRef, RSSSourceID: src.ID}, DisplayName: src.GroupName, Selection: RSSListGroupSelection{EffectiveSelected: true, SelectionMode: string(animedomain.SelectionModeAuto)}, Offset: RSSListOffset{Value: src.EpisodeOffset, Editable: offsetEditable, Source: RSSListOffsetSourceLink}, Feed: RSSListFeedStatus{State: RSSListFeedFresh}, Items: []RSSListItem{}}
		if runtime, ok := runtimesBySource[src.ID]; ok {
			group.Feed.State = rssListRuntimeFeedState(runtime.Health)
			if runtime.ReasonCode != nil {
				group.Feed.ErrorCode = *runtime.ReasonCode
			}
			if runtime.ReasonDetail != nil {
				group.Feed.ErrorReason = *runtime.ReasonDetail
			}
		}
		groups[src.ID] = &group
	}
	// An owner link exists only for a group the runtime selected, so its reason
	// is why it won its slot. The persisted section is replayed through the same
	// decision core, one provider at a time; a link the section no longer
	// mentions keeps no reason rather than a guessed one.
	groupKeys := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		if group.Scope.Provider == "custom" {
			continue
		}
		groupKeys[group.Scope.Provider+":"+group.Scope.ProviderGroupRef] = struct{}{}
	}
	keyList := make([]string, 0, len(groupKeys))
	for key := range groupKeys {
		keyList = append(keyList, key)
	}
	reasons, err := animedomain.SubgroupsSectionSelectionReasonsForKeys(effective.Selection, keyList, nil)
	if err != nil {
		return RSSListView{}, sectionResolutionError(err)
	}
	for _, group := range groups {
		// The link row claims the group is selected; only a replay that agrees
		// explains it, so the hint never contradicts the badge beside it.
		if decision, ok := reasons[group.Scope.Provider+":"+group.Scope.ProviderGroupRef]; ok && decision.Selected == group.Selection.EffectiveSelected {
			value := decision.Reason
			group.Selection.ReasonCode = &value
		}
	}
	parserSelections := make(map[string]acquisitiontarget.ParserSelectionInput, len(workspace.RSSSources))
	for _, release := range workspace.Releases {
		src, ok := sourcesByID[release.RSSSourceID]
		if !ok {
			continue
		}
		group := groups[src.ID]
		if len(group.Items) >= limit {
			view.Truncated = true
			continue
		}
		rawFacts, err := json.Marshal(release.RawFacts)
		if err != nil {
			return RSSListView{}, err
		}
		sourceFacts, err := json.Marshal(release.SourceFacts)
		if err != nil {
			return RSSListView{}, err
		}
		parsedFacts, err := json.Marshal(release.ParsedFacts)
		if err != nil {
			return RSSListView{}, err
		}
		item, err := rssapplication.DecodeStoredFeedItem(rawFacts, release.InternalFacts, sourceFacts, parsedFacts, release.StableItemKey)
		if err != nil {
			return RSSListView{}, fmt.Errorf("decode persisted release %s: %w", release.ID, err)
		}
		var sourceGroup providerdomain.GroupRef
		if provenance := src.ProviderProvenance; provenance != nil {
			sourceGroup = providerdomain.GroupRefOrZero(providerdomain.Kind(provenance.Provider), provenance.ProviderGroupRef)
		}
		evalInput := animedomain.SourceEvaluationInput{Index: 0, Kind: string(src.SourceKind), GroupName: src.GroupName, Group: sourceGroup, Offset: src.EpisodeOffset}
		// The parser selection belongs to the source and the owner's configuration,
		// not to the release: it is read once per source.
		selection, loaded := parserSelections[src.ID]
		if !loaded {
			if selection, err = rssapplication.LoadPersistedReleaseParserSelection(ctx, s.db, release.ID); err != nil {
				return RSSListView{}, fmt.Errorf("resolve release parser evidence %s: %w", release.ID, err)
			}
			if metadataPreview != nil {
				selection = overlayParserSelection(selection, *metadataPreview)
			}
			parserSelections[src.ID] = selection
		}
		evidence, err := rssapplication.ResolveParserEvidenceForSelection(ctx, selection, item.Title)
		if err != nil {
			return RSSListView{}, fmt.Errorf("resolve release parser evidence %s: %w", release.ID, err)
		}
		evaluated := animedomain.EvaluateFeedItemWithIdentity(item, evalInput, effective, domainHold, rules, evidence.Identity)
		// The owner row's file projection is rebuilt from the release's frozen
		// file facts, bound to the payload those facts were parsed from. It is
		// not published as a TorrentManifestView (an owner release is already
		// parsed, so the row's torrent state is not_applicable); it is the
		// identity source of each file row, exactly as the seed's own manifest is
		// in preview mode, so both modes derive the same file_ref for the same
		// file.
		manifest := rssListOwnerManifest(release.payloadDigest, release.ReleaseFiles)
		// The acceptance rule is the batch plan: a draft preview classifies the
		// manifest exactly like admission (no deduplication, it never reads
		// the library), so it cannot show accepted what admission filters.
		if mode == "owner_draft" {
			if err := previewBatchPlan(ctx, &evaluated, item, manifest, evidence, effective, unknownRoute, nil); err != nil {
				return RSSListView{}, fmt.Errorf("plan release files %s: %w", release.ID, err)
			}
		}
		if mode == "owner" {
			// The persisted evaluation is the owner's result: the verdict,
			// reason and parsed facts (with the frozen plan's coverage) the
			// release detail shows.
			evaluated.Verdict = animedomain.ReleaseVerdict(release.Verdict)
			evaluated.ReasonCode = release.ReasonCode
			if release.ReasonDetail != nil {
				detail := *release.ReasonDetail
				evaluated.ReasonDetail = &detail
			}
			if release.ParsedFacts != nil {
				evaluated.ParsedFacts = persistedParsedFacts(*release.ParsedFacts)
			}
		}
		// The owner row's seed identity is the release itself. The release's
		// item key is not used: it can fall back to the enclosure URL, which
		// may carry a passkey, and stable_seed_key is not a secret field.
		viewItem := RSSListItem{SnapshotID: "release:" + release.ID, StableSeedKey: release.ID, Seed: RSSListSeedFacts{Title: item.Title, PublishedAt: release.RawFacts.PublishedAt, SizeBytes: release.RawFacts.EnclosureLength, InfoHash: item.TorrentInfoHash, LocatorDisplay: src.LocatorDisplay, ExpectedPayloadDigest: item.TorrentInfoHash}, Torrent: RSSListTorrentState{State: animecatalogdomain.TorrentAcquisitionNotApplicable}, Evaluation: RSSListEvaluation{State: RSSListEvaluationEvaluated, Verdict: string(evaluated.Verdict), ReasonCode: evaluated.ReasonCode, ParsedFacts: evaluated.ParsedFacts}, Files: []RSSListFile{}, DownloadAction: RSSListDownloadAction{Kind: RSSListDownloadNone}}
		if item.AniBTTags != nil {
			tags := append([]string{}, item.AniBTTags.Tags...)
			viewItem.Seed.AniBTTags = &tags
		}
		titleTags := append([]string{}, animedomain.ReleaseTitleTags(item).Tags...)
		viewItem.Seed.TitleTags = &titleTags
		if mode == "owner" && rssListReleaseReevaluating(release, rulesVersion) {
			viewItem.Evaluation.State = "reevaluating"
		}
		if evaluated.ReasonDetail != nil {
			viewItem.Evaluation.ReasonDetail = *evaluated.ReasonDetail
		}
		// Like RSS admission, Vars are frozen only from an exact persisted
		// inventory with every size known; without it the file stays
		// nonrenderable rather than inventing a Vars sample.
		if varsProgram != nil {
			if files, ok := exactVarsFiles(item); ok {
				varsEvidence := varspersistence.PersistedReleaseEvidence(varsAnime, release.ID, item, naming.VarsEpisode(evidence.NamingFacts()), release.rawSourceFacts, release.inputHashV1, release.inputHashV2)
				varsEvidence.Files = files
				frozen := varsProgram.FreezeEvaluation(namingvars.BuildInput(varsEvidence))
				evaluated.FrozenVars = &frozen
			}
		}
		previewFiles, err := previewFilesForItem(ctx, item, release.StableItemKey, evaluated, manifest, evalInput, effective, workspace.ManagedAnime.Title, rules, evidence, unknownRoute, nil)
		if err != nil {
			return RSSListView{}, fmt.Errorf("resolve release file episodes %s: %w", release.ID, err)
		}
		for _, file := range previewFiles {
			if mapped, ok := rssListFileRow(file, manifest); ok {
				viewItem.Files = append(viewItem.Files, mapped)
			}
		}
		if mode == "owner" {
			applyFrozenPlanToFiles(viewItem.Files, release.frozenPlan)
		}
		// The owner row lists the files of the release we already parsed, so
		// each row reports that release's real size and media kind instead of
		// the zero value of an unknown file. The frozen target is an owner-only
		// decision, applyParsedReleaseFiles takes it only there.
		if release.ReleaseFiles != nil {
			applyParsedReleaseFiles(viewItem.Files, *release.ReleaseFiles, mode == "owner")
		}
		if mode == "owner_draft" {
			viewItem.DownloadAction = RSSListDownloadAction{Kind: RSSListDownloadNone, ReasonCode: "preview_mode", Reason: "Unsaved rules preview is read-only."}
		} else {
			viewItem.DownloadAction = rssListOwnerDownloadAction(workspace, release)
		}
		group.Items = append(group.Items, viewItem)
	}
	for _, source := range workspace.RSSSources {
		view.Groups = append(view.Groups, *groups[source.ID])
	}
	if err := s.applyLibraryOverlap(ctx, &view); err != nil {
		return RSSListView{}, err
	}
	if mode == "owner" {
		if err := s.applyLibraryLocations(ctx, workspace.ManagedAnime.ID, &view); err != nil {
			return RSSListView{}, err
		}
	}
	sortRSSListView(&view, false)
	return view, nil
}

// rssListReleaseReevaluating says whether an owner row waits on a
// re-evaluation: its evaluation predates the current rules and the Release is
// one the re-evaluation schedulers select (animeapplication's shared scope). A
// Release submitted for download, adopted, failed in acquisition or a baseline
// is never evaluated again automatically, so it keeps its persisted verdict.
func rssListReleaseReevaluating(release Release, rulesVersion int64) bool {
	submitted := release.DownloadTaskID != nil && *release.DownloadTaskID != ""
	return release.EvaluatedRulesVersion < rulesVersion && animeapplication.ReleaseInReevaluationScope(release.Verdict, submitted)
}

// rssListOwnerDownloadAction is the run-again decision the retry command
// makes (rssapplication.ReleaseRetryability), read from the same facts: a
// stopped subscription does not block it; a download or processing in progress
// and a release whose files are in the library do.
func rssListOwnerDownloadAction(workspace AnimeWorkspace, release Release) RSSListDownloadAction {
	decision := rssapplication.ReleaseRetryability(rssapplication.ReleaseRetryFacts{
		Verdict:           release.Verdict,
		DownloadTaskState: release.currentTaskState,
		AcquisitionState:  release.currentAcquisitionState,
		LifecycleState:    workspace.Subscription.LifecycleState,
		SubscriptionState: string(workspace.Subscription.State),
		PlacementMissing:  release.placementMissing,
	})
	if !decision.Allowed {
		return RSSListDownloadAction{Kind: RSSListDownloadNone, ReasonCode: decision.ReasonCode, Reason: releaseRetryDenials[decision.ReasonCode]}
	}
	return RSSListDownloadAction{Allowed: true, Kind: RSSListDownloadRetry}
}

// releaseRetryDenials explains each reason a release may not run again.
var releaseRetryDenials = map[string]string{
	rssapplication.RetryReasonOwnerRetired:         "订阅已删除，只读。",
	rssapplication.RetryReasonAlreadyPresent:       "文件已在库中。",
	rssapplication.RetryReasonDownloadInProgress:   "正在下载。",
	rssapplication.RetryReasonProcessingInProgress: "下载完成，正在处理文件。",
	rssapplication.RetryReasonAlreadyPlaced:        "文件已在库中。",
	rssapplication.RetryReasonPresentAtResume:      "恢复订阅时已存在，不会下载。",
	rssapplication.RetryReasonNotStarted:           "订阅未开始。",
}

// applyFrozenPlanToFiles makes each file row agree with the batch plan the
// owner's release was last evaluated with: a file the plan does not place is
// never shown included, and carries the plan's reason (or its outcome when the
// plan names none, e.g. ignored_kind). A file the plan places keeps its
// classification row. The plan's own binding is the authority for which video a
// subtitle follows and is backfilled the same way the frozen target is, so the
// row can be listed under that video without the reader binding it again.
func applyFrozenPlanToFiles(files []RSSListFile, plan *batchplan.Plan) {
	if plan == nil {
		return
	}
	for index := range files {
		file := &files[index]
		entry, ok := plan.File(file.SourcePath)
		if !ok {
			continue
		}
		if entry.BoundVideo != "" {
			file.BoundVideoSourcePath = entry.BoundVideo
		}
		if entry.Outcome == "" || plan.Required(file.SourcePath) {
			continue
		}
		file.Included = false
		file.ReasonCode = entry.ReasonCode
		if file.ReasonCode == "" {
			file.ReasonCode = string(entry.Outcome)
		}
	}
}

// applyParsedReleaseFiles fills each file row from the release's parsed files,
// matched by exact source path. The owner row lists the files of a release
// whose torrent metadata was already parsed, so a row with a parsed file must
// report that file's real size and media kind; they are intrinsic facts and
// hold in both owner read modes. The frozen target is an owner-only decision
// and is taken only when frozenTargets is true.
//
// An unparsed file keeps nil size and kind: the read model reports it as
// unknown rather than as 0 bytes of the "other" kind.
func applyParsedReleaseFiles(files []RSSListFile, releaseFiles []ReleaseFile, frozenTargets bool) {
	for _, parsed := range releaseFiles {
		for index := range files {
			if files[index].SourcePath != parsed.Path {
				continue
			}
			if parsed.SizeBytes != nil {
				size := *parsed.SizeBytes
				files[index].SizeBytes = &size
			}
			if parsed.MediaKind != "" {
				kind := parsed.MediaKind
				files[index].MediaKind = &kind
			}
			if frozenTargets && parsed.TargetPath != nil {
				files[index].RenderedTarget = *parsed.TargetPath
				// The frozen target is not this render: its empty facts are unknown.
				files[index].EmptyFacts = nil
			}
		}
	}
}

// persistedParsedFacts converts the stored evaluation facts to the view shape.
func persistedParsedFacts(stored ReleaseParsedFacts) *animedomain.ParsedReleaseFacts {
	facts := &animedomain.ParsedReleaseFacts{Episode: stored.Episode, GroupName: stored.GroupName, Languages: stored.Languages, Quality: stored.Quality, Special: stored.Special}
	if stored.Coverage != nil {
		facts.Coverage = &animedomain.ReleaseCoverage{Season: stored.Coverage.Season, Episodes: append([]int(nil), stored.Coverage.Episodes...)}
	}
	return facts
}

// composeRSSListView evaluates the configuration once per selected provider
// group and assembles the subgroup/seed/file hierarchy from the same resolved
// groups, so the structure and the evaluation can never disagree.
func (s *Service) composeRSSListView(
	ctx context.Context,
	mode string,
	configIdentity string,
	draft animedomain.AnimeDraft,
	effective animedomain.EffectiveConfig,
	selection acquisitiontarget.ParserSelectionInput,
	feed []PreviewFeedSnapshotRef,
	previewOffset int,
) (RSSListView, error) {
	return s.composeRSSListViewObserved(ctx, mode, configIdentity, draft, effective, selection, feed, nil, nil, previewOffset)
}

// composeRSSListViewObserved is composeRSSListView for a configuration whose
// groups may include observed ones: sources maps a group ref to where its
// selection came from (nil means every group is configured) and observation is
// echoed on the view.
func (s *Service) composeRSSListViewObserved(
	ctx context.Context,
	mode string,
	configIdentity string,
	draft animedomain.AnimeDraft,
	effective animedomain.EffectiveConfig,
	selection acquisitiontarget.ParserSelectionInput,
	feed []PreviewFeedSnapshotRef,
	sources map[string]string,
	observation *RSSListObservation,
	previewOffset int,
) (RSSListView, error) {
	return s.composeRSSListViewFor(ctx, mode, configIdentity, draft, effective, selection, feed, sources, observation, previewOffset, false, nil)
}

// composeRSSListViewFor is composeRSSListViewObserved with the one-shot browse
// switch (see ReadRSSListViewInput.OneShot).
func (s *Service) composeRSSListViewFor(
	ctx context.Context,
	mode string,
	configIdentity string,
	draft animedomain.AnimeDraft,
	effective animedomain.EffectiveConfig,
	selection acquisitiontarget.ParserSelectionInput,
	feed []PreviewFeedSnapshotRef,
	sources map[string]string,
	observation *RSSListObservation,
	previewOffset int,
	oneShot bool,
	fileSelections rssListFileSelections,
) (RSSListView, error) {
	hold := animedomain.MetadataHoldFromBindings(draft.Metadata.Bgm, draft.Metadata.Tmdb)
	varsAnime, err := s.draftVarsAnimeEvidence(ctx, draft)
	if err != nil {
		return RSSListView{}, err
	}
	baseline := rssListVersions()
	view := RSSListView{
		Mode:           mode,
		ConfigIdentity: configIdentity,
		MetadataHold:   hold,
		Versions:       baseline,
		GeneratedAt:    time.Now().UTC(),
		Groups:         []RSSListGroup{},
		Observation:    observation,
	}

	var previews []providerGroupPreview
	var groupEntries []rssListGroupEntry
	if draft.Origin == animedomain.SubscriptionOriginCustom {
		previews, groupEntries, err = s.resolveCustomSnapshotProjectionFor(ctx, configIdentity, draft, effective, feed, oneShot, previewOffset)
	} else if draft.Origin == animedomain.SubscriptionOriginAnibt || draft.Origin == animedomain.SubscriptionOriginMikan {
		var unused []PreviewFeedSnapshotRef
		previews, unused, err = s.resolveRSSListProviderGroupPreviews(ctx, draft, effective, feed)
		view.UnusedFeedIdentities = unused
		if err == nil {
			groupEntries, err = rssListGroupEntriesFor(draft, effective, previewOffset, oneShot)
		}
	} else if draft.Origin == animedomain.SubscriptionOriginOneshot {
		// The search one-shot browse. An origin=oneshot draft is the form the
		// admission path binds to one exact keyword search snapshot, so the list
		// read resolves that snapshot and nothing else; a ref that names a feed
		// snapshot is refused rather than answered from it.
		previews, groupEntries, err = s.resolveSearchSnapshotProjection(ctx, draft, feed, previewOffset)
	} else {
		// An origin whose snapshot cannot be resolved still lists its draft's
		// groups. They carry no seeds, and the shared overlap pass at the end of
		// this function still runs, so the read has exactly one place that decides
		// a library verdict.
		view.Groups = s.customOriginGroups(draft)
	}
	if err != nil {
		return RSSListView{}, err
	}
	previewsByGroup := make(map[string]providerGroupPreview, len(previews))
	for _, preview := range previews {
		previewsByGroup[preview.Ref.ProviderGroupRef] = preview
	}

	for _, entry := range groupEntries {
		group := RSSListGroup{
			Scope: RSSListGroupScope{
				Provider:         string(entry.provider),
				ProviderEntryRef: entry.providerEntryRef,
				ProviderGroupRef: entry.groupRef,
			},
			DisplayName: entry.displayName,
			Score:       entry.score,
			Selection: RSSListGroupSelection{
				EffectiveSelected: entry.selected,
				SelectionMode:     entry.selectionMode,
				Source:            selectionSource(sources, entry.groupRef),
				ReasonCode:        entry.reasonCode,
			},
			Offset: entry.offset,
			Feed:   RSSListFeedStatus{State: RSSListFeedMissing},
			Items:  []RSSListItem{},
		}
		preview, hasPreview := previewsByGroup[entry.groupRef]
		if !hasPreview {
			if entry.selected {
				group.Feed.State = RSSListFeedSnapshotRequired
			}
			view.Groups = append(view.Groups, group)
			continue
		}
		group.Feed = RSSListFeedStatus{
			State:           rssListFeedState(preview.Snapshot.Freshness),
			SnapshotID:      preview.Ref.SnapshotID,
			SnapshotVersion: preview.Ref.SnapshotVersion,
			ContentHash:     preview.Ref.ContentHash,
		}
		result := newPreviewEvaluation()
		result.OneShot = oneShot
		result.Selections = fileSelections.forSnapshot(preview.Ref.SnapshotID)
		sourceKind := animedomain.SourceKindProviderGroup
		if draft.Origin == animedomain.SubscriptionOriginCustom {
			sourceKind = animedomain.SourceKindCustomRss
		}
		// Each group previews with its own offset, exactly as a one-shot of that
		// group's seed would resolve it.
		groupSelection := selection
		groupSelection.Offset = entry.offset.Value
		if err := s.evaluateFeedItemsWithSelection(ctx, &result, 0, string(sourceKind), preview.GroupName, providerdomain.GroupRefOrZero(providerdomain.Kind(entry.provider), entry.groupRef), groupSelection, effective, hold, draft.Title, varsAnime, preview.Items, preview.Manifests, draft.Business); err != nil {
			return RSSListView{}, err
		}
		releasesByKey := make(map[string]animedomain.PreviewReleaseResult, len(result.Releases))
		for _, release := range result.Releases {
			releasesByKey[release.StableItemKey] = release
		}
		filesByKey := make(map[string][]animedomain.PreviewFileResult, len(result.Releases))
		for _, file := range result.Files {
			filesByKey[file.StableItemKey] = append(filesByKey[file.StableItemKey], file)
		}
		for _, seed := range preview.Snapshot.Seeds {
			manifest := preview.Manifests[seed.StableSeedKey]
			item := rssListSeedItem(preview.Ref.SnapshotID, seed, manifest)
			release, evaluated := releasesByKey[seed.StableSeedKey]
			if manifest == nil {
				// An unexpanded seed has no structure to plan or render, so it
				// takes the pending row; only a feed-layer rejection, which reads
				// the RSS facts alone and no torrent fact can change, is already
				// its verdict.
				code, detail := animedomain.FeedFilterReason(feedItemFromPreviewSeed(seed, preview.ItemProvider), effective)
				group.Items = append(group.Items, rssListPendingItem(item, code, detail, entry.selected, group.Feed))
				continue
			}
			if evaluated {
				item.Evaluation = rssListReleaseEvaluation(release)
			} else {
				item.Evaluation = RSSListEvaluation{State: RSSListEvaluationUnavailable}
			}
			for _, file := range filesByKey[seed.StableSeedKey] {
				if mapped, ok := rssListFileRow(file, manifest); ok {
					item.Files = append(item.Files, mapped)
				}
			}
			item.ManifestFiles = result.ManifestFiles[seed.StableSeedKey]
			item.DownloadAction = rssListDownloadAction(entry.selected, group.Feed, item)
			group.Items = append(group.Items, item)
		}
		view.Groups = append(view.Groups, group)
	}
	if err := s.applyLibraryOverlap(ctx, &view); err != nil {
		return RSSListView{}, err
	}
	sortRSSListView(&view, true)
	return view, nil
}

// rssListFileSafetyValid is the only safety verdict under which a rendered
// target is a landing path the library could hold.
const rssListFileSafetyValid = "valid"

// errLibraryFactsUnavailable reports that the service has no library to read at
// all (it is unwired), so an overlap is undecidable rather than "nothing is
// held". A read that starts and fails is a different, louder thing.
var errLibraryFactsUnavailable = errors.New("library placement facts are unavailable")

// applyLibraryOverlap annotates every seed of a composed view with its
// read-only library overlap. It is one library read and no write: it creates no
// placement, reserves no path and produces no WorkItem.
//
// The compared set is the seed's write set: the files this seed would actually
// place. A seed whose structure was never established (an unavailable torrent,
// a seed that was never evaluated, a seed with no file row at all), a seed
// whose own write set is not comparable, or a write-set file with no rendered
// target leaves the overlap undecidable, because comparing against a path the
// server cannot compute would be a guess. A service with no library to read at
// all is undecidable for the same reason: the answer is "unknown", not
// "nothing is held". A library read that starts and fails stays a real error
// and fails the read, because a database failure is not a fact about the
// library.
func (s *Service) applyLibraryOverlap(ctx context.Context, view *RSSListView) error {
	if view == nil {
		return nil
	}
	type seedWriteSet struct {
		item        *RSSListItem
		files       []*RSSListFile
		undecidable bool
	}
	sets := make([]seedWriteSet, 0)
	paths := make([]string, 0)
	for groupIndex := range view.Groups {
		group := &view.Groups[groupIndex]
		for itemIndex := range group.Items {
			item := &group.Items[itemIndex]
			set := seedWriteSet{item: item}
			// The write set is comparable only when the seed's own structure was
			// established. An unavailable torrent has no structure to compare
			// against, a seed that was never evaluated has no write set at all, and
			// a seed that carries no file row has no structure the server ever read
			// (a release whose file list was never projected, or torrent facts that
			// were never acquired). Each is "cannot decide", never "nothing is
			// held", so none of them may fall through to an absent verdict.
			if item.Torrent.State == animecatalogdomain.TorrentAcquisitionUnavailable ||
				item.Evaluation.State != RSSListEvaluationEvaluated ||
				len(item.Files) == 0 {
				set.undecidable = true
			}
			for fileIndex := range item.Files {
				file := &item.Files[fileIndex]
				// An excluded file is not part of the write set, and a rejected
				// safety verdict never yields a landing path.
				if !file.Included || file.Safety != rssListFileSafetyValid {
					continue
				}
				// A target that still holds a preview placeholder (a media fact
				// known only after download) cannot be compared with the library.
				if strings.TrimSpace(file.RenderedTarget) == "" || naming.HasPreviewPlaceholder(file.RenderedTarget) {
					set.undecidable = true
					continue
				}
				set.files = append(set.files, file)
				paths = append(paths, file.RenderedTarget)
			}
			sets = append(sets, set)
		}
	}
	held, libraryErr := s.readHeldLibraryPaths(ctx, paths)
	// Only "there is no library to read" degrades to undetermined. A failing
	// library read is a real error and stays one: reporting a database failure
	// as "cannot decide the overlap" would hide the failure behind a field the
	// reader is told to render as unknown.
	if libraryErr != nil && !errors.Is(libraryErr, errLibraryFactsUnavailable) {
		return libraryErr
	}
	for index := range sets {
		set := &sets[index]
		if set.undecidable || libraryErr != nil {
			code := RSSListLibraryOverlapReasonNoTarget
			if libraryErr != nil {
				code = RSSListLibraryOverlapReasonUnavailable
			}
			set.item.LibraryOverlap = LibraryOverlap{Verdict: RSSListLibraryOverlapUndetermined, ReasonCode: &code}
			continue
		}
		present := 0
		for _, file := range set.files {
			inLibrary := held[file.RenderedTarget]
			if inLibrary {
				present++
			}
			value := inLibrary
			file.InLibrary = &value
		}
		pending := len(set.files) - present
		verdict := RSSListLibraryOverlapAbsent
		if present > 0 {
			verdict = RSSListLibraryOverlapPresent
		}
		set.item.LibraryOverlap = LibraryOverlap{Verdict: verdict, PresentFiles: &present, PendingFiles: &pending}
	}
	return nil
}

// readHeldLibraryPaths answers which of these paths the library already holds.
// "Held" means an active placement whose verification is verified: the library
// committed those bytes at that path.
func (s *Service) readHeldLibraryPaths(ctx context.Context, paths []string) (map[string]bool, error) {
	held := make(map[string]bool, len(paths))
	distinct := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, candidate := range paths {
		if _, duplicate := seen[candidate]; duplicate {
			continue
		}
		seen[candidate] = struct{}{}
		distinct = append(distinct, candidate)
	}
	if len(distinct) == 0 {
		return held, nil
	}
	if s == nil || s.db == nil {
		return nil, errLibraryFactsUnavailable
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT p.physical_path
FROM placements p
WHERE p.state = 'active' AND p.verification_state = 'verified'
  AND p.physical_path COLLATE "C" = ANY($1::text[])`, pq.Array(distinct))
	if err != nil {
		return nil, fmt.Errorf("read held library paths: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scan held library path: %w", err)
		}
		held[path] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read held library paths: %w", err)
	}
	return held, nil
}

// rssListGroupEntry is one subgroup with the facts its header needs.
type rssListGroupEntry struct {
	provider         animedomain.ProviderKind
	providerEntryRef string
	groupRef         string
	displayName      string
	score            int
	selected         bool
	selectionMode    string
	reasonCode       *string
	offset           RSSListOffset
}

// rssListGroupEntries lists every configured subgroup with its selection and
// offset, presented in the product's group order (sortRSSListGroupEntries).
// Selection comes from the server-derived effective
// projection, which is the selection authority; the draft only supplies display
// facts. A group the revision does not select stays listable: selection is a
// state, not a reason to hide a group.
func rssListGroupEntries(draft animedomain.AnimeDraft, effective animedomain.EffectiveConfig, previewOffset int) ([]rssListGroupEntry, error) {
	return rssListGroupEntriesFor(draft, effective, previewOffset, false)
}

// oneShotOffset is the offset every group of a one-shot browse previews with:
// the one-shot request's own episode_offset, never a draft source's.
func oneShotOffset(previewOffset int) RSSListOffset {
	if previewOffset != 0 {
		return RSSListOffset{Value: previewOffset, Source: RSSListOffsetSourcePreviewInput}
	}
	return RSSListOffset{Value: 0, Source: RSSListOffsetSourceDefault}
}

func rssListGroupEntriesFor(draft animedomain.AnimeDraft, effective animedomain.EffectiveConfig, previewOffset int, oneShot bool) ([]rssListGroupEntry, error) {
	entryRef := ""
	if draft.OriginEntryRef != nil {
		entryRef = strings.TrimSpace(*draft.OriginEntryRef)
	}
	// A provider draft's offset belongs to the source that will become its RSS
	// link row. Subscribe persists one link row per draft source, and that row's
	// identity is the source's provider provenance triple (the DB provenance sync
	// derives provider/provider_entry_ref/provider_group_ref from it), so the
	// draft side matches a group entry to its source by the same triple. A group
	// with no matching source keeps offset 0.
	offsetsByGroup := make(map[string]int, len(draft.RssSources))
	for _, source := range draft.RssSources {
		provenance := source.ProviderProvenance
		if provenance == nil {
			continue
		}
		if string(provenance.Provider) != string(draft.Origin) || strings.TrimSpace(provenance.ProviderEntryRef) != entryRef {
			continue
		}
		offsetsByGroup[provenance.ProviderGroupRef] = source.EpisodeOffset
	}
	display := make(map[string]animedomain.SubgroupScoreEntry, len(draft.Subgroups.Entries))
	for _, entry := range draft.Subgroups.Entries {
		if string(entry.Provider) != string(draft.Origin) {
			continue
		}
		display[entry.GroupRef] = entry
	}
	// The effective projection is the selection authority. A configured group
	// missing from it is still listed, so a projection gap cannot hide a group.
	ordered := make([]animedomain.SubgroupScoreEntry, 0, len(effective.Selection.Entries)+len(display))
	seen := make(map[string]struct{}, len(effective.Selection.Entries))
	for _, entry := range effective.Selection.Entries {
		if string(entry.Provider) != string(draft.Origin) {
			continue
		}
		if draft, ok := display[entry.GroupRef]; ok && entry.DisplayName == "" {
			entry.DisplayName = draft.DisplayName
		}
		if draft, ok := display[entry.GroupRef]; ok && entry.Score == 0 {
			entry.Score = draft.Score
		}
		ordered = append(ordered, entry)
		seen[entry.GroupRef] = struct{}{}
	}
	for _, entry := range draft.Subgroups.Entries {
		if string(entry.Provider) != string(draft.Origin) {
			continue
		}
		if _, ok := seen[entry.GroupRef]; ok {
			continue
		}
		ordered = append(ordered, entry)
	}

	// The reason behind each entry's selection state comes from the same
	// decision that produced effective_selected, over the very section this
	// read lists, so the explanation and the flag cannot disagree and the
	// browser never re-derives the rules from the booleans. Selection is
	// decided per provider, so the section is restricted to this draft's
	// provider: another provider's entries may share this section and must not
	// appear to consume its slots.
	reasons, err := animedomain.SubgroupsSectionSelectionReasons(animedomain.SubgroupsSectionForProvider(effective.Selection, animedomain.ProviderKind(draft.Origin)), nil)
	if err != nil {
		return nil, err
	}

	entries := make([]rssListGroupEntry, 0, len(ordered))
	for _, entry := range ordered {
		// The offset comes from the draft source this group's link row will be
		// created from. A group with no source of its own previews with the
		// read's preview-only offset, which is how a browser shows a one-shot's
		// non-zero offset before any link exists; the provenance says which.
		offset := RSSListOffset{Value: 0, Source: RSSListOffsetSourceDefault}
		if oneShot {
			offset = oneShotOffset(previewOffset)
		} else if own, ok := offsetsByGroup[entry.GroupRef]; ok {
			offset = RSSListOffset{Value: own, Source: RSSListOffsetSourceDraftSource}
		} else if previewOffset != 0 {
			offset = RSSListOffset{Value: previewOffset, Source: RSSListOffsetSourcePreviewInput}
		}
		var reasonCode *string
		// Attach the reason only when replaying the decision reproduces the
		// entry's own selection state. A stored section whose flag and decision
		// have drifted apart must not be explained by a reason that contradicts
		// the badge the caller renders beside it.
		if decision, ok := reasons[string(entry.Provider)+":"+entry.GroupRef]; ok && decision.Selected == entry.EffectiveSelected {
			value := decision.Reason
			reasonCode = &value
		}
		entries = append(entries, rssListGroupEntry{
			provider:         entry.Provider,
			providerEntryRef: entryRef,
			groupRef:         entry.GroupRef,
			displayName:      entry.DisplayName,
			score:            entry.Score,
			selected:         entry.EffectiveSelected,
			selectionMode:    string(entry.SelectionMode),
			reasonCode:       reasonCode,
			offset:           offset,
		})
	}
	sortRSSListGroupEntries(entries)
	return entries, nil
}

// sortRSSListGroupEntries presents group rows in the product's group order
// (ownership's subgroups_order.go): a selected group lists before an
// unselected one, a higher effective score next, and the display name breaks
// the remaining ties with the group_ref as the deterministic fallback.
func sortRSSListGroupEntries(entries []rssListGroupEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return rssListGroupEntryBefore(entries[i], entries[j])
	})
}

func rssListGroupEntryBefore(a, b rssListGroupEntry) bool {
	if a.selected != b.selected {
		return a.selected
	}
	if a.score != b.score {
		return a.score > b.score
	}
	return rssListGroupEntryNameKey(a) < rssListGroupEntryNameKey(b)
}

func rssListGroupEntryNameKey(entry rssListGroupEntry) string {
	name := strings.TrimSpace(entry.displayName)
	if name == "" {
		name = entry.groupRef
	}
	return strings.ToLower(name) + "\x00" + entry.groupRef
}

// customOriginGroups lists a custom/oneshot draft's own sources. Such a source
// owns no persisted provider feed snapshot, so its feed state is reported as
// missing and no manifest or download capability is fabricated. It takes no
// effective projection: every source a custom draft names is its own group.
func (s *Service) customOriginGroups(draft animedomain.AnimeDraft) []RSSListGroup {
	groups := make([]RSSListGroup, 0, len(draft.RssSources))
	for _, source := range draft.RssSources {
		offset := RSSListOffset{Value: source.EpisodeOffset, Source: RSSListOffsetSourceDraftSource}
		groups = append(groups, RSSListGroup{
			Scope: RSSListGroupScope{
				Provider:         string(draft.Origin),
				ProviderEntryRef: "",
				ProviderGroupRef: strings.TrimSpace(source.GroupName),
			},
			DisplayName: strings.TrimSpace(source.GroupName),
			Selection:   RSSListGroupSelection{EffectiveSelected: true, SelectionMode: string(animedomain.SelectionModeAuto)},
			Offset:      offset,
			Feed:        RSSListFeedStatus{State: RSSListFeedRefreshRequired, ErrorCode: RSSListReasonUnsupportedOrigin, ErrorReason: "该来源没有持久化的 provider feed 快照，无法渲染精确的 torrent 结构。"},
			Items:       []RSSListItem{},
		})
	}
	return groups
}

func rssListRuntimeFeedState(health string) string {
	switch health {
	case "ready":
		return RSSListFeedFresh
	case "degraded":
		return RSSListFeedStale
	case "missing":
		return RSSListFeedMissing
	default:
		return RSSListFeedRefreshRequired
	}
}

func rssListFeedState(freshness string) string {
	switch freshness {
	case "fresh":
		return RSSListFeedFresh
	case "stale":
		return RSSListFeedStale
	case "missing":
		return RSSListFeedMissing
	default:
		return RSSListFeedRefreshRequired
	}
}

// rssListSeedItem is the seed row before any verdict: its identity, the facts
// the RSS feed itself carried, and its torrent state, which a manifest decides.
func rssListSeedItem(snapshotID string, seed SeedPreviewFact, manifest *animecatalogdomain.TorrentManifest) RSSListItem {
	item := RSSListItem{
		SnapshotID:    snapshotID,
		StableSeedKey: seed.StableSeedKey,
		Seed: RSSListSeedFacts{
			Title:                 seed.Title,
			PublishedAt:           optionalSeedTime(seed.PublishedAt),
			SizeBytes:             seed.SizeBytes,
			InfoHash:              seed.InfoHash,
			ExpectedPayloadDigest: seed.ExpectedPayloadDigest,
			LocatorDisplay:        seed.LocatorDisplay,
			AniBTTags:             seedAniBTTags(seed.AniBTTags),
			TitleTags:             seedTitleTags(seed.Title),
		},
		Torrent:        rssListTorrentState(seed, manifest),
		Files:          []RSSListFile{},
		DownloadAction: RSSListDownloadAction{Kind: RSSListDownloadNone},
	}
	return item
}

// seedAniBTTags projects the snapshot's frozen AniBT block onto the row. A nil
// block means the snapshot recorded no tag set and stays nil; an empty set is a
// recorded fact (an AniBT item observed with no tags) and stays an empty list.
func seedAniBTTags(stored *acquisitiondomain.StoredAniBTTags) *[]string {
	if stored == nil {
		return nil
	}
	tags := append([]string{}, stored.Tags...)
	return &tags
}

// rssListReleaseEvaluation is the evaluated verdict of one seed's release.
func rssListReleaseEvaluation(release animedomain.PreviewReleaseResult) RSSListEvaluation {
	evaluation := RSSListEvaluation{
		State:       RSSListEvaluationEvaluated,
		Verdict:     string(release.Verdict),
		ReasonCode:  release.ReasonCode,
		ParsedFacts: release.ParsedFacts,
	}
	if release.ReasonDetail != nil {
		evaluation.ReasonDetail = *release.ReasonDetail
	}
	return evaluation
}

// rssListPendingItem completes the row of a seed the server has not expanded.
// Nothing is planned or rendered — there is no structure to judge it against —
// so the row keeps only the RSS-native facts above and lists no file. Pending
// is the absence of an answer rather than a failure, and the download action
// names the expansion as the step that must happen first while still
// delivering nothing itself (kind none).
//
// The one exception is a seed the feed-layer filter (AniBT tag row, title tag
// row, keyword filter) rejected: that decision reads only the RSS facts the
// row already carries, it is the same decision (animedomain.FeedFilterReason)
// the evaluation of an expanded seed and the subscription pipeline make, and
// no torrent fact can turn it around. The row is evaluated as filtered with
// its reason while its torrent stays pending, so it can still be expanded.
func rssListPendingItem(item RSSListItem, filterCode, filterDetail string, selected bool, feed RSSListFeedStatus) RSSListItem {
	item.Evaluation = RSSListEvaluation{State: RSSListEvaluationPending}
	if filterCode != "" {
		item.Evaluation = RSSListEvaluation{State: RSSListEvaluationEvaluated, Verdict: string(animedomain.ReleaseVerdictFiltered), ReasonCode: filterCode, ReasonDetail: filterDetail}
	}
	item.DownloadAction = rssListDownloadAction(selected, feed, item)
	return item
}

// rssListTorrentState renders one seed's torrent structure. A manifest is the
// proof the seed was expanded — from the snapshot's own sealed projection or
// from the expansion cache — so its presence, and nothing else, decides
// available: the snapshot's persisted state is only pre-expansion evidence.
// Without a manifest a persisted failure stays a failure (the snapshot is the
// record of it), and everything else is pending, which is not a failure at all
// and is what an unexpanded row renders as.
func rssListTorrentState(seed SeedPreviewFact, manifest *animecatalogdomain.TorrentManifest) RSSListTorrentState {
	if manifest != nil {
		return RSSListTorrentState{State: animecatalogdomain.TorrentAcquisitionAvailable, Manifest: manifest}
	}
	if seed.TorrentState == animecatalogdomain.TorrentAcquisitionUnavailable {
		return RSSListTorrentState{State: animecatalogdomain.TorrentAcquisitionUnavailable, ErrorCode: seed.TorrentErrorCode, ErrorReason: seed.TorrentErrorReason}
	}
	return RSSListTorrentState{State: RSSListTorrentPending}
}

// rssListOwnerManifest rebuilds the file projection of one persisted release
// from its frozen file facts. Every file_ref is derived from the payload digest
// of the exact torrent those facts were parsed from — the same binding, and the
// same path rule, the shared torrent projection uses — so an owner row and a
// preview row carry the same identity for the same file. Without that digest,
// or with a file that names no path, no projection can be derived: it is nil
// and no row is listed, rather than a row published with an empty required
// field.
func rssListOwnerManifest(payloadDigest string, files *[]ReleaseFile) *animecatalogdomain.TorrentManifest {
	if payloadDigest == "" || files == nil || len(*files) == 0 {
		return nil
	}
	manifest := &animecatalogdomain.TorrentManifest{PayloadDigest: payloadDigest, Files: make([]animecatalogdomain.TorrentFile, 0, len(*files))}
	for ordinal, file := range *files {
		path := strings.TrimSpace(file.Path)
		if path == "" {
			path = strings.TrimSpace(file.Filename)
		}
		if path == "" {
			return nil
		}
		var size int64
		if file.SizeBytes != nil {
			size = *file.SizeBytes
		}
		language := ""
		if file.Language != nil {
			language = *file.Language
		}
		manifest.Files = append(manifest.Files, animecatalogdomain.TorrentFile{
			FileRef: animecatalogdomain.TorrentFileRef(payloadDigest, path), Ordinal: ordinal,
			SourcePath: path, Filename: file.Filename, SizeBytes: size, MediaKind: file.MediaKind, Language: language,
		})
	}
	return manifest
}

// rssListFileRow projects one preview file into a seed row. manifest is the
// identity source: its payload digest bound every file_ref, and it is the
// seed's own projection in preview mode and the release's rebuilt projection in
// owner mode. ok is false for a file the row list never carries: the two hard
// gates (below the torrent's first level, and a kind that is neither video nor
// subtitle) are what ManifestFileExclusion reports as ignored_nested and
// ignored_kind, and a file that failed them was never a candidate. The
// projection reads that decision instead of recomputing it, and only the row
// list drops the file: the plan and the download cleanup keep accounting for
// it.
func rssListFileRow(file animedomain.PreviewFileResult, manifest *animecatalogdomain.TorrentManifest) (RSSListFile, bool) {
	if file.ReasonCode != nil && isHardGateFileReason(*file.ReasonCode) {
		return RSSListFile{}, false
	}
	return rssListFile(file, manifest), true
}

// isHardGateFileReason reports the two reasons the file-level hard gates carry
// (filekinds.ReasonIgnoredNested / ReasonIgnoredKind).
func isHardGateFileReason(reasonCode string) bool {
	return reasonCode == filekinds.ReasonIgnoredNested || reasonCode == filekinds.ReasonIgnoredKind || reasonCode == filekinds.ReasonIgnoredUnselected
}

func rssListFile(file animedomain.PreviewFileResult, manifest *animecatalogdomain.TorrentManifest) RSSListFile {
	mapped := RSSListFile{
		SourcePath:           file.SourcePath,
		Included:             file.Included,
		Safety:               string(file.Safety),
		BoundVideoSourcePath: file.BoundVideo,
		SubLang:              file.SubLang,
		SubLangTag:           file.SubLangTag,
		SubLangSource:        file.SubLangSource,
	}
	if file.ReasonCode != nil {
		mapped.ReasonCode = *file.ReasonCode
	}
	if file.RenderedTarget != nil {
		mapped.RenderedTarget = *file.RenderedTarget
	}
	if len(file.EmptyFacts) > 0 {
		mapped.EmptyFacts = append([]string(nil), file.EmptyFacts...)
	}
	if file.RenderError != nil {
		mapped.RenderError = *file.RenderError
	}
	if manifest != nil {
		for _, projected := range manifest.Files {
			if projected.SourcePath != file.SourcePath {
				continue
			}
			mapped.FileRef = projected.FileRef
			mapped.Ordinal = projected.Ordinal
			mapped.Filename = projected.Filename
			// A manifest file always declares both facts (the manifest rejects
			// an unlisted media kind and a negative size).
			size := projected.SizeBytes
			kind := projected.MediaKind
			mapped.SizeBytes = &size
			mapped.MediaKind = &kind
			mapped.Language = projected.Language
			break
		}
	}
	if mapped.Filename == "" {
		mapped.Filename = pathBase(file.SourcePath)
	}
	return mapped
}

// rssListDownloadAction decides one row's capability. The decision lives here so
// the browser renders a reason instead of re-deriving the admission policy.
func rssListDownloadAction(selected bool, feed RSSListFeedStatus, item RSSListItem) RSSListDownloadAction {
	block := func(code, reason string) RSSListDownloadAction {
		return RSSListDownloadAction{Allowed: false, Kind: RSSListDownloadNone, ReasonCode: code, Reason: reason}
	}
	if !selected {
		return block(RSSListReasonGroupNotSelected, "该字幕组未被当前配置选中，不能下载。")
	}
	if feed.State != RSSListFeedFresh {
		return block(RSSListReasonSnapshotRefreshReq, "预览快照已过期或缺失，请先刷新种子。")
	}
	if item.Torrent.State == RSSListTorrentPending {
		// Nothing is delivered by this action, so the kind stays none: expanding is
		// the next step and it is not a download. A seed whose feed published no
		// infohash is a candidate too: the fetched torrent's infohash becomes its
		// identity once it is expanded.
		return block(RSSListReasonSeedNotExpanded, "该种子尚未展开，请先展开查看文件结构，再下载。")
	}
	if item.Torrent.State != animecatalogdomain.TorrentAcquisitionAvailable {
		return block(RSSListReasonTorrentUnavailable, "未能获取该种子的 torrent 文件结构，请先刷新种子。")
	}
	// Identity = the feed's infohash (or published digest) when present, else the
	// infohash of the expanded torrent. Both present and different fails closed.
	switch rssListSeedIdentity(item) {
	case rssListIdentityMismatch:
		return block(RSSListReasonSeedIdentityMismatch, "该种子的 torrent 与 RSS 发布的 info_hash 不一致，不能下载。")
	case rssListIdentityMissing:
		return block(RSSListReasonStrongIdentityMissing, "该种子缺少可校验的 info_hash，暂不能下载。")
	}
	if item.Evaluation.State != RSSListEvaluationEvaluated {
		return block(RSSListReasonEvaluationUnavailable, "该种子尚未完成解析，暂不能下载。")
	}
	if item.Seed.PublishedAt == nil {
		return block(RSSListReasonPublishedAtMissing, "该种子缺少发布时间，暂不能下载。")
	}
	return RSSListDownloadAction{Allowed: true, Kind: RSSListDownloadOneShot}
}

type rssListIdentityState int

const (
	rssListIdentityOK rssListIdentityState = iota
	rssListIdentityMissing
	rssListIdentityMismatch
)

// rssListSeedIdentity decides the strong identity of an expanded seed. The
// feed's infohash must name the expanded torrent (its v1 or v2 hash), exactly as
// admission verifies the frozen bytes; a published payload digest must equal the
// manifest's. Without either, the manifest's own infohash is the identity.
func rssListSeedIdentity(item RSSListItem) rssListIdentityState {
	manifest := item.Torrent.Manifest
	feedHash := strings.ToLower(strings.TrimSpace(item.Seed.InfoHash))
	digest := strings.TrimSpace(item.Seed.ExpectedPayloadDigest)
	if manifest == nil {
		if feedHash != "" || digest != "" {
			return rssListIdentityOK
		}
		return rssListIdentityMissing
	}
	if feedHash != "" && manifest.InfoHashV1 != feedHash && manifest.InfoHashV2 != feedHash {
		if manifest.InfoHashV1 != "" || manifest.InfoHashV2 != "" {
			return rssListIdentityMismatch
		}
	}
	if digest != "" && manifest.PayloadDigest != "" && digest != manifest.PayloadDigest {
		return rssListIdentityMismatch
	}
	if feedHash != "" || digest != "" || manifest.InfoHashV1 != "" || manifest.InfoHashV2 != "" {
		return rssListIdentityOK
	}
	return rssListIdentityMissing
}

func pathBase(value string) string {
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}

// optionalSeedTime omits an unset feed timestamp instead of publishing the zero
// instant as if the provider had declared it.
func optionalSeedTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// selectionSource is where one group's selection came from; a group the caller
// did not classify is configured.
func selectionSource(sources map[string]string, groupRef string) string {
	if source, ok := sources[groupRef]; ok {
		return source
	}
	return RSSListSelectionSourceConfigured
}

// exactVarsFiles is the Vars file inventory exactly as RSS admission builds
// it (target_acquisition): every persisted file with its size; any unknown
// size means there is no exact inventory.
func exactVarsFiles(item acquisitiondomain.FeedItem) ([]namingvars.File, bool) {
	files := []namingvars.File{}
	if len(item.ReleaseFiles) == 0 {
		return files, false
	}
	for _, file := range item.ReleaseFiles {
		if file.SizeBytes == nil {
			return files, false
		}
		files = append(files, namingvars.File{Name: path.Base(file.SourcePath), Path: file.SourcePath, Size: *file.SizeBytes})
	}
	return files, true
}

// seedTitleTags is the title-parse tag set of a feed-snapshot seed, read with
// the same pinned parser the title row evaluates.
func seedTitleTags(title string) *[]string {
	tags := append([]string{}, animedomain.ReleaseTitleTags(acquisitiondomain.FeedItem{Title: title}).Tags...)
	return &tags
}

// draftVarsAnimeEvidence is the Vars anime evidence of a configuration draft:
// the draft title, the fixed title an acceptance writes as the override, the
// provider catalog title the subscription will freeze, and the binding (what
// an acceptance of this draft freezes) and the bound works' recorded titles. A
// draft names no managed anime yet.
func (s *Service) draftVarsAnimeEvidence(ctx context.Context, draft animedomain.AnimeDraft) (namingvars.AnimeEvidence, error) {
	evidence := namingvars.AnimeEvidence{AnimeTitle: draft.Title}
	// Acceptance freezes the edited title as title_override, which outranks
	// every provider and metadata title; the preview must rank it the same.
	if fixed := animeapplication.FixedTitle(draft.Title, draft.TitleOverride); fixed != nil {
		evidence.AnimeTitleOverride = *fixed
	}
	providerTitle, err := s.draftProviderTitle(ctx, draft)
	if err != nil {
		return evidence, err
	}
	evidence.AnimeProviderTitle = providerTitle
	if y := draft.Metadata.Year; y != nil {
		year := int64(*y)
		evidence.AnimeYear = &year
	}
	if s := draft.Metadata.Season; s != nil {
		season := int64(*s)
		evidence.AnimeSeason = &season
	}
	if b := draft.Metadata.Bgm; b != nil {
		evidence.BgmID = b.WorkId
	}
	if b := draft.Metadata.Tmdb; b != nil {
		season := int64(b.Season)
		evidence.TmdbID, evidence.TmdbKind, evidence.TmdbSeason = b.WorkId, string(b.MediaKind), &season
	}
	return evidence, varspersistence.FillWorkTitles(ctx, s.varsQuerier(), &evidence)
}

// draftProviderTitle is the provider catalog title a subscription of this
// draft freezes: the latest observed title of its origin entry, normalized by
// the same rule the subscribe transaction uses. A custom/oneshot draft and an
// entry the catalog does not carry have no provider title.
//
// Two snapshots can observe the same entry at the same instant, so the order
// breaks the tie on id. The subscribe transaction reads the title with the same
// order (draft providerTitleTx), so the preview shows the row the subscription
// freezes.
func (s *Service) draftProviderTitle(ctx context.Context, draft animedomain.AnimeDraft) (string, error) {
	if draft.Origin != animedomain.SubscriptionOriginAnibt && draft.Origin != animedomain.SubscriptionOriginMikan {
		return "", nil
	}
	if draft.OriginEntryRef == nil || strings.TrimSpace(*draft.OriginEntryRef) == "" {
		return "", nil
	}
	querier := s.varsQuerier()
	if querier == nil {
		return "", nil
	}
	var title string
	err := querier.QueryRowContext(ctx, `
SELECT title FROM provider_catalog_entries
WHERE provider=$1 AND provider_entry_ref=$2
ORDER BY observed_at DESC, id DESC LIMIT 1`, string(draft.Origin), strings.TrimSpace(*draft.OriginEntryRef)).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if normalized := animedomain.ProviderCatalogTitle(title); normalized != nil {
		return *normalized, nil
	}
	return "", nil
}

// ownerUnknownRoute reads the instance's own unclassified_route the library plan
// of this anime uses, so the file preview shows unknown files the way they will
// be handled. Global is never read; an instance without one fails closed.
func (s *Service) ownerUnknownRoute(ctx context.Context, animeID string) (string, error) {
	if s == nil || s.db == nil {
		return animedomain.UnknownRouteSkip, nil
	}
	var route sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT unclassified_route FROM anime_business_configs WHERE managed_anime_id = $1`, animeID).Scan(&route)
	if err != nil {
		return "", fmt.Errorf("read unclassified route: %w", err)
	}
	if !route.Valid {
		return "", fmt.Errorf("anime %s has no unclassified route", animeID)
	}
	return route.String, nil
}
