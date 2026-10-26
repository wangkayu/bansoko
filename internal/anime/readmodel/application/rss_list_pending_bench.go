package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	animedomain "bansoko/internal/anime/domain"
	catalogapplication "bansoko/internal/catalog/application"
	rssapplication "bansoko/internal/rss/application"
	subscriptiondomain "bansoko/internal/subscription/domain"
)

// A pending (not started) subscription has run nothing, so its owner RSS view
// has no persisted Release to show. Its RSS page is the same test bench a draft
// gets instead: the owner's current configuration is evaluated over the exact
// provider feed snapshots the caller supplies, through the one draft compose
// path, so a bench read of an owner and of a draft can never derive a seed
// differently. The read stays pure: no product write, no provider request and
// no download capability. A custom origin's bench reads the snapshot that the
// explicit owner fetch (POST /anime/{id}/custom-feed-snapshots:fetch) persisted
// under the instance's own scope (OwnerCustomSnapshotIdentity); without one
// its single group reports snapshot_required.

// rssListReasonNotStarted blocks every bench row: a pending subscription
// acquires nothing until it is started.
const rssListReasonNotStarted = "not_started"

// rssListReasonStartBaseline marks a bench row of a subscription with the
// new-releases-only option that was published before the instance was created (or has no
// publish time): once started it would be recorded as start_baseline. The row
// stays read-only like every other bench row.
const rssListReasonStartBaseline = rssapplication.ReleaseVerdictStartBaseline

// pendingBenchAction is the download action of one bench row: always blocked,
// with the start_baseline reason when the instance's frozen baseline covers
// the row.
func pendingBenchAction(subscription Subscription, published *time.Time) RSSListDownloadAction {
	if subscription.NewReleasesOnly && subscription.StartBaselineAt != nil {
		var at time.Time
		if published != nil {
			at = *published
		}
		if rssapplication.StartBaselineCovers(*subscription.StartBaselineAt, at) {
			return RSSListDownloadAction{Kind: RSSListDownloadNone, ReasonCode: rssListReasonStartBaseline, Reason: "Published before subscribing."}
		}
	}
	return RSSListDownloadAction{Kind: RSSListDownloadNone, ReasonCode: rssListReasonNotStarted, Reason: "The subscription has not been started."}
}

// pendingBenchApplies reports whether an owner read is served by the bench: a
// pending provider origin (feed snapshots per group) or a pending custom origin
// (the explicitly fetched snapshot of its single source).
func pendingBenchApplies(workspace AnimeWorkspace) bool {
	if workspace.Subscription.State != subscriptiondomain.SubscriptionPending {
		return false
	}
	origin := animedomain.SubscriptionOrigin(workspace.Subscription.Origin)
	if origin == animedomain.SubscriptionOriginCustom {
		return true
	}
	if origin != animedomain.SubscriptionOriginAnibt && origin != animedomain.SubscriptionOriginMikan {
		return false
	}
	return workspace.Subscription.OriginEntryRef != nil && strings.TrimSpace(*workspace.Subscription.OriginEntryRef) != ""
}

// composePendingOwnerBench evaluates a pending owner's configuration over the
// supplied feed snapshots. effective is the owner's resolved configuration (with
// the unsaved rules patch already applied in owner_draft mode); the group
// selection is replaced by the owner's runtime selection, which is exactly the
// set of groups the first refresh after start reads.
func (s *Service) composePendingOwnerBench(ctx context.Context, mode string, workspace AnimeWorkspace, effective animedomain.EffectiveConfig, rulesOverrides map[string]animedomain.OverrideValue, metadataPreview *MetadataPreviewInput, feed []PreviewFeedSnapshotRef, limit int) (RSSListView, error) {
	business, err := s.ownerBusinessSnapshot(ctx, workspace.ManagedAnime.ID)
	if err != nil {
		return RSSListView{}, err
	}
	draft := pendingBenchDraft(workspace, business)
	// The custom projection derives its snapshot scope from the source locator
	// and the instance's own scope identity (never the versioned bench
	// identity, so a rules edit keeps the fetched snapshot readable).
	scopeIdentity := ""
	if draft.Origin == animedomain.SubscriptionOriginCustom {
		if err := s.fillPendingBenchLocators(ctx, workspace, &draft); err != nil {
			return RSSListView{}, err
		}
		scopeIdentity = catalogapplication.OwnerCustomSnapshotIdentity(workspace.ManagedAnime.ID)
	}
	effective.Selection.Entries = pendingBenchSelection(workspace)
	draft.Subgroups = effective.Selection
	selection, err := s.draftParserSelectionInput(ctx, draft, effective)
	if err != nil {
		return RSSListView{}, err
	}
	identity := fmt.Sprintf("owner_bench:%s@%d.%d.%d", workspace.ManagedAnime.ID, workspace.Subscription.Version, workspace.Subgroups.Version, workspace.Rules.Version)
	if mode == "owner_draft" {
		digest, err := ownerDraftDigest(rulesOverrides, metadataPreview)
		if err != nil {
			return RSSListView{}, err
		}
		identity += "#" + digest
	}
	composeIdentity := identity
	if scopeIdentity != "" {
		composeIdentity = scopeIdentity
	}
	view, err := s.composeRSSListViewFor(ctx, mode, composeIdentity, draft, effective, selection, feed, nil, nil, 0, false)
	if err != nil {
		return RSSListView{}, err
	}
	view.ConfigIdentity = identity
	view.LimitPerGroup = limit
	if view.UnusedFeedIdentities == nil {
		view.UnusedFeedIdentities = []PreviewFeedSnapshotRef{}
	}
	for g := range view.Groups {
		// Offsets are edited on the owner's own link rows, not on the bench.
		view.Groups[g].Offset.Editable = false
		for i := range view.Groups[g].Items {
			view.Groups[g].Items[i].DownloadAction = pendingBenchAction(workspace.Subscription, view.Groups[g].Items[i].Seed.PublishedAt)
		}
	}
	return view, nil
}

