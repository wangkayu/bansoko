package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/batchplan"
	"bansoko/internal/persistence/txscope"
	plugintargetport "bansoko/internal/plugintarget/port"
	"bansoko/internal/templatecatalog/notification"
	"bansoko/internal/torrenttree"
)

// SetBusinessDeliveryEnqueuer wires the release_discovered notification
// admission. Without it no discovery notice is admitted.
func (s *Service) SetBusinessDeliveryEnqueuer(enqueuer plugintargetport.BusinessDeliveryEnqueuer) {
	if s == nil {
		return
	}
	s.notifications = enqueuer
}

// sourceHasBaselineTx reports whether the source already completed a
// successful refresh before this one. The first successful scan of a new
// subscription or a newly linked source only records the baseline: items
// already in the feed are history, not discoveries.
func sourceHasBaselineTx(ctx context.Context, tx *sql.Tx, sourceID string) (bool, error) {
	var baseline bool
	err := tx.QueryRowContext(ctx, `
SELECT last_success_at IS NOT NULL FROM rss_source_runtime WHERE rss_source_id = $1`, sourceID).Scan(&baseline)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read RSS source baseline: %w", err)
	}
	return baseline, nil
}

// sourceResumeBaselinePendingTx reports whether this refresh is the source's
// first successful one since its subscription was resumed. Only a source whose
// runtime already existed at the resume qualifies; a source linked later takes
// the ordinary first-scan baseline. The caller holds the subscription row lock,
// so the resume stamp cannot move between this read and the runtime write that
// copies it.
func sourceResumeBaselinePendingTx(ctx context.Context, tx *sql.Tx, sourceID string) (bool, error) {
	var pending bool
	err := tx.QueryRowContext(ctx, `
SELECT sub.resumed_at IS NOT NULL AND rt.created_at < sub.resumed_at
       AND rt.resume_baseline_at IS DISTINCT FROM sub.resumed_at
FROM rss_source_runtime AS rt
JOIN rss_sources AS src ON src.id = rt.rss_source_id
JOIN subscriptions AS sub ON sub.id = src.subscription_id
WHERE rt.rss_source_id = $1`, sourceID).Scan(&pending)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read RSS source resume baseline: %w", err)
	}
	return pending, nil
}

