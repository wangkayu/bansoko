// Package groupdir is the collected provider group directory: the read-only
// subgroup snapshot that ships with the release and seeds the subgroup
// dictionary. It answers "which groups does the provider have?" for both
// providers without any network access.
//
// The dictionary the settings page selects from is this collected snapshot
// union the rows a user retrieved and added (provider_group_dictionary). This
// package is only the collected half: it is never written, and a group created
// after the snapshot is reached through the dictionary's retrieval path (one
// live provider read) instead of a periodic crawl.
//
// Provenance of the embedded snapshots:
//
//	collector       the collection scripts are archived with the task that ran
//	                them, not shipped here: they live in the private overlay at
//	                .trellis/tasks/archive/2026-10/10-16-subgroup-dataset/scripts/
//	                (scrape_mikan.py, scrape_anibt.py, assemble_subgroups.py).
//	                They are absent from this repository on purpose — they
//	                reach the public provider network — so regenerating a
//	                snapshot is an owner operation that needs that overlay,
//	                not a command a checkout can run.
//	collection      mikan_groups.json: scrape_mikan.py --start 1 --end 1400,
//	                then assemble_subgroups.py — every probed Mikan id with its
//	                last observed published name and the liveness observed at
//	                snapshot time (a retired id answered 302 to the site root
//	                and carries no name)
//	                anibt_groups.json: scrape_anibt.py — the single
//	                server-rendered /en/group page, whose 35 cards are the
//	                whole AniBT group set (`native_id` and `updated_at` are
//	                carried for provenance; they are not search identity)
//	regenerate      re-run those scripts, then rebuild each JSON from
//	                subgroups.json keeping that provider's fields
//	owner edits     the Mikan raw/unknown bucket 「生肉/不明字幕」 is id 202:
//	                releases with no publish group carry it and it has no
//	                PublishGroup page, so the probe saw it as retired; it is
//	                kept live under that name. id 180, which the probe saw
//	                published under the same name, is removed so a name
//	                search resolves to the id releases actually carry.
//	                Re-apply after regenerating.
//
// Mikan exposes no API to query groups by name: confirming one group means
// enumerating /Home/PublishGroup/{id}, which is roughly 28 minutes of
// rate-limited requests for the whole id space. This snapshot is what makes a
// name search possible at all.
package groupdir

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	providerdomain "bansoko/internal/provider/domain"
)

//go:embed mikan_groups.json
var mikanGroupsJSON []byte

//go:embed anibt_groups.json
var anibtGroupsJSON []byte

// Provider names the two directories this package carries: the provider keys
// the rest of the product uses.
const (
	ProviderAniBT = string(providerdomain.ProviderAniBT)
	ProviderMikan = string(providerdomain.ProviderMikan)
)

// MaxResults is the largest result set one search returns. The dictionary
// surface declares the same bound as its maxItems.
const MaxResults = 50

// Entry is one collected subgroup.
type Entry struct {
	// Key is the provider's own subgroup key: the Mikan numeric id, or the
	// AniBT slug. It is the identity the subgroup list stores.
	Key string
	// Name is the display name the provider published at snapshot time. A
	// retired Mikan id has none.
	Name string
	// Alive is the liveness observed at snapshot time. It is evidence about
	// the snapshot, never a live guarantee. AniBT's directory carries no
	// liveness bit, so its entries are reported available.
	Alive bool
}

// Snapshot is one provider's decoded collected directory.
type Snapshot struct {
	Provider      string
	SchemaVersion string
	SnapshotDate  string
	Entries       []Entry
}

// mikanEntryDTO mirrors the mikan snapshot resource.
type mikanEntryDTO struct {
	ID    int64  `json:"id"`
	Alive bool   `json:"alive"`
	Name  string `json:"name"`
}

// aniBTEntryDTO mirrors the anibt snapshot resource. native_id and updated_at
// travel with the source record but are not search identity, so they are not
// decoded.
type aniBTEntryDTO struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

var (
	loadOnce sync.Once
	snapshot map[string]Snapshot
	loadErr  error
)

// Load decodes both embedded snapshots once and returns them by provider.
func Load() (map[string]Snapshot, error) {
	loadOnce.Do(func() {
		mikan, err := decodeMikan(mikanGroupsJSON)
		if err != nil {
			loadErr = err
			return
		}
		anibt, err := decodeAniBT(anibtGroupsJSON)
		if err != nil {
			loadErr = err
			return
		}
		snapshot = map[string]Snapshot{ProviderMikan: mikan, ProviderAniBT: anibt}
	})
	return snapshot, loadErr
}

// For returns one provider's decoded snapshot.
func For(provider string) (Snapshot, bool) {
	snapshots, err := Load()
	if err != nil {
		return Snapshot{}, false
	}
	value, ok := snapshots[strings.ToLower(strings.TrimSpace(provider))]
	return value, ok
}

// SnapshotDate reports one provider's snapshot date.
func SnapshotDate(provider string) string {
	value, ok := For(provider)
	if !ok {
		return ""
	}
	return value.SnapshotDate
}

