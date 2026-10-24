package application

import (
	"context"
	"errors"
	"strings"
	"strconv"
	"testing"
	"time"

	catalogdomain "bansoko/internal/catalog/domain"
)

// fakeGroupDirectory is the collected layer in its smallest explicit form: each
// query's result set is configured up front, so a test asserts the merge and the
// ordering rather than re-deriving them from the matching rule.
type fakeGroupDirectory struct {
	search map[string][]catalogdomain.ResolvedGroup
	lookup map[string]catalogdomain.ResolvedGroup
}

func (f fakeGroupDirectory) Matches(provider, query string) []catalogdomain.ResolvedGroup {
	return append([]catalogdomain.ResolvedGroup(nil), f.search[provider+"|"+query]...)
}

func (f fakeGroupDirectory) Lookup(provider, groupRef string) (catalogdomain.ResolvedGroup, bool) {
	entry, ok := f.lookup[provider+":"+groupRef]
	return entry, ok
}

// fakeGroupResolver records every classification and resolution, which is how
// the tests prove which retrieval path issued a provider request.
type fakeGroupResolver struct {
	keys     map[string]string
	resolved map[string][]catalogdomain.ResolvedGroup
	queries  []string
}

func (f *fakeGroupResolver) QueryKey(provider, query string) (string, bool) {
	key, ok := f.keys[provider+"|"+query]
	return key, ok
}

func (f *fakeGroupResolver) Resolve(_ context.Context, provider, query string) ([]catalogdomain.ResolvedGroup, error) {
	f.queries = append(f.queries, provider+"|"+query)
	return f.resolved[provider+"|"+query], nil
}

// fakeGroupDictionaryStore is the user layer: an in-memory row set that records
// every write, so a test can assert that a path wrote nothing.
type fakeGroupDictionaryStore struct {
	rows     map[string]catalogdomain.GroupDictionaryEntry
	inserted []string
	deleted  []string
}

func newFakeGroupDictionaryStore(entries ...catalogdomain.GroupDictionaryEntry) *fakeGroupDictionaryStore {
	store := &fakeGroupDictionaryStore{rows: map[string]catalogdomain.GroupDictionaryEntry{}}
	for _, entry := range entries {
		store.rows[entry.Key()] = entry
	}
	return store
}

func userEntry(provider, groupRef, name string, available bool) catalogdomain.GroupDictionaryEntry {
	return catalogdomain.GroupDictionaryEntry{Provider: provider, GroupRef: groupRef, DisplayName: name, Available: available, Source: catalogdomain.GroupDictionarySourceUser, AddedAt: time.Now().UTC()}
}

