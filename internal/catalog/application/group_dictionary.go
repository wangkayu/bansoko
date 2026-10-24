package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"bansoko/internal/catalog/domain"
	providerdomain "bansoko/internal/provider/domain"
)

// MaxGroupDictionaryCandidates bounds one unpaged subgroup dictionary list (a
// retrieval) and is the default page size of the paged listing. It mirrors the
// maxItems of SubgroupGroupCandidateList in api/openapi.yaml.
const MaxGroupDictionaryCandidates = 50

// MaxGroupDictionaryPageSize caps one page of the dictionary listing. It is
// sized so the available entries of the largest collected snapshot (Mikan,
// about 370 live ids) fit in one page; a larger request is clamped, not
// refused. It mirrors the limit maximum in api/openapi.yaml.
const MaxGroupDictionaryPageSize = 500

// GroupDictionaryListing is one listing request of the subgroup dictionary.
type GroupDictionaryListing struct {
	Provider string
	// Query filters by display name, or by Mikan key prefix; empty lists all.
	Query string
	// Source restricts the listing to one layer; empty lists both.
	Source string
	// HideRetired leaves out collected entries the snapshot records as
	// retired. The user's own additions are always listed: the user chose them
	// and must be able to see and remove them.
	HideRetired bool
	// Offset is the number of ordered entries to skip.
	Offset int
	// Limit is the page size; zero selects MaxGroupDictionaryCandidates and a
	// value above MaxGroupDictionaryPageSize is clamped to it.
	Limit int
}

// GroupDictionaryPage is one page of the ordered dictionary listing.
type GroupDictionaryPage struct {
	Entries []domain.GroupDictionaryEntry
	// NextOffset is the offset of the following page, nil on the last page.
	NextOffset *int
	// Total counts every entry the listing matches, across all pages.
	Total int
}

// maxGroupDictionaryQueryLength bounds the free-form query every dictionary
// operation accepts.
const maxGroupDictionaryQueryLength = 256

// GroupDirectory is the collected half of the subgroup dictionary: the
// read-only snapshot published with the release. It performs no I/O and needs
// no configured connection, which is what lets the dictionary answer on an
// install that has saved none. The concrete implementation is
// internal/provider/groupdir, adapted by the composition root.
type GroupDirectory interface {
	// Matches returns every entry of one provider's collected directory that
	// the query matches (an empty query matches all), ordered available-first
	// and then by ascending key. The caller pages or bounds the result.
	Matches(provider, query string) []domain.ResolvedGroup
	// Lookup resolves one exact key, with no prefix or substring matching.
	Lookup(provider, groupRef string) (domain.ResolvedGroup, bool)
}

// GroupResolver is the networked half of dictionary retrieval: it confirms a
// group at the provider and reads the name the provider publishes. It is
// reached only for a query the dictionary itself cannot answer, and it never
// writes. The concrete implementation is
// internal/provider/adapter.ConfiguredGroupLookup, adapted by the composition
// root.
type GroupResolver interface {
	// QueryKey reports the provider group key a query already names — a key or
	// a pipe to one — with no I/O at all, so the dictionary can answer a pasted
	// key or group page link from its own layers.
	QueryKey(provider, query string) (string, bool)
	// Resolve confirms or finds groups at the provider. An empty result is a
	// valid answer, not an error.
	Resolve(ctx context.Context, provider, query string) ([]domain.ResolvedGroup, error)
}

// GroupDictionaryStore is the catalog-owned persistence boundary for the user
// layer of the subgroup dictionary. Only user entries are stored: the collected
// layer always comes from the embedded snapshot and is neither written nor
// deletable. The port never exposes database rows or transaction handles.
type GroupDictionaryStore interface {
	ListGroupDictionaryEntries(ctx context.Context, provider string) ([]domain.GroupDictionaryEntry, error)
	FindGroupDictionaryEntry(ctx context.Context, provider, groupRef string) (domain.GroupDictionaryEntry, bool, error)
	InsertGroupDictionaryEntry(ctx context.Context, provider, groupRef, displayName string, available bool) (domain.GroupDictionaryEntry, error)
	DeleteGroupDictionaryEntry(ctx context.Context, provider, groupRef string) (bool, error)
}

// ErrGroupDictionaryRequestInvalid reports a request the dictionary cannot
// answer as asked (unknown provider or source, a query outside its bound, or a
// query and source that are both empty). The transport maps it to 400.
var ErrGroupDictionaryRequestInvalid = errors.New("subgroup dictionary request is invalid")

