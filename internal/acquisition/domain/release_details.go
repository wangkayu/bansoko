package domain

import (
	"net/url"
	"strings"
)

// ProviderDetailsURL accepts a provider's explicit page field. It never turns a
// torrent download location or a proxy URL into a guessed release page. External
// delivery applies its separate credential/private-address policy afterwards.
func ProviderDetailsURL(provider, raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	var prefix string
	switch provider {
	case "anibt":
		if host != "anibt.net" {
			return ""
		}
		prefix = "/release/"
	case "mikan":
		if host != "mikanani.me" && host != "mikanime.tv" {
			return ""
		}
		prefix = "/Home/Episode/"
	case "nyaa":
		if host != "nyaa.si" {
			return ""
		}
		prefix = "/view/"
	default:
		return ""
	}
	if !strings.HasPrefix(u.Path, prefix) || strings.TrimPrefix(u.Path, prefix) == "" {
		return ""
	}
	return raw
}
