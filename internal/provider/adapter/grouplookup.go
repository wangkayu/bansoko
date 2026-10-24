package adapter

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"bansoko/internal/apperrors"
	providerdomain "bansoko/internal/provider/domain"
)

// maxGroupLookupQueryLength bounds the free-form query of one resolution.
const maxGroupLookupQueryLength = 256

var (
	// mikanGroupPagePath matches the path of a Mikan group page.
	mikanGroupPagePath = regexp.MustCompile(`^/Home/PublishGroup/([0-9]+)/?$`)
	// aniBTGroupPagePath matches the path of an AniBT group page. The locale
	// segment is optional so both `/en/group/<slug>` (what anibt.net links to)
	// and a locale-less `/group/<slug>` resolve.
	aniBTGroupPagePath = regexp.MustCompile(`^/(?:[a-z]{2}/)?group/([A-Za-z0-9][A-Za-z0-9._-]{0,127})/?$`)
)

// ConfiguredGroupLookup is the networked half of subgroup dictionary
// retrieval: it confirms that a group exists at the provider and reads the name
// the provider publishes for it. It resolves the current saved connection on
// every call, exactly like ConfiguredGroupReader, so a successful connection
// Save or a new network route takes effect without a restart.
//
// It never reads the collected snapshot: the local dictionary layers are the
// catalog's business, and this adapter is reached only for a query the
// dictionary could not answer. It also never writes anything; the caller
// decides whether a resolved group enters the user layer of the dictionary.
//
// Only a key is resolvable — a Mikan numeric id, an AniBT slug, or the group
// page URL of either; a name resolves to nothing and costs no request. Cost per
// provider: Mikan confirms an id with one read of that group's page, and AniBT
// confirms a slug with one read of its single group directory page.
type ConfiguredGroupLookup struct {
	Connections    ConnectionSource
	ClientResolver ConnectionHTTPClientResolver
}

// QueryKey reports the provider group key a query already names: a normalized
// key, or the key inside a group page URL. It performs no I/O and no provider
// validation beyond the provider allow-list, so a caller can classify input
// before deciding whether any request is warranted.
func (l ConfiguredGroupLookup) QueryKey(provider, query string) (string, bool) {
	kind, err := lookupProviderKind(provider)
	if err != nil {
		return "", false
	}
	if kind == providerdomain.ProviderMikan {
		return mikanGroupKeyFromQuery(query)
	}
	return aniBTGroupKeyFromQuery(query)
}

// Resolve confirms or finds groups at the provider for one query. An empty
// result is a valid answer, not an error; only an unknown provider, an
// over-long query, or a provider read failure is an error.
func (l ConfiguredGroupLookup) Resolve(ctx context.Context, provider, query string) ([]providerdomain.GroupEntry, error) {
	kind, err := lookupProviderKind(provider)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(query)
	if len(trimmed) > maxGroupLookupQueryLength {
		return nil, &apperrors.BadRequest{
			Code:    "SUBGROUP_QUERY_INVALID",
			Message: fmt.Sprintf("The subgroup query must be at most %d characters.", maxGroupLookupQueryLength),
		}
	}
	if trimmed == "" {
		return nil, nil
	}
	// Only a key is resolvable, and a query that names none is answered here,
	// before any connection is resolved: discovery confirms keys, never names,
	// so the honest answer needs neither a configured connection nor a request.
	key, ok := l.QueryKey(string(kind), trimmed)
	if !ok {
		return nil, nil
	}
	selected, err := resolveProviderConnection(ctx, l.Connections, kind)
	if err != nil {
		return nil, lookupUnavailable(err)
	}
	client, err := resolveConnectionClient(ctx, l.ClientResolver, selected)
	if err != nil {
		return nil, lookupUnavailable(err)
	}
	if kind == providerdomain.ProviderMikan {
		return resolveMikanGroup(ctx, selected.Endpoint, client, key)
	}
	return resolveAniBTGroup(ctx, selected.Endpoint, client, key)
}