// ErrGroupDictionaryQueryNotKey reports a discover request whose query names no
// provider group key (a Mikan id, an AniBT slug, or the group page link of
// either). Discovery confirms keys only; the transport maps it to 400.
var ErrGroupDictionaryQueryNotKey = errors.New("subgroup discovery needs a group key")

// ErrGroupDictionaryEntryImmutable reports an attempt to remove an entry that
// belongs to the collected snapshot. The collected layer is reference data
// published with the release, so it can be neither edited nor deleted; the
// transport maps this to 409.
var ErrGroupDictionaryEntryImmutable = errors.New("subgroup dictionary entry is immutable")

// ErrGroupDictionaryEntryNotFound reports a removal of an entry that exists in
// neither dictionary layer. The transport maps it to 404.
var ErrGroupDictionaryEntryNotFound = errors.New("subgroup dictionary entry was not found")

// SetGroupDirectory wires the collected (published, read-only) layer of the
// subgroup dictionary. It mirrors the composition-root setter pattern of the
// other catalog ports so the catalog Service stays free of concrete adapter
// imports.
func (s *Service) SetGroupDirectory(directory GroupDirectory) {
	if s != nil {
		s.groupDirectory = directory
	}
}

// SetGroupResolver wires the networked half of dictionary retrieval.
func (s *Service) SetGroupResolver(resolver GroupResolver) {
	if s != nil {
		s.groupResolver = resolver
	}
}

// SetGroupDictionaryStore wires the persistence boundary of the dictionary's
// user layer.
func (s *Service) SetGroupDictionaryStore(store GroupDictionaryStore) {
	if s != nil {
		s.groupDictionary = store
	}
}

// ListGroupDictionary lists one page of the subgroup dictionary: the collected
// snapshot union the user layer, filtered by query, by layer and optionally to
// live entries, in the dictionary order (available first, then the user's own
// additions, then ascending key). It is purely local — it contacts no provider
// and needs no configured connection, so an install that has saved none still
// gets an answer.
//
// Every filter is optional: each page is bounded by the limit, so even an
// unfiltered listing of both layers (about 1400 Mikan ids) answers in pages.
func (s *Service) ListGroupDictionary(ctx context.Context, listing GroupDictionaryListing) (GroupDictionaryPage, error) {
	provider, err := dictionaryProvider(listing.Provider)
	if err != nil {
		return GroupDictionaryPage{}, fmt.Errorf("%w: %s", ErrGroupDictionaryRequestInvalid, err)
	}
	layer, err := dictionaryLayer(listing.Source)
	if err != nil {
		return GroupDictionaryPage{}, fmt.Errorf("%w: %s", ErrGroupDictionaryRequestInvalid, err)
	}
	query := strings.TrimSpace(listing.Query)
	if len(query) > maxGroupDictionaryQueryLength {
		return GroupDictionaryPage{}, fmt.Errorf("%w: the subgroup query must be at most %d characters", ErrGroupDictionaryRequestInvalid, maxGroupDictionaryQueryLength)
	}
	if listing.Offset < 0 || listing.Limit < 0 {
		return GroupDictionaryPage{}, fmt.Errorf("%w: offset and limit must not be negative", ErrGroupDictionaryRequestInvalid)
	}
	limit := listing.Limit
	if limit == 0 {
		limit = MaxGroupDictionaryCandidates
	}
	if limit > MaxGroupDictionaryPageSize {
		limit = MaxGroupDictionaryPageSize
	}
	entries, err := s.dictionaryEntries(ctx, provider, query, layer)
	if err != nil {
		return GroupDictionaryPage{}, err
	}
	if listing.HideRetired {
		live := entries[:0]
		for _, entry := range entries {
			if entry.Available || entry.Source == domain.GroupDictionarySourceUser {
				live = append(live, entry)
			}
		}
		entries = live
	}
	sortGroupDictionary(entries)
	page := GroupDictionaryPage{Total: len(entries), Entries: []domain.GroupDictionaryEntry{}}
	if listing.Offset >= len(entries) {
		return page, nil
	}
	end := listing.Offset + limit
	if end < len(entries) {
		next := end
		page.NextOffset = &next
	} else {
		end = len(entries)
	}
	page.Entries = entries[listing.Offset:end]
	return page, nil
}

