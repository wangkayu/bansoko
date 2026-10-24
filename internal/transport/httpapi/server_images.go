package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"

	identitymetadataadapter "bansoko/internal/identitymetadata/adapter"
	"bansoko/internal/transport/httpapi/generated"
)

// ImagePosterService resolves and streams poster bytes for a Bangumi subject.
// It is implemented by internal/identitymetadata/adapter.
type ImagePosterService interface {
	FetchPoster(ctx context.Context, bgmID string) (io.ReadCloser, string, error)
}

// ImageCoverService registers provider image URLs and streams the proxied
// bytes. It is implemented by internal/identitymetadata/adapter. CoverPaths
// returns the same-origin path for each URL the provider policy allows; a URL
// it omits must not be emitted in any form.
type ImageCoverService interface {
	CoverPaths(ctx context.Context, provider string, urls []string) map[string]string
	FetchCover(ctx context.Context, key string) (io.ReadCloser, string, error)
}

// ReadImagePoster streams the proxied poster for one Bangumi subject id. The
// browser never talks to the image host directly: bytes flow through the
// configured BGM connection's network route, and the immutable cache hint lets
// the browser cache them locally.
func (s *Server) ReadImagePoster(w http.ResponseWriter, r *http.Request, bgmId string) {
	posters := s.deps.ImagePosters
	body, contentType, err := posters.FetchPoster(r.Context(), bgmId)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// Client disconnected or stopped waiting; there is nobody to answer
			// and nothing to diagnose, so stay quiet instead of logging a 500.
			return
		}
		if errors.Is(err, identitymetadataadapter.ErrPosterNotFound) {
			writeError(w, r, notFound("POSTER_NOT_FOUND", "The subject has no poster."))
			return
		}
		writeError(w, r, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

// ReadImageCover streams a provider cover or candidate poster registered under
// an opaque key. The request carries no URL: the upstream address comes from
// the server's registry, so this route cannot be steered to another host.
func (s *Server) ReadImageCover(w http.ResponseWriter, r *http.Request, imageKey string) {
	// The generated wrapper does not enforce the path pattern.
	if !imageCoverKeyPattern.MatchString(imageKey) {
		writeError(w, r, notFound("COVER_NOT_FOUND", "The image is not available."))
		return
	}
	body, contentType, err := s.deps.ImageCovers.FetchCover(r.Context(), imageKey)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return
		case errors.Is(err, identitymetadataadapter.ErrCoverNotFound):
			writeError(w, r, notFound("COVER_NOT_FOUND", "The image is not available."))
		case errors.Is(err, identitymetadataadapter.ErrCoverUnavailable):
			writeError(w, r, &APIError{Status: http.StatusBadGateway, Code: "COVER_UPSTREAM_FAILED", Message: "The provider did not return a usable image."})
		default:
			writeError(w, r, err)
		}
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", contentType)
	// The key is content-addressed by source URL, so the bytes behind a key do
	// not change within the TTL. private: the route is authenticated.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, body)
}

var imageCoverKeyPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var bgmSubjectIDPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// coverTarget is one image field of a response about to be written, with the
// provider whose hosts its URL must belong to.
type coverTarget struct {
	provider string
	field    **string
}

// proxyCovers replaces each target's provider URL with its same-origin proxy
// path. A URL the service rejects (or any URL when no service is wired) is
// dropped, so a response never carries a third-party image address.
func (s *Server) proxyCovers(ctx context.Context, targets []coverTarget) {
	byProvider := map[string][]string{}
	for _, target := range targets {
		if *target.field != nil {
			byProvider[target.provider] = append(byProvider[target.provider], **target.field)
		}
	}
	paths := map[string]map[string]string{}
	for provider, urls := range byProvider {
		paths[provider] = map[string]string{}
		if s.deps.ImageCovers != nil {
			paths[provider] = s.deps.ImageCovers.CoverPaths(ctx, provider, urls)
		}
	}
	for _, target := range targets {
		if *target.field == nil {
			continue
		}
		if path, ok := paths[target.provider][**target.field]; ok && path != "" {
			value := path
			*target.field = &value
		} else {
			*target.field = nil
		}
	}
}

func (s *Server) proxyCatalogCovers(ctx context.Context, items []generated.CatalogItem) {
	targets := make([]coverTarget, 0, len(items))
	for index := range items {
		targets = append(targets, coverTarget{provider: string(items[index].Provider), field: &items[index].CoverUrl})
	}
	s.proxyCovers(ctx, targets)
}

func (s *Server) proxyAnimeListCovers(ctx context.Context, items []generated.AnimeListItem) {
	targets := make([]coverTarget, 0, len(items))
	for index := range items {
		targets = append(targets, coverTarget{provider: string(items[index].Subscription.Origin), field: &items[index].CoverUrl})
	}
	s.proxyCovers(ctx, targets)
}

// proxyCandidatePosters points metadata-candidate posters at the same-origin
// proxy: Bangumi posters reuse the subject poster proxy, TMDB posters go
// through the registered cover proxy.
func (s *Server) proxyCandidatePosters(ctx context.Context, candidates []generated.MetadataSearchCandidate) {
	targets := make([]coverTarget, 0, len(candidates))
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.PosterUrl == nil {
			continue
		}
		if candidate.Provider == generated.MetadataSearchCandidateProviderBgm {
			candidate.PosterUrl = nil
			if bgmSubjectIDPattern.MatchString(candidate.ProviderWorkId) {
				path := "/api/v1/images/poster/" + candidate.ProviderWorkId
				candidate.PosterUrl = &path
			}
			continue
		}
		targets = append(targets, coverTarget{provider: identitymetadataadapter.CoverProviderTMDB, field: &candidate.PosterUrl})
	}
	s.proxyCovers(ctx, targets)
}
