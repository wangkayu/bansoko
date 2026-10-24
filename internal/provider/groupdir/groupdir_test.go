package groupdir

import (
	"strconv"
	"strings"
	"testing"
)

func TestLoadCarriesBothSnapshots(t *testing.T) {
	snapshots, err := Load()
	if err != nil {
		t.Fatalf("load embedded directories: %v", err)
	}
	if len(snapshots) != 2 {
		t.Fatalf("expected exactly the two provider directories, got %d", len(snapshots))
	}
	for provider, snapshot := range snapshots {
		if snapshot.Provider != provider || snapshot.SchemaVersion == "" || snapshot.SnapshotDate == "" {
			t.Fatalf("snapshot lost its identity: %+v", snapshot)
		}
		if SnapshotDate(provider) != snapshot.SnapshotDate {
			t.Fatalf("SnapshotDate(%q)=%q, snapshot carries %q", provider, SnapshotDate(provider), snapshot.SnapshotDate)
		}
		if len(snapshot.Entries) == 0 {
			t.Fatalf("%s directory is empty", provider)
		}
	}
	if _, ok := For("anibt"); !ok {
		t.Fatal("anibt directory is unavailable")
	}
	if _, ok := For("mikan"); !ok {
		t.Fatal("mikan directory is unavailable")
	}
	if _, ok := For("tmdb"); ok {
		t.Fatal("an unknown provider resolved a directory")
	}
}

func TestMikanSnapshotSeparatesLiveFromRetiredIDs(t *testing.T) {
	snapshot, ok := For(ProviderMikan)
	if !ok {
		t.Fatal("mikan directory is unavailable")
	}
	alive, retired := 0, 0
	var previous uint64
	for _, entry := range snapshot.Entries {
		id, err := strconv.ParseUint(entry.Key, 10, 64)
		if err != nil || id == 0 {
			t.Fatalf("mikan key %q is not a positive id", entry.Key)
		}
		if id <= previous {
			t.Fatalf("mikan ids are not strictly ascending at %q", entry.Key)
		}
		previous = id
		if entry.Alive {
			alive++
			if entry.Name == "" {
				t.Fatalf("live mikan id %s has no name", entry.Key)
			}
			continue
		}
		retired++
		if entry.Name != "" {
			t.Fatalf("retired mikan id %s published a name %q", entry.Key, entry.Name)
		}
	}
	if alive == 0 || retired == 0 {
		t.Fatalf("snapshot must carry both live and retired ids: alive=%d retired=%d", alive, retired)
	}
}

func TestAniBTSnapshotIsAvailableAndSlugOrdered(t *testing.T) {
	snapshot, ok := For(ProviderAniBT)
	if !ok {
		t.Fatal("anibt directory is unavailable")
	}
	var previous string
	for _, entry := range snapshot.Entries {
		if entry.Key == "" || entry.Name == "" {
			t.Fatalf("anibt entry lost its slug or name: %+v", entry)
		}
		if !entry.Alive {
			t.Fatalf("anibt directory publishes no liveness bit, so %q must read available", entry.Key)
		}
		if previous != "" && entry.Key < previous {
			t.Fatalf("anibt slugs are not ascending at %q", entry.Key)
		}
		previous = entry.Key
	}
}

func TestSearchMatchesMikanNamesCaseInsensitively(t *testing.T) {
	target := firstLiveASCII(t, ProviderMikan)
	for _, needle := range []string{strings.ToUpper(target.Name), strings.ToLower(target.Name), target.Name} {
		results := Search(ProviderMikan, needle, MaxResults)
		found := false
		for _, result := range results {
			if result.Key == target.Key {
				found = true
			}
			if !strings.Contains(strings.ToLower(result.Name), strings.ToLower(needle)) {
				t.Fatalf("query %q returned a non-matching name %q", needle, result.Name)
			}
		}
		if !found {
			t.Fatalf("query %q lost the live group it names (%s)", needle, target.Key)
		}
	}
}

func TestSearchMatchesMikanIDPrefix(t *testing.T) {
	snapshot, _ := For(ProviderMikan)
	prefix := snapshot.Entries[0].Key
	results := Search(ProviderMikan, prefix, MaxResults)
	if len(results) == 0 {
		t.Fatalf("id prefix %q matched nothing", prefix)
	}
	for _, result := range results {
		if !strings.HasPrefix(result.Key, prefix) {
			t.Fatalf("id prefix %q returned unrelated id %s", prefix, result.Key)
		}
	}
	// The id-prefix arm belongs to Mikan alone. An AniBT slug is matched as a
	// substring of the key, so a number that no slug or name contains finds
	// nothing there.
	if results := Search(ProviderAniBT, "999999999", MaxResults); len(results) != 0 {
		t.Fatalf("a numeric query matched AniBT entries: %+v", results)
	}
}

func TestSearchMatchesAniBTNamesAndSlugs(t *testing.T) {
	snapshot, _ := For(ProviderAniBT)
	target := snapshot.Entries[0]
	byName := Search(ProviderAniBT, target.Name, MaxResults)
	if len(byName) == 0 || byName[0].Key != target.Key {
		t.Fatalf("anibt name query %q resolved to %+v", target.Name, byName)
	}
	bySlug := Search(ProviderAniBT, strings.ToUpper(target.Key), MaxResults)
	if len(bySlug) == 0 || bySlug[0].Key != target.Key {
		t.Fatalf("anibt slug query %q resolved to %+v", target.Key, bySlug)
	}
	if results := Search(ProviderAniBT, "nothing-like-this", MaxResults); len(results) != 0 {
		t.Fatalf("unmatched anibt query returned %+v", results)
	}
}

