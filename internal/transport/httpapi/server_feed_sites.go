package httpapi

import (
	"net/http"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/transport/httpapi/generated"
)

// ListFeedSites serves the declared feed-site support list. The list is code
// (acquisition/domain.FeedSites), so this read needs no service dependency.
func (s *Server) ListFeedSites(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r.Context(), http.StatusOK, feedSiteList(acquisitiondomain.FeedSites()))
}

func feedSiteList(sites []acquisitiondomain.FeedSite) generated.FeedSiteList {
	result := generated.FeedSiteList{Sites: make([]generated.FeedSite, 0, len(sites))}
	for _, site := range sites {
		result.Sites = append(result.Sites, generated.FeedSite{
			Id:       generated.FeedSiteId(site.ID),
			Name:     site.Name,
			Hosts:    append(make([]string, 0, len(site.Hosts)), site.Hosts...),
			Identity: generated.FeedSiteIdentity(site.Identity),
			Match:    generated.FeedSiteMatch(site.Match),
		})
	}
	return result
}