// RetrieveGroupDictionary is the dictionary's discover-and-add operation, and
// the only dictionary path that may read a provider. It takes a key — a Mikan
// group id or an AniBT slug, or the group page link of either — and never a
// name: browsing and filtering by name is the local listing's job.
//
//   - a key the dictionary already holds returns that entry and issues no
//     provider request and no write.
//   - a key the dictionary does not hold is confirmed at the provider, and the
//     confirmed group joins the user layer of the dictionary. The dictionary
//     therefore grows on demand, one confirmed key at a time, instead of by
//     crawl. A key the provider does not confirm is an empty list.
//   - anything that is not a key is refused (ErrGroupDictionaryQueryNotKey)
//     before any layer or provider is touched.
//
// Every path is idempotent: a key the dictionary already holds is returned as
// it stands, never re-inserted, so repeating a retrieval is a no-op and never
// an error.
func (s *Service) RetrieveGroupDictionary(ctx context.Context, provider, query string) ([]domain.GroupDictionaryEntry, error) {
	provider, err := dictionaryProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrGroupDictionaryRequestInvalid, err)
	}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > maxGroupDictionaryQueryLength {
		return nil, fmt.Errorf("%w: the subgroup query is required and must be at most %d characters", ErrGroupDictionaryRequestInvalid, maxGroupDictionaryQueryLength)
	}
	if s == nil || s.groupResolver == nil {
		return nil, errors.New("subgroup group resolver is not configured")
	}
	key, ok := s.groupResolver.QueryKey(provider, query)
	if !ok {
		return nil, ErrGroupDictionaryQueryNotKey
	}
	entry, found, err := s.findGroupDictionaryEntry(ctx, provider, key)
	if err != nil {
		return nil, err
	}
	if found {
		return []domain.GroupDictionaryEntry{entry}, nil
	}
	resolved, err := s.groupResolver.Resolve(ctx, provider, key)
	if err != nil {
		return nil, err
	}
	return s.storeResolvedGroups(ctx, provider, resolved)
}

// DeleteGroupDictionaryEntry removes one entry from the user layer of the
// dictionary. It answers three states, because the two layers differ in what
// they allow:
//
//   - the user layer held the entry: it is gone (201-tier success).
//   - the user layer did not, but the collected snapshot does: the entry is
//     reference data published with the release and cannot be deleted
//     (ErrGroupDictionaryEntryImmutable).
//   - neither layer holds it: there is nothing to delete
//     (ErrGroupDictionaryEntryNotFound).
func (s *Service) DeleteGroupDictionaryEntry(ctx context.Context, provider, groupRef string) error {
	provider, err := dictionaryProvider(provider)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrGroupDictionaryRequestInvalid, err)
	}
	ref, err := providerdomain.NewGroupRef(providerdomain.Kind(provider), groupRef)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrGroupDictionaryRequestInvalid, err)
	}
	if s == nil || s.groupDictionary == nil {
		return errors.New("subgroup group dictionary store is not configured")
	}
	deleted, err := s.groupDictionary.DeleteGroupDictionaryEntry(ctx, provider, ref.ExternalID)
	if err != nil {
		return err
	}
	if deleted {
		return nil
	}
	if s.groupDirectory != nil {
		if _, ok := s.groupDirectory.Lookup(provider, ref.ExternalID); ok {
			return ErrGroupDictionaryEntryImmutable
		}
	}
	return ErrGroupDictionaryEntryNotFound
}