func TestSearchOrdersLiveEntriesFirst(t *testing.T) {
	needle := ""
	for _, candidate := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "a", "e", "i", "o", "u", "s"} {
		live, retired := 0, 0
		for _, result := range Search(ProviderMikan, candidate, MaxResults) {
			if result.Alive {
				live++
				continue
			}
			retired++
		}
		if live > 0 && retired > 0 {
			needle = candidate
			break
		}
	}
	if needle == "" {
		t.Skip("no query in this snapshot mixes live and retired ids")
	}
	seenRetired := false
	for _, result := range Search(ProviderMikan, needle, MaxResults) {
		if !result.Alive {
			seenRetired = true
			continue
		}
		if seenRetired {
			t.Fatalf("query %q ordered a live id after a retired one", needle)
		}
	}
	// The page is filled after ordering, so a dense prefix cannot push live
	// groups out of a bounded page.
	if results := Search(ProviderMikan, needle, 1); len(results) != 1 || !results[0].Alive {
		t.Fatalf("bounded page for %q did not lead with a live group: %+v", needle, results)
	}
}

// Matches is the unbounded form the paged dictionary listing reads: the whole
// directory for an empty query, live ids first, in the same order as Search.
func TestMatchesReturnsTheWholeOrderedDirectory(t *testing.T) {
	snapshot, ok := For(ProviderMikan)
	if !ok {
		t.Fatal("mikan snapshot missing")
	}
	all := Matches(ProviderMikan, "")
	if len(all) != len(snapshot.Entries) {
		t.Fatalf("Matches returned %d of %d entries", len(all), len(snapshot.Entries))
	}
	seenRetired := false
	for _, entry := range all {
		if !entry.Alive {
			seenRetired = true
		} else if seenRetired {
			t.Fatalf("a live id %s follows a retired one", entry.Key)
		}
	}
	bounded := Search(ProviderMikan, "", MaxResults)
	for index, entry := range bounded {
		if all[index].Key != entry.Key {
			t.Fatalf("Matches and Search disagree at %d: %s vs %s", index, all[index].Key, entry.Key)
		}
	}
	if Matches("tmdb", "") != nil {
		t.Fatal("unknown provider returned entries")
	}
}

func TestSearchIsBoundedAndNilByDefault(t *testing.T) {
	if results := Search(ProviderMikan, "a", 0); results != nil {
		t.Fatalf("zero bound returned %+v", results)
	}
	if results := Search("tmdb", "a", MaxResults); results != nil {
		t.Fatalf("unknown provider returned %+v", results)
	}
	all := Search(ProviderMikan, "", 1000)
	if len(all) != MaxResults {
		t.Fatalf("empty query returned %d entries, want the %d-entry cap", len(all), MaxResults)
	}
	for _, result := range all {
		if !result.Alive {
			t.Fatalf("browsing the collected layer led with a retired id: %+v", result)
		}
	}
}

func TestLookupIsExactAndNormalizesMikanIDs(t *testing.T) {
	snapshot, _ := For(ProviderMikan)
	target := snapshot.Entries[0]
	for _, spelling := range []string{target.Key, "00" + target.Key} {
		entry, ok := Lookup(ProviderMikan, spelling)
		if !ok || entry.Key != target.Key {
			t.Fatalf("exact lookup of %q returned %+v ok=%v", spelling, entry, ok)
		}
	}
	// A longer number that shares the prefix is a different key, not a match.
	if _, ok := Lookup(ProviderMikan, target.Key+"9999999"); ok {
		t.Fatalf("lookup matched a longer id than %q", target.Key)
	}
	if _, ok := Lookup(ProviderMikan, "not-a-number"); ok {
		t.Fatal("lookup accepted a non-numeric Mikan key")
	}
	if _, ok := Lookup("tmdb", target.Key); ok {
		t.Fatal("lookup accepted an unknown provider")
	}
	anibt, _ := For(ProviderAniBT)
	if entry, ok := Lookup(ProviderAniBT, anibt.Entries[0].Key); !ok || entry.Name != anibt.Entries[0].Name {
		t.Fatalf("exact slug lookup failed: %+v ok=%v", entry, ok)
	}
	if _, ok := Lookup(ProviderAniBT, strings.ToUpper(anibt.Entries[0].Key)); ok {
		t.Fatal("anibt slugs are case-sensitive keys; an uppercased slug must not resolve exactly")
	}
}

// firstLiveASCII returns a live entry whose name is ASCII and not numeric, so
// case folding is observable and the query cannot also be an id prefix.
func firstLiveASCII(t *testing.T, provider string) Entry {
	t.Helper()
	snapshot, ok := For(provider)
	if !ok {
		t.Fatalf("%s directory is unavailable", provider)
	}
	for _, entry := range snapshot.Entries {
		if !entry.Alive || len(entry.Name) < 4 || !isASCII(entry.Name) || isDecimal(entry.Name) {
			continue
		}
		return entry
	}
	t.Fatalf("%s directory carries no ASCII-named live group to exercise case folding", provider)
	return Entry{}
}

func isASCII(value string) bool {
	for _, character := range value {
		if character > 0x7f {
			return false
		}
	}
	return value != ""
}

// isDecimal reports whether a name is all digits, which would let the name
// double as a Mikan id prefix and blur what a test is proving.
func isDecimal(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}

func TestMikanRawBucketResolvesToReleaseID(t *testing.T) {
	results := Search(ProviderMikan, "生肉/不明字幕", MaxResults)
	if len(results) != 1 || results[0].Key != "202" || !results[0].Alive {
		t.Fatalf("raw bucket name resolved to %+v, want only live id 202", results)
	}
	if results := Search(ProviderMikan, "180", MaxResults); len(results) != 0 {
		t.Fatalf("removed id 180 is still listed: %+v", results)
	}
}