func decodeMikan(raw []byte) (Snapshot, error) {
	var resource struct {
		SchemaVersion string          `json:"schema_version"`
		SnapshotDate  string          `json:"snapshot_date"`
		Entries       []mikanEntryDTO `json:"entries"`
	}
	if err := json.Unmarshal(raw, &resource); err != nil {
		return Snapshot{}, fmt.Errorf("decode embedded Mikan group directory: %w", err)
	}
	if strings.TrimSpace(resource.SchemaVersion) == "" || strings.TrimSpace(resource.SnapshotDate) == "" {
		return Snapshot{}, fmt.Errorf("embedded Mikan group directory is missing its schema version or snapshot date")
	}
	entries := make([]Entry, 0, len(resource.Entries))
	var previous int64
	for _, item := range resource.Entries {
		if item.ID <= 0 {
			return Snapshot{}, fmt.Errorf("embedded Mikan group directory has a non-positive id %d", item.ID)
		}
		if item.ID <= previous {
			return Snapshot{}, fmt.Errorf("embedded Mikan group directory ids are not strictly ascending at %d", item.ID)
		}
		previous = item.ID
		if item.Alive && strings.TrimSpace(item.Name) == "" {
			return Snapshot{}, fmt.Errorf("embedded Mikan group directory marks %d alive without a name", item.ID)
		}
		entries = append(entries, Entry{Key: strconv.FormatInt(item.ID, 10), Name: item.Name, Alive: item.Alive})
	}
	if len(entries) == 0 {
		return Snapshot{}, fmt.Errorf("embedded Mikan group directory is empty")
	}
	return Snapshot{Provider: ProviderMikan, SchemaVersion: resource.SchemaVersion, SnapshotDate: resource.SnapshotDate, Entries: entries}, nil
}

func decodeAniBT(raw []byte) (Snapshot, error) {
	var resource struct {
		SchemaVersion string          `json:"schema_version"`
		SnapshotDate  string          `json:"snapshot_date"`
		Entries       []aniBTEntryDTO `json:"entries"`
	}
	if err := json.Unmarshal(raw, &resource); err != nil {
		return Snapshot{}, fmt.Errorf("decode embedded AniBT group directory: %w", err)
	}
	if strings.TrimSpace(resource.SchemaVersion) == "" || strings.TrimSpace(resource.SnapshotDate) == "" {
		return Snapshot{}, fmt.Errorf("embedded AniBT group directory is missing its schema version or snapshot date")
	}
	entries := make([]Entry, 0, len(resource.Entries))
	seen := make(map[string]struct{}, len(resource.Entries))
	for _, item := range resource.Entries {
		slug := strings.TrimSpace(item.Slug)
		name := strings.TrimSpace(item.Name)
		if slug == "" || name == "" {
			return Snapshot{}, fmt.Errorf("embedded AniBT group directory has an entry without a slug or name")
		}
		if _, exists := seen[slug]; exists {
			return Snapshot{}, fmt.Errorf("embedded AniBT group directory contains duplicate slug %q", slug)
		}
		seen[slug] = struct{}{}
		// AniBT's directory publishes no liveness bit: a listed group is one
		// the site currently serves.
		entries = append(entries, Entry{Key: slug, Name: name, Alive: true})
	}
	if len(entries) == 0 {
		return Snapshot{}, fmt.Errorf("embedded AniBT group directory is empty")
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Key < entries[right].Key })
	return Snapshot{Provider: ProviderAniBT, SchemaVersion: resource.SchemaVersion, SnapshotDate: resource.SnapshotDate, Entries: entries}, nil
}

// Search matches one provider's collected directory under the shared group
// query rule (providerdomain.GroupQueryMatches): a name matches as a
// case-insensitive substring, and a decimal query also matches a Mikan id as a
// prefix. An empty query returns the whole directory, which is what browsing
// one dictionary layer needs. Results are bounded by max and never exceed
// MaxResults; available groups come first, then ascending key.
//
// The rule is shared with the dictionary's user layer, so the collected and
// stored halves of one query answer identically.
func Search(provider, query string, max int) []Entry {
	snapshot, ok := For(provider)
	if !ok {
		return nil
	}
	if max <= 0 {
		return nil
	}
	if max > MaxResults {
		max = MaxResults
	}
	// Order before truncating: a retired Mikan id must never take a page slot
	// ahead of a live group that matches the same prefix.
	matches := matchSnapshot(snapshot, query)
	if len(matches) > max {
		matches = matches[:max]
	}
	return matches
}

// Matches is Search without the bound: every entry of one provider's collected
// directory that the query matches, available first and then ascending key. It
// backs the dictionary's paged listing, which bounds each page itself.
func Matches(provider, query string) []Entry {
	snapshot, ok := For(provider)
	if !ok {
		return nil
	}
	return matchSnapshot(snapshot, query)
}

func matchSnapshot(snapshot Snapshot, query string) []Entry {
	kind := providerdomain.Kind(snapshot.Provider)
	matches := make([]Entry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if !providerdomain.GroupQueryMatches(kind, entry.Key, entry.Name, query) {
			continue
		}
		matches = append(matches, entry)
	}
	return orderEntries(matches)
}

// orderEntries puts available entries first, then ascending key. Mikan keys are
// compared numerically, so 58 orders before 612.
func orderEntries(entries []Entry) []Entry {
	sort.SliceStable(entries, func(left, right int) bool {
		if entries[left].Alive != entries[right].Alive {
			return entries[left].Alive
		}
		return providerdomain.GroupKeyLess(entries[left].Key, entries[right].Key)
	})
	return entries
}

// Lookup resolves one exact key in one provider's collected directory, with no
// prefix or substring matching. A Mikan id is compared numerically, so a
// zero-padded spelling still resolves.
func Lookup(provider, key string) (Entry, bool) {
	snapshot, ok := For(provider)
	if !ok {
		return Entry{}, false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return Entry{}, false
	}
	if snapshot.Provider == ProviderMikan {
		wanted, err := strconv.ParseUint(key, 10, 64)
		if err != nil || wanted == 0 {
			return Entry{}, false
		}
		key = strconv.FormatUint(wanted, 10)
	}
	for _, entry := range snapshot.Entries {
		if entry.Key == key {
			return entry, true
		}
	}
	return Entry{}, false
}