// dictionaryEntries collects one provider's dictionary layers, filtered by
// query and by layer, with no ordering and no bound: the caller orders and
// pages or bounds the merged result. Where both layers hold one key the collected entry
// wins, so a stored row can never shadow the release's own snapshot.
func (s *Service) dictionaryEntries(ctx context.Context, provider, query string, layer domain.GroupDictionarySource) ([]domain.GroupDictionaryEntry, error) {
	if s == nil {
		return nil, errors.New("catalog service is nil")
	}
	entries := make([]domain.GroupDictionaryEntry, 0, MaxGroupDictionaryCandidates)
	seen := make(map[string]struct{}, MaxGroupDictionaryCandidates)
	if layer != domain.GroupDictionarySourceUser {
		if s.groupDirectory == nil {
			return nil, errors.New("subgroup group directory is not configured")
		}
		for _, group := range s.groupDirectory.Matches(provider, query) {
			entry := domain.GroupDictionaryEntry{Provider: provider, GroupRef: group.GroupRef, DisplayName: group.DisplayName, Available: group.Available, Source: domain.GroupDictionarySourceCollected}
			if _, exists := seen[entry.Key()]; exists {
				continue
			}
			seen[entry.Key()] = struct{}{}
			entries = append(entries, entry)
		}
	}
	if layer != domain.GroupDictionarySourceCollected {
		if s.groupDictionary == nil {
			return nil, errors.New("subgroup group dictionary store is not configured")
		}
		stored, err := s.groupDictionary.ListGroupDictionaryEntries(ctx, provider)
		if err != nil {
			return nil, err
		}
		kind := providerdomain.Kind(provider)
		for _, entry := range stored {
			if !providerdomain.GroupQueryMatches(kind, entry.GroupRef, entry.DisplayName, query) {
				continue
			}
			if _, exists := seen[entry.Key()]; exists {
				continue
			}
			seen[entry.Key()] = struct{}{}
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

// findGroupDictionaryEntry resolves one exact key against both dictionary
// layers, preferring the collected layer for the same reason dictionaryEntries
// does.
func (s *Service) findGroupDictionaryEntry(ctx context.Context, provider, groupRef string) (domain.GroupDictionaryEntry, bool, error) {
	if s == nil {
		return domain.GroupDictionaryEntry{}, false, errors.New("catalog service is nil")
	}
	groupRef = strings.TrimSpace(groupRef)
	if groupRef == "" {
		return domain.GroupDictionaryEntry{}, false, nil
	}
	if s.groupDirectory != nil {
		if group, ok := s.groupDirectory.Lookup(provider, groupRef); ok {
			return domain.GroupDictionaryEntry{Provider: provider, GroupRef: group.GroupRef, DisplayName: group.DisplayName, Available: group.Available, Source: domain.GroupDictionarySourceCollected}, true, nil
		}
	}
	if s.groupDictionary == nil {
		return domain.GroupDictionaryEntry{}, false, errors.New("subgroup group dictionary store is not configured")
	}
	return s.groupDictionary.FindGroupDictionaryEntry(ctx, provider, groupRef)
}

// storeResolvedGroups adds freshly resolved groups to the user layer and
// returns their entries. It is idempotent: a key either layer already holds is
// returned as it stands and inserted nowhere, so a repeated retrieval neither
// errors nor churns a row.
func (s *Service) storeResolvedGroups(ctx context.Context, provider string, groups []domain.ResolvedGroup) ([]domain.GroupDictionaryEntry, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	if s == nil || s.groupDictionary == nil {
		return nil, errors.New("subgroup group dictionary store is not configured")
	}
	entries := make([]domain.GroupDictionaryEntry, 0, len(groups))
	for _, group := range groups {
		groupRef := strings.TrimSpace(group.GroupRef)
		if groupRef == "" {
			continue
		}
		entry, found, err := s.findGroupDictionaryEntry(ctx, provider, groupRef)
		if err != nil {
			return nil, err
		}
		if !found {
			if entry, err = s.groupDictionary.InsertGroupDictionaryEntry(ctx, provider, groupRef, group.DisplayName, group.Available); err != nil {
				return nil, err
			}
		}
		entries = append(entries, entry)
	}
	return orderGroupDictionary(entries), nil
}

// orderGroupDictionary orders a merged dictionary list and bounds it. Available
// entries come first: an entry whose provider still serves it is the better
// candidate, and a retired Mikan id must never take a slot ahead of a live
// group. At equal availability the user's own additions come first, because
// they are the entries the user explicitly retrieved, and then keys ascend so
// the same query always renders the same list.
func orderGroupDictionary(entries []domain.GroupDictionaryEntry) []domain.GroupDictionaryEntry {
	sortGroupDictionary(entries)
	if len(entries) > MaxGroupDictionaryCandidates {
		entries = entries[:MaxGroupDictionaryCandidates]
	}
	return entries
}

// sortGroupDictionary applies the dictionary order in place, with no bound.
func sortGroupDictionary(entries []domain.GroupDictionaryEntry) {
	sort.SliceStable(entries, func(left, right int) bool {
		if entries[left].Available != entries[right].Available {
			return entries[left].Available
		}
		if entries[left].Source != entries[right].Source {
			return entries[left].Source == domain.GroupDictionarySourceUser
		}
		return providerdomain.GroupKeyLess(entries[left].GroupRef, entries[right].GroupRef)
	})
}

// dictionaryProvider normalizes and validates the provider of a dictionary
// request. Only the two providers with a group directory are dictionary
// sources, so an unknown one is caller input, not an empty layer.
func dictionaryProvider(provider string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if err := validateCatalogProvider(provider); err != nil {
		return "", err
	}
	return provider, nil
}

// dictionaryLayer parses the optional layer filter. An absent filter selects
// both layers; anything else is caller input.
func dictionaryLayer(source string) (domain.GroupDictionarySource, error) {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "":
		return "", nil
	case string(domain.GroupDictionarySourceCollected):
		return domain.GroupDictionarySourceCollected, nil
	case string(domain.GroupDictionarySourceUser):
		return domain.GroupDictionarySourceUser, nil
	default:
		return "", fmt.Errorf("unsupported subgroup dictionary source %q", source)
	}
}