func (f *fakeGroupDictionaryStore) ListGroupDictionaryEntries(_ context.Context, provider string) ([]catalogdomain.GroupDictionaryEntry, error) {
	entries := make([]catalogdomain.GroupDictionaryEntry, 0, len(f.rows))
	for _, entry := range f.rows {
		if entry.Provider == provider {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func (f *fakeGroupDictionaryStore) FindGroupDictionaryEntry(_ context.Context, provider, groupRef string) (catalogdomain.GroupDictionaryEntry, bool, error) {
	entry, ok := f.rows[provider+":"+groupRef]
	return entry, ok, nil
}

func (f *fakeGroupDictionaryStore) InsertGroupDictionaryEntry(_ context.Context, provider, groupRef, displayName string, available bool) (catalogdomain.GroupDictionaryEntry, error) {
	entry := userEntry(provider, groupRef, displayName, available)
	f.rows[entry.Key()] = entry
	f.inserted = append(f.inserted, entry.Key())
	return entry, nil
}

func (f *fakeGroupDictionaryStore) DeleteGroupDictionaryEntry(_ context.Context, provider, groupRef string) (bool, error) {
	key := provider + ":" + groupRef
	if _, ok := f.rows[key]; !ok {
		return false, nil
	}
	delete(f.rows, key)
	f.deleted = append(f.deleted, key)
	return true, nil
}

func dictionaryKeys(entries []catalogdomain.GroupDictionaryEntry) string {
	keys := make([]string, 0, len(entries))
	for _, entry := range entries {
		keys = append(keys, entry.Key())
	}
	return strings.Join(keys, ",")
}

// dictionaryService wires a service with both dictionary layers and the
// networked half. A test that omits a layer leaves it as the caller passed it.
func dictionaryService(directory GroupDirectory, resolver GroupResolver, store GroupDictionaryStore) *Service {
	service := &Service{now: func() time.Time { return time.Now().UTC() }}
	service.SetGroupDirectory(directory)
	service.SetGroupResolver(resolver)
	service.SetGroupDictionaryStore(store)
	return service
}

func TestListGroupDictionaryMergesBothLayersAndLeadsWithLiveEntries(t *testing.T) {
	directory := fakeGroupDirectory{search: map[string][]catalogdomain.ResolvedGroup{
		"mikan|loli": {
			{Provider: "mikan", GroupRef: "58", DisplayName: "SampleGroup", Available: true},
			{Provider: "mikan", GroupRef: "612", DisplayName: "Loli Retirement", Available: false},
		},
	}}
	store := newFakeGroupDictionaryStore(userEntry("mikan", "61", "Loli Raws", true), userEntry("mikan", "900", "Loli Archive", false))
	service := dictionaryService(directory, nil, store)

	entries, err := listGroupDictionary(t, service, GroupDictionaryListing{Provider: "mikan", Query: "loli"})
	if err != nil {
		t.Fatal(err)
	}
	// Available first, then the user's own additions, then ascending key: the
	// merged list never lets a retired id take a slot ahead of a live group.
	if got := dictionaryKeys(entries); got != "mikan:61,mikan:58,mikan:900,mikan:612" {
		t.Fatalf("merged dictionary order = %s", got)
	}
	for _, entry := range entries {
		if entry.Source == "" {
			t.Fatalf("merged entry lost its layer: %+v", entry)
		}
	}
}

func TestListGroupDictionaryFiltersByLayer(t *testing.T) {
	directory := fakeGroupDirectory{search: map[string][]catalogdomain.ResolvedGroup{
		"anibt|": {{Provider: "anibt", GroupRef: "samplegroup", DisplayName: "SampleGroup", Available: true}},
	}}
	store := newFakeGroupDictionaryStore(userEntry("anibt", "samplegroup", "示例字幕组", true))
	service := dictionaryService(directory, nil, store)

	collected, err := listGroupDictionary(t, service, GroupDictionaryListing{Provider: "anibt", Source: "collected"})
	if err != nil {
		t.Fatal(err)
	}
	if dictionaryKeys(collected) != "anibt:samplegroup" {
		t.Fatalf("collected layer = %s", dictionaryKeys(collected))
	}
	stored, err := listGroupDictionary(t, service, GroupDictionaryListing{Provider: "anibt", Source: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if dictionaryKeys(stored) != "anibt:samplegroup" {
		t.Fatalf("user layer = %s", dictionaryKeys(stored))
	}
}

// mikanSnapshot builds a collected layer of live ids 1..live and retired ids
// after them, the shape of the real Mikan snapshot (few live, many retired).
func mikanSnapshot(live, retired int) []catalogdomain.ResolvedGroup {
	groups := make([]catalogdomain.ResolvedGroup, 0, live+retired)
	for index := 1; index <= live+retired; index++ {
		groups = append(groups, catalogdomain.ResolvedGroup{
			Provider: "mikan", GroupRef: strconv.Itoa(index), DisplayName: "Group " + strconv.Itoa(index), Available: index <= live,
		})
	}
	return groups
}

// An unfiltered, unsourced listing is legal now that every response is a page:
// it walks both layers in the dictionary order, and next_offset continues it
// until the last page reports none.
func TestListGroupDictionaryPagesTheWholeDictionary(t *testing.T) {
	directory := fakeGroupDirectory{search: map[string][]catalogdomain.ResolvedGroup{"mikan|": mikanSnapshot(120, 30)}}
	store := newFakeGroupDictionaryStore(userEntry("mikan", "9001", "Mine", true))
	service := dictionaryService(directory, nil, store)

	first, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 151 || len(first.Entries) != MaxGroupDictionaryCandidates || first.NextOffset == nil || *first.NextOffset != 50 {
		t.Fatalf("first page total=%d len=%d next=%v", first.Total, len(first.Entries), first.NextOffset)
	}
	// The user's own live addition leads, then collected keys ascend.
	if first.Entries[0].GroupRef != "9001" || first.Entries[1].GroupRef != "1" {
		t.Fatalf("first page order = %s", dictionaryKeys(first.Entries[:3]))
	}

	seen := len(first.Entries)
	offset := *first.NextOffset
	var last GroupDictionaryPage
	for pages := 0; pages < 10; pages++ {
		page, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan", Offset: offset, Limit: 40})
		if err != nil {
			t.Fatal(err)
		}
		seen += len(page.Entries)
		last = page
		if page.NextOffset == nil {
			break
		}
		offset = *page.NextOffset
	}
	if seen != 151 || last.NextOffset != nil {
		t.Fatalf("paging saw %d entries, last next=%v", seen, last.NextOffset)
	}
	if tail := last.Entries[len(last.Entries)-1]; tail.GroupRef != "150" || tail.Available {
		t.Fatalf("retired ids must close the listing, got %+v", tail)
	}

	beyond, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan", Offset: 500})
	if err != nil {
		t.Fatal(err)
	}
	if len(beyond.Entries) != 0 || beyond.NextOffset != nil || beyond.Total != 151 {
		t.Fatalf("offset past the end = %+v", beyond)
	}
}

// A limit above the page maximum is clamped, never refused; zero selects the
// default page size.
func TestListGroupDictionaryClampsTheLimit(t *testing.T) {
	directory := fakeGroupDirectory{search: map[string][]catalogdomain.ResolvedGroup{"mikan|": mikanSnapshot(700, 0)}}
	service := dictionaryService(directory, nil, newFakeGroupDictionaryStore())

	page, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan", Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != MaxGroupDictionaryPageSize || page.NextOffset == nil || *page.NextOffset != MaxGroupDictionaryPageSize {
		t.Fatalf("clamped page len=%d next=%v", len(page.Entries), page.NextOffset)
	}
}

// hide_retired drops collected retired ids, so one page holds every live
// entry; the user's own additions stay even when retired.
func TestListGroupDictionaryHidesRetiredCollectedEntries(t *testing.T) {
	directory := fakeGroupDirectory{search: map[string][]catalogdomain.ResolvedGroup{"mikan|": mikanSnapshot(366, 1033)}}
	store := newFakeGroupDictionaryStore(userEntry("mikan", "9002", "Old Mine", false))
	service := dictionaryService(directory, nil, store)

	page, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan", HideRetired: true, Limit: MaxGroupDictionaryPageSize})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 367 || len(page.Entries) != 367 || page.NextOffset != nil {
		t.Fatalf("live listing total=%d len=%d next=%v", page.Total, len(page.Entries), page.NextOffset)
	}
	if tail := page.Entries[len(page.Entries)-1]; tail.GroupRef != "9002" || tail.Source != catalogdomain.GroupDictionarySourceUser {
		t.Fatalf("the user's retired addition must stay listed, got %+v", tail)
	}
}

// A filtered listing pages the same way, over the matches only.
func TestListGroupDictionaryPagesAFilteredListing(t *testing.T) {
	directory := fakeGroupDirectory{search: map[string][]catalogdomain.ResolvedGroup{"mikan|group": mikanSnapshot(60, 5)}}
	store := newFakeGroupDictionaryStore(userEntry("mikan", "9003", "My Group", true), userEntry("mikan", "9004", "Unrelated", true))
	service := dictionaryService(directory, nil, store)

	first, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan", Query: "group", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if first.Total != 66 || first.NextOffset == nil || *first.NextOffset != 50 {
		t.Fatalf("filtered first page total=%d next=%v", first.Total, first.NextOffset)
	}
	second, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan", Query: "group", Offset: 50, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 16 || second.NextOffset != nil {
		t.Fatalf("filtered second page len=%d next=%v", len(second.Entries), second.NextOffset)
	}
	for _, entry := range append(first.Entries, second.Entries...) {
		if entry.GroupRef == "9004" {
			t.Fatal("a non-matching user entry leaked into the filtered listing")
		}
	}
}

func TestListGroupDictionaryRejectsInvalidInputWithoutTouchingALayer(t *testing.T) {
	directory := fakeGroupDirectory{search: map[string][]catalogdomain.ResolvedGroup{}}
	store := newFakeGroupDictionaryStore()
	service := dictionaryService(directory, nil, store)

	for name, call := range map[string]func() error{
		"unknown provider": func() error {
			_, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "bgm", Query: "loli"})
			return err
		},
		"unknown source": func() error {
			_, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan", Query: "loli", Source: "live"})
			return err
		},
		"over-long query": func() error {
			_, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan", Query: strings.Repeat("x", maxGroupDictionaryQueryLength+1)})
			return err
		},
		"negative offset": func() error {
			_, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan", Offset: -1})
			return err
		},
		"negative limit": func() error {
			_, err := service.ListGroupDictionary(context.Background(), GroupDictionaryListing{Provider: "mikan", Limit: -1})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrGroupDictionaryRequestInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

// The dictionary surface is local: it answers whether or not a provider
// connection was ever saved, so an install that has configured none still gets
// its candidate list.
func TestListGroupDictionaryNeedsNoProviderConnection(t *testing.T) {
	directory := fakeGroupDirectory{search: map[string][]catalogdomain.ResolvedGroup{
		"mikan|58": {{Provider: "mikan", GroupRef: "58", DisplayName: "SampleGroup", Available: true}},
	}}
	service := dictionaryService(directory, nil, newFakeGroupDictionaryStore())

	entries, err := listGroupDictionary(t, service, GroupDictionaryListing{Provider: "mikan", Query: "58"})
	if err != nil {
		t.Fatal(err)
	}
	if dictionaryKeys(entries) != "mikan:58" {
		t.Fatalf("local listing = %s", dictionaryKeys(entries))
	}
}

func TestRetrieveGroupDictionaryAnswersAKeyFromTheDictionaryAlone(t *testing.T) {
	directory := fakeGroupDirectory{lookup: map[string]catalogdomain.ResolvedGroup{
		"mikan:58": {Provider: "mikan", GroupRef: "58", DisplayName: "SampleGroup", Available: true},
	}}
	resolver := &fakeGroupResolver{keys: map[string]string{"mikan|58": "58"}, resolved: map[string][]catalogdomain.ResolvedGroup{}}
	store := newFakeGroupDictionaryStore()
	service := dictionaryService(directory, resolver, store)

	entries, err := service.RetrieveGroupDictionary(context.Background(), "mikan", "58")
	if err != nil {
		t.Fatal(err)
	}
	if dictionaryKeys(entries) != "mikan:58" || entries[0].Source != catalogdomain.GroupDictionarySourceCollected {
		t.Fatalf("key hit = %+v", entries)
	}
	if len(resolver.queries) != 0 {
		t.Fatalf("a dictionary hit still read the provider: %v", resolver.queries)
	}
	if len(store.inserted) != 0 {
		t.Fatalf("a dictionary hit wrote: %v", store.inserted)
	}
}

func TestRetrieveGroupDictionaryConfirmsAMissingKeyAndStoresIt(t *testing.T) {
	directory := fakeGroupDirectory{lookup: map[string]catalogdomain.ResolvedGroup{}}
	resolver := &fakeGroupResolver{
		keys:     map[string]string{"mikan|700": "700"},
		resolved: map[string][]catalogdomain.ResolvedGroup{"mikan|700": {{Provider: "mikan", GroupRef: "700", DisplayName: "New Group", Available: true}}},
	}
	store := newFakeGroupDictionaryStore()
	service := dictionaryService(directory, resolver, store)

	entries, err := service.RetrieveGroupDictionary(context.Background(), "mikan", "700")
	if err != nil {
		t.Fatal(err)
	}
	if dictionaryKeys(entries) != "mikan:700" || entries[0].Source != catalogdomain.GroupDictionarySourceUser {
		t.Fatalf("retrieved key = %+v", entries)
	}
	if len(resolver.queries) != 1 {
		t.Fatalf("missing key made %d provider reads, want 1", len(resolver.queries))
	}
	if strings.Join(store.inserted, ",") != "mikan:700" {
		t.Fatalf("retrieved key was not stored in the user layer: %v", store.inserted)
	}
}

// Discovery confirms keys only: a name — even one the dictionary holds — is
// refused before any layer, the provider or the store is touched. Browsing by
// name is the local listing's job.
func TestRetrieveGroupDictionaryRefusesANameWithoutAnyRead(t *testing.T) {
	directory := fakeGroupDirectory{search: map[string][]catalogdomain.ResolvedGroup{
		"anibt|示例": {{Provider: "anibt", GroupRef: "samplegroup", DisplayName: "示例字幕组", Available: true}},
	}}
	resolver := &fakeGroupResolver{keys: map[string]string{}, resolved: map[string][]catalogdomain.ResolvedGroup{
		"anibt|New Sub": {{Provider: "anibt", GroupRef: "newsub", DisplayName: "New Sub", Available: true}},
	}}
	store := newFakeGroupDictionaryStore()
	service := dictionaryService(directory, resolver, store)

	for _, query := range []string{"示例", "New Sub"} {
		if _, err := service.RetrieveGroupDictionary(context.Background(), "anibt", query); !errors.Is(err, ErrGroupDictionaryQueryNotKey) {
			t.Fatalf("%q error = %v, want ErrGroupDictionaryQueryNotKey", query, err)
		}
	}
	if _, err := service.RetrieveGroupDictionary(context.Background(), "mikan", "SampleGroup"); !errors.Is(err, ErrGroupDictionaryQueryNotKey) {
		t.Fatalf("Mikan name error = %v", err)
	}
	if len(resolver.queries) != 0 || len(store.inserted) != 0 {
		t.Fatalf("a refused name read %v and wrote %v", resolver.queries, store.inserted)
	}
}

// A pasted group page link is a key: the resolver is asked with the key it
// classified, not the raw link.
func TestRetrieveGroupDictionaryConfirmsTheClassifiedKey(t *testing.T) {
	directory := fakeGroupDirectory{lookup: map[string]catalogdomain.ResolvedGroup{}}
	link := "https://anibt.net/en/group/newsub"
	resolver := &fakeGroupResolver{
		keys:     map[string]string{"anibt|" + link: "newsub"},
		resolved: map[string][]catalogdomain.ResolvedGroup{"anibt|newsub": {{Provider: "anibt", GroupRef: "newsub", DisplayName: "New Sub", Available: true}}},
	}
	store := newFakeGroupDictionaryStore()
	service := dictionaryService(directory, resolver, store)

	entries, err := service.RetrieveGroupDictionary(context.Background(), "anibt", link)
	if err != nil {
		t.Fatal(err)
	}
	if dictionaryKeys(entries) != "anibt:newsub" || strings.Join(resolver.queries, ",") != "anibt|newsub" {
		t.Fatalf("link retrieval = %+v via %v", entries, resolver.queries)
	}
}

// A key the user layer already holds is returned as it stands: a repeat is a
// no-op, never a second row and never an error.
func TestRetrieveGroupDictionaryIsIdempotentForAStoredKey(t *testing.T) {
	directory := fakeGroupDirectory{lookup: map[string]catalogdomain.ResolvedGroup{}}
	resolver := &fakeGroupResolver{keys: map[string]string{"anibt|samplegroup": "samplegroup"}, resolved: map[string][]catalogdomain.ResolvedGroup{}}
	store := newFakeGroupDictionaryStore(userEntry("anibt", "samplegroup", "SampleGroup", true))
	service := dictionaryService(directory, resolver, store)

	entries, err := service.RetrieveGroupDictionary(context.Background(), "anibt", "samplegroup")
	if err != nil {
		t.Fatal(err)
	}
	if dictionaryKeys(entries) != "anibt:samplegroup" || entries[0].Source != catalogdomain.GroupDictionarySourceUser {
		t.Fatalf("stored key hit = %+v", entries)
	}
	if len(store.inserted) != 0 || len(resolver.queries) != 0 {
		t.Fatalf("a stored key hit wrote %v and read %v", store.inserted, resolver.queries)
	}
}

func TestDeleteGroupDictionaryEntryRemovesAUserEntry(t *testing.T) {
	directory := fakeGroupDirectory{lookup: map[string]catalogdomain.ResolvedGroup{}}
	store := newFakeGroupDictionaryStore(userEntry("mikan", "700", "New Group", true))
	service := dictionaryService(directory, nil, store)

	// A zero-padded spelling is the same key, and reaches the same row.
	if err := service.DeleteGroupDictionaryEntry(context.Background(), "mikan", " 0700 "); err != nil {
		t.Fatal(err)
	}
	if strings.Join(store.deleted, ",") != "mikan:700" {
		t.Fatalf("deleted %v", store.deleted)
	}
}

func TestDeleteGroupDictionaryEntryRefusesACollectedEntry(t *testing.T) {
	directory := fakeGroupDirectory{lookup: map[string]catalogdomain.ResolvedGroup{
		"mikan:58": {Provider: "mikan", GroupRef: "58", DisplayName: "SampleGroup", Available: true},
	}}
	service := dictionaryService(directory, nil, newFakeGroupDictionaryStore())

	err := service.DeleteGroupDictionaryEntry(context.Background(), "mikan", "58")
	if !errors.Is(err, ErrGroupDictionaryEntryImmutable) {
		t.Fatalf("collected entry deletion error = %v", err)
	}
}

func TestDeleteGroupDictionaryEntryReportsAMissingEntry(t *testing.T) {
	directory := fakeGroupDirectory{lookup: map[string]catalogdomain.ResolvedGroup{}}
	service := dictionaryService(directory, nil, newFakeGroupDictionaryStore())

	err := service.DeleteGroupDictionaryEntry(context.Background(), "mikan", "58")
	if !errors.Is(err, ErrGroupDictionaryEntryNotFound) {
		t.Fatalf("missing entry deletion error = %v", err)
	}
}

func TestDeleteGroupDictionaryEntryRejectsInvalidInput(t *testing.T) {
	directory := fakeGroupDirectory{lookup: map[string]catalogdomain.ResolvedGroup{}}
	store := newFakeGroupDictionaryStore()
	service := dictionaryService(directory, nil, store)

	for name, call := range map[string]func() error{
		"unknown provider": func() error {
			return service.DeleteGroupDictionaryEntry(context.Background(), "bgm", "58")
		},
		"mikan key that is not an id": func() error {
			return service.DeleteGroupDictionaryEntry(context.Background(), "mikan", "SampleGroup")
		},
		"anibt key that is not a slug": func() error {
			return service.DeleteGroupDictionaryEntry(context.Background(), "anibt", "示例字幕组")
		},
		"empty key": func() error {
			return service.DeleteGroupDictionaryEntry(context.Background(), "mikan", "   ")
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrGroupDictionaryRequestInvalid) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if len(store.deleted) != 0 {
		t.Fatalf("invalid input deleted %v", store.deleted)
	}
}

// listGroupDictionary reads one page with the default limit and returns its
// entries, for tests that assert only the merge and the order.
func listGroupDictionary(t *testing.T, service *Service, listing GroupDictionaryListing) ([]catalogdomain.GroupDictionaryEntry, error) {
	t.Helper()
	page, err := service.ListGroupDictionary(context.Background(), listing)
	return page.Entries, err
}