// ownerBusinessSnapshot is the instance's frozen business snapshot, which every
// owner save keeps in step with the owner's configuration. A missing snapshot
// stays nil: the evaluation then fails closed for any group with seeds.
func (s *Service) ownerBusinessSnapshot(ctx context.Context, managedAnimeID string) (*animedomain.BusinessSnapshot, error) {
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT business_snapshot FROM anime_business_configs WHERE managed_anime_id = $1 AND business_snapshot IS NOT NULL`, managedAnimeID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var business animedomain.BusinessSnapshot
	if err := json.Unmarshal(raw, &business); err != nil {
		return nil, fmt.Errorf("decode business snapshot: %w", err)
	}
	return &business, nil
}

// pendingBenchDraft is the owner's configuration in the draft shape the compose
// path reads: identity, metadata and the link rows (for their offsets). Source
// locators are not copied here; only a custom bench reads them
// (fillPendingBenchLocators) to derive its snapshot scope.
func pendingBenchDraft(workspace AnimeWorkspace, business *animedomain.BusinessSnapshot) animedomain.AnimeDraft {
	draft := animedomain.AnimeDraft{
		Business:       business,
		InstanceLabel:  workspace.Subscription.InstanceLabel,
		Origin:         animedomain.SubscriptionOrigin(workspace.Subscription.Origin),
		OriginEntryRef: workspace.Subscription.OriginEntryRef,
		Title:          workspace.ManagedAnime.Title,
		TitleOverride:  workspace.ManagedAnime.TitleOverride,
		RssSources:     make([]animedomain.RSSSourceDraft, 0, len(workspace.RSSSources)),
	}
	draft.Metadata.Bgm, draft.Metadata.Tmdb = metadataPatchBindingsForDomain(workspace.Metadata.BGM, workspace.Metadata.TMDB)
	draft.Metadata.TotalEpisodes = workspace.Metadata.TotalEpisodes
	draft.Metadata.MediaKindOverride = workspace.Metadata.MediaKindOverride
	// The owner-edited year is anime.year; without it the bench rendered every
	// saved (or unsaved, owner_draft) year as missing.
	draft.Metadata.Year = workspace.Metadata.Year
	draft.Metadata.Season = workspace.Metadata.Season
	for _, src := range workspace.RSSSources {
		draft.RssSources = append(draft.RssSources, animedomain.RSSSourceDraft{
			AnibtAuxMode:       src.AnibtAuxMode,
			GroupName:          src.GroupName,
			EpisodeOffset:      src.EpisodeOffset,
			ProviderProvenance: src.ProviderProvenance,
			SourceKind:         src.SourceKind,
			Title:              src.Title,
		})
	}
	return draft
}

// fillPendingBenchLocators copies the saved locators of a custom bench's
// sources into its draft: the custom snapshot scope is derived from them.
func (s *Service) fillPendingBenchLocators(ctx context.Context, workspace AnimeWorkspace, draft *animedomain.AnimeDraft) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id, locator FROM rss_sources WHERE subscription_id = $1`, workspace.Subscription.ID)
	if err != nil {
		return fmt.Errorf("read bench source locators: %w", err)
	}
	defer rows.Close()
	locators := make(map[string]string, len(workspace.RSSSources))
	for rows.Next() {
		var id, locator string
		if err := rows.Scan(&id, &locator); err != nil {
			return fmt.Errorf("scan bench source locator: %w", err)
		}
		locators[id] = locator
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read bench source locators: %w", err)
	}
	for i, src := range workspace.RSSSources {
		if i < len(draft.RssSources) {
			draft.RssSources[i].Locator = locators[src.ID]
		}
	}
	return nil
}

// pendingBenchSelection is the owner's runtime group selection: a group is
// selected exactly when the first refresh after start would read it (selected
// and still available upstream).
func pendingBenchSelection(workspace AnimeWorkspace) []animedomain.SubgroupScoreEntry {
	entries := make([]animedomain.SubgroupScoreEntry, 0, len(workspace.Subgroups.EffectiveSelection))
	for _, row := range workspace.Subgroups.EffectiveSelection {
		entries = append(entries, animedomain.SubgroupScoreEntry{
			DisplayName:       row.DisplayName,
			EffectiveSelected: row.EffectiveSelected && row.Available,
			GroupRef:          row.GroupRef,
			ManualSelected:    row.ManualSelected,
			Provider:          animedomain.ProviderKind(row.Provider),
			Score:             row.EffectiveScore,
			SelectionMode:     animedomain.SelectionMode(row.SelectionMode),
		})
	}
	return entries
}
