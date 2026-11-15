package application

import (
	"encoding/json"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/templatecatalog/notification"
)

// applyBusinessDeliverySourceFacts reads only the admission's captured evidence.
// Neither delayed resolution nor retry may consult a mutable release/RSS row.
func applyBusinessDeliverySourceFacts(facts map[string]any, trigger string, evidence []byte) {
	switch trigger {
	case "download_submitted", "download_failed", "file_operation_completed", "file_operation_failed", "unknown_files_imported", "release_discovered", "replacement_applied":
	default:
		return
	}
	var source struct {
		Provider    string                            `json:"provider"`
		DetailsURL  string                            `json:"details_url"`
		DownloadURL string                            `json:"download_url"`
		SourceSite  *acquisitiondomain.SeedSourceSite `json:"source_site"`
	}
	for _, key := range []string{"release.src", "release.site", "release.link", notification.AniBTLinkVariable, notification.MikanLinkVariable, notification.DownloadLinkVariable} {
		facts[key] = nil
	}
	if err := json.Unmarshal(evidence, &source); err != nil {
		return
	}
	var site string
	switch source.Provider {
	case "anibt":
		site = "AniBT"
	case "mikan":
		site = "Mikan"
	case "nyaa":
		site = "Nyaa"
	case "jackett":
		if source.SourceSite != nil && source.SourceSite.Validate() == nil {
			site = source.SourceSite.Name
		}
	case "custom":
	default:
		return
	}
	facts["release.src"] = source.Provider
	if site != "" {
		// A tracker name is upstream text, not a route for embedding a URL.
		clean := maskPayloadURLs(mustSourceText(site))
		var text map[string]any
		if json.Unmarshal(clean, &text) == nil {
			facts["release.site"] = text["site"]
		}
	}
	if link := notification.SafeExternalURL(source.DetailsURL, false); link != "" {
		facts["release.link"] = link
		if acquisitiondomain.ProviderDetailsURL(source.Provider, link) != "" {
			switch source.Provider {
			case "anibt":
				facts[notification.AniBTLinkVariable] = link
			case "mikan":
				facts[notification.MikanLinkVariable] = link
			}
		}
	}
	if link := notification.SafeExternalURL(source.DownloadURL, false); link != "" {
		facts[notification.DownloadLinkVariable] = link
	}
}

func mustSourceText(site string) []byte {
	raw, _ := json.Marshal(map[string]string{"site": site})
	return raw
}