func lookupProviderKind(provider string) (providerdomain.Kind, error) {
	kind := providerdomain.Kind(strings.ToLower(strings.TrimSpace(provider)))
	switch kind {
	case providerdomain.ProviderAniBT, providerdomain.ProviderMikan:
		return kind, nil
	default:
		return "", &apperrors.BadRequest{Code: "PROVIDER_KIND_INVALID", Message: "Provider must be anibt or mikan."}
	}
}

// lookupUnavailable maps a connection or transport failure onto the stable
// service-unavailable shape. The reason is bounded and carries no credentials.
func lookupUnavailable(err error) error {
	reason := err.Error()
	if len(reason) > 512 {
		reason = reason[:512]
	}
	return &apperrors.Business{Status: http.StatusServiceUnavailable, Code: "PROVIDER_GROUP_LOOKUP_FAILED", Message: reason}
}

// resolveMikanGroup confirms one already-classified Mikan key with a single
// live read of that group's page. A key Mikan no longer serves resolves to
// nothing: absence at the provider is an answer, not an error.
func resolveMikanGroup(ctx context.Context, endpoint string, client *http.Client, key string) ([]providerdomain.GroupEntry, error) {
	entry, found, err := (MikanGroupReader{Endpoint: endpoint, Client: client}).ReadGroupByID(ctx, key)
	if err != nil {
		return nil, lookupUnavailable(err)
	}
	if !found {
		return nil, nil
	}
	return []providerdomain.GroupEntry{entry}, nil
}

// resolveAniBTGroup reads the single AniBT group directory page and confirms
// one slug against it: the exact slug resolves to its entry, and a slug the
// directory does not list is an empty answer. One request.
func resolveAniBTGroup(ctx context.Context, endpoint string, client *http.Client, slug string) ([]providerdomain.GroupEntry, error) {
	catalog, err := (AniBTGroupReader{Endpoint: endpoint, Client: client}).ReadGroupCatalog(ctx)
	if err != nil {
		return nil, lookupUnavailable(err)
	}
	for _, entry := range catalog {
		if strings.EqualFold(entry.Ref.ExternalID, slug) {
			return []providerdomain.GroupEntry{entry}, nil
		}
	}
	return nil, nil
}

// mikanGroupKeyFromQuery reports the normalized Mikan subgroup id of a query
// that already names a group: a positive integer, or the path of a group page
// URL. The host of a URL is not checked, because a saved connection may point
// at a mirror; the path shape is what identifies the page.
func mikanGroupKeyFromQuery(query string) (string, bool) {
	if ref, err := providerdomain.NewGroupRef(providerdomain.ProviderMikan, query); err == nil {
		return ref.ExternalID, true
	}
	parsed, ok := lookupURL(query)
	if !ok {
		return "", false
	}
	match := mikanGroupPagePath.FindStringSubmatch(parsed.Path)
	if match == nil {
		return "", false
	}
	ref, err := providerdomain.NewGroupRef(providerdomain.ProviderMikan, match[1])
	if err != nil {
		return "", false
	}
	return ref.ExternalID, true
}

// aniBTGroupKeyFromQuery reports the group slug of a query that already names a
// group: a legal AniBT slug, or the path of a group page URL.
func aniBTGroupKeyFromQuery(query string) (string, bool) {
	if ref, err := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, query); err == nil {
		return ref.ExternalID, true
	}
	parsed, ok := lookupURL(query)
	if !ok {
		return "", false
	}
	match := aniBTGroupPagePath.FindStringSubmatch(parsed.Path)
	if match == nil {
		return "", false
	}
	ref, err := providerdomain.NewGroupRef(providerdomain.ProviderAniBT, match[1])
	if err != nil {
		return "", false
	}
	return ref.ExternalID, true
}

// lookupURL parses an absolute HTTP URL, tolerating a pasted link without its
// scheme. Anything that does not carry a dotted host is not a link.
func lookupURL(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, false
	}
	if parsed.Host == "" {
		parsed, err = url.Parse("https://" + raw)
		if err != nil {
			return nil, false
		}
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || !strings.Contains(parsed.Hostname(), ".") {
		return nil, false
	}
	return parsed, true
}