// markReleaseDiscoveryTx marks a release a refresh created after the source's
// baseline. Its first evaluation that passes filtering admits one
// release_discovered notice and clears the mark.
func markReleaseDiscoveryTx(ctx context.Context, tx *sql.Tx, releaseID string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET discovery_notice_due = TRUE WHERE id = $1`, releaseID); err != nil {
		return fmt.Errorf("mark release discovery: %w", err)
	}
	return nil
}

// discoveryVerdictSettles reports how one evaluation verdict settles a pending
// discovery notice: notify when the release passed filtering, drop the notice
// when it was filtered out or the subscription was stopped, and keep it while
// the release could not be evaluated yet (a later evaluation decides).
func discoveryVerdictSettles(verdict string) (notify, drop bool) {
	switch verdict {
	case "filtered", "subscription_stopped":
		return false, true
	case "parse_failed", "source_unavailable":
		return false, false
	default:
		return true, false
	}
}

// settleReleaseDiscoveryTx runs inside the release owner write, after the
// evaluated verdict is stored. A release marked by its refresh admits one
// release_discovered notice the first time an automatic evaluation passes
// filtering; the admission key (release, anime) makes it once only. A manual
// retry never notifies: it is not a discovery.
func (s *Service) settleReleaseDiscoveryTx(ctx context.Context, tx *sql.Tx, snapshot releaseSnapshot, item acquisitiondomain.FeedItem, verdict string, evidence *releaseParserEvidence, plan *batchplan.Plan) error {
	if snapshot.manualRetry {
		return nil
	}
	notify, drop := discoveryVerdictSettles(verdict)
	if !notify && !drop {
		return nil
	}
	var due bool
	if err := tx.QueryRowContext(ctx, `
UPDATE releases SET discovery_notice_due = FALSE
WHERE id = $1 AND discovery_notice_due
RETURNING TRUE`, snapshot.releaseID).Scan(&due); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("settle release discovery: %w", err)
	}
	if !notify || s.notifications == nil {
		return nil
	}
	var animeTitle, titleOverride string
	if err := tx.QueryRowContext(ctx, `
SELECT title, COALESCE(title_override, '') FROM managed_anime WHERE id = $1`, snapshot.managedAnimeID).Scan(&animeTitle, &titleOverride); err != nil {
		return fmt.Errorf("read release discovery anime: %w", err)
	}
	if titleOverride != "" {
		animeTitle = titleOverride
	}
	payload, err := json.Marshal(releaseDiscoveredFacts(snapshot, item, animeTitle, evidence, plan))
	if err != nil {
		return fmt.Errorf("encode release discovery facts: %w", err)
	}
	if err := s.notifications.EnqueueBusinessDelivery(ctx, txscope.Wrap(tx), plugintargetport.BusinessDeliveryRequest{
		SourceOwnerType: plugintargetport.OwnerRelease, SourceOwnerID: snapshot.releaseID, SourceOwnerVersion: 1,
		ManagedAnimeID: snapshot.managedAnimeID, TriggerKind: plugintargetport.TriggerReleaseDiscovered,
		Payload: payload, ResultKey: "anime:" + snapshot.managedAnimeID,
	}); err != nil {
		return fmt.Errorf("enqueue release discovered notice: %w", err)
	}
	return nil
}

// releaseTreeFiles projects the release's stored file list onto the listing
// input: a file whose size was never recorded counts as zero bytes.
func releaseTreeFiles(files []acquisitiondomain.ReleaseFile) []torrenttree.File {
	out := make([]torrenttree.File, 0, len(files))
	for _, file := range files {
		var size int64
		if file.SizeBytes != nil {
			size = *file.SizeBytes
		}
		out = append(out, torrenttree.File{Path: file.SourcePath, SizeBytes: size})
	}
	return out
}

// releaseDiscoveredFacts builds the release_discovered payload under the
// unified fact paths: the anime, the release, its source, the episode the
// selected parser froze and the batch plan summary (episodes covered and the
// torrent's file count). The group is the source's configured group name
// (anime.group), never the release title's parsed group.
func releaseDiscoveredFacts(snapshot releaseSnapshot, item acquisitiondomain.FeedItem, animeTitle string, evidence *releaseParserEvidence, plan *batchplan.Plan) map[string]any {
	text := func(value string) any {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 1024 {
			return nil
		}
		return value
	}
	facts := map[string]any{
		"anime.id": snapshot.managedAnimeID, "anime.title": text(animeTitle),
		"release.src": text(snapshot.sourceKind), "anime.group": text(snapshot.groupName),
		"release.id":           text(snapshot.releaseID),
		"release.title":        text(item.Title),
		"release.published_at": nil, "release.episodes": nil,
		"ep.number": nil, "ep.raw": nil, "ep.display": nil,
		"torrent.file_count": nil, "torrent.size": nil, "torrent.tree": nil,
	}
	if !item.Published.IsZero() {
		facts["release.published_at"] = item.Published.UTC().Format(time.RFC3339)
	}
	if evidence != nil && evidence.Identity.State == "resolved" && evidence.Identity.Episode != nil {
		episode := string(*evidence.Identity.Episode)
		facts["ep.number"], facts["ep.display"] = episode, episode
		if evidence.Identity.RawEpisode != nil {
			facts["ep.raw"] = string(*evidence.Identity.RawEpisode)
		}
	}
	if plan != nil {
		if _, episodes, ok := plan.Episodes(); ok {
			facts["release.episodes"] = batchplan.EpisodesLabel(episodes)
			if len(episodes) > 1 {
				// A batch has no single episode.
				facts["ep.number"], facts["ep.raw"], facts["ep.display"] = nil, nil, nil
			}
		}
	}
	if len(item.ReleaseFiles) > 0 {
		facts["torrent.file_count"] = len(item.ReleaseFiles)
		facts["torrent.tree"] = torrenttree.Render(releaseTreeFiles(item.ReleaseFiles))
	}
	if size := releaseSize(item); size != nil {
		facts["torrent.size"] = *size
	}
	for key, value := range notification.ValueFacts(nil, snapshot.sourceFacts, text) {
		facts[key] = value
	}
	return facts
}
