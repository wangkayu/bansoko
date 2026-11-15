package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"bansoko/internal/templatecatalog/notification"
)

var (
	anibtReleaseIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
	infoHashV1Pattern     = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
)

// enrichBusinessDeliveryLinkFacts derives safe outbound locators at the
// delivery boundary. The original admission and file-proof payloads remain
// unchanged; only the frozen delivery copy receives these derived facts.
func enrichBusinessDeliveryLinkFacts(ctx context.Context, tx *sql.Tx, facts map[string]any) error {
	releaseID := notificationFactString(facts, "release.id")
	if releaseID == "" {
		return nil
	}
	var internalFacts []byte
	var sourceKind string
	err := tx.QueryRowContext(ctx, `
SELECT r.internal_facts, s.source_kind
FROM releases r
JOIN rss_sources s ON s.id=r.rss_source_id
WHERE r.id=$1`, releaseID).Scan(&internalFacts, &sourceKind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("derive notification release links: %w", err)
	}
	applyBusinessDeliveryLinkFacts(facts, sourceKind, internalFacts)
	return nil
}

func applyBusinessDeliveryLinkFacts(facts map[string]any, sourceKind string, internalFacts []byte) {
	var stored struct {
		GUID              string `json:"guid"`
		Link              string `json:"link"`
		Enclosure         string `json:"enclosure"`
		TorrentURL        string `json:"torrent_url"`
		DownloadURL       string `json:"download_url"`
		TorrentInfoHashV1 string `json:"torrent_info_hash_v1"`
	}
	if len(internalFacts) > 0 {
		if err := json.Unmarshal(internalFacts, &stored); err != nil {
			// A malformed internal snapshot cannot contribute a safe URL.
			return
		}
	}
	provider := strings.ToLower(strings.TrimSpace(sourceKind))
	if provider == "" {
		provider = strings.ToLower(notificationFactString(facts, "release.src"))
	}
	switch provider {
	case "anibt":
		candidate := strings.TrimSpace(stored.Link)
		if candidate == "" && anibtReleaseIDPattern.MatchString(strings.TrimSpace(stored.GUID)) {
			candidate = "https://anibt.net/release/" + strings.TrimSpace(stored.GUID)
		}
		facts[notification.AniBTLinkVariable] = candidate
		facts[notification.AniBTLinkVariable] = notification.LinkValue(facts, notification.AniBTLinkVariable)
	case "mikan":
		candidate := strings.TrimSpace(stored.Link)
		if candidate == "" && infoHashV1Pattern.MatchString(strings.TrimSpace(stored.TorrentInfoHashV1)) {
			candidate = "https://mikanani.me/Home/Episode/" + strings.ToLower(strings.TrimSpace(stored.TorrentInfoHashV1))
		}
		facts[notification.MikanLinkVariable] = candidate
		facts[notification.MikanLinkVariable] = notification.LinkValue(facts, notification.MikanLinkVariable)
	}
	for _, candidate := range []string{stored.DownloadURL, stored.TorrentURL, stored.Enclosure} {
		if safe := notification.SafeExternalURL(candidate, false); safe != "" {
			facts[notification.DownloadLinkVariable] = safe
			break
		}
	}
}
