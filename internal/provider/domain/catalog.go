package domain

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// The table is build-embedded reference data. It is never refreshed from the
// network and does not establish provider availability.
//
//go:embed group_translations.json
var builtInGroupTranslations []byte

func BuiltInGroupTranslation() (TranslationVersion, error) {
	var version TranslationVersion
	if err := json.Unmarshal(builtInGroupTranslations, &version); err != nil {
		return TranslationVersion{}, fmt.Errorf("decode built-in group translation: %w", err)
	}
	if err := version.Validate(); err != nil {
		return TranslationVersion{}, err
	}
	return version, nil
}

func (v TranslationVersion) Validate() error {
	if strings.TrimSpace(v.SchemaVersion) == "" || strings.TrimSpace(v.DataVersion) == "" {
		return errors.New("group translation schema and data versions are required")
	}
	canonicalSeen := make(map[string]struct{}, len(v.Items))
	refSeen := make(map[string]string)
	for _, item := range v.Items {
		if strings.TrimSpace(item.CanonicalKey) == "" || strings.TrimSpace(item.DisplayName) == "" {
			return errors.New("group translation canonical key and display name are required")
		}
		if _, exists := canonicalSeen[item.CanonicalKey]; exists {
			return fmt.Errorf("duplicate canonical group key %q", item.CanonicalKey)
		}
		canonicalSeen[item.CanonicalKey] = struct{}{}
		for _, ref := range item.ProviderRefs {
			if err := ref.Validate(); err != nil {
				return fmt.Errorf("invalid translation mapping %q: %w", ref.Key(), err)
			}
			if previous, exists := refSeen[ref.Key()]; exists {
				return fmt.Errorf("provider group %q mapped by both %q and %q", ref.Key(), previous, item.CanonicalKey)
			}
			refSeen[ref.Key()] = item.CanonicalKey
		}
	}
	return nil
}

// BuildLocalGroupCatalogView joins cached provider facts with the immutable
// translation table. It performs no provider I/O and does not create domain
// records. The newest snapshot for each provider/catalog entry wins.
func BuildLocalGroupCatalogView(translation TranslationVersion, snapshots []GroupSnapshot, asOf time.Time) (LocalGroupCatalogView, error) {
	if err := translation.Validate(); err != nil {
		return LocalGroupCatalogView{}, err
	}
	latest := make(map[string]GroupSnapshot)
	for _, snapshot := range snapshots {
		if err := snapshot.Validate(); err != nil {
			return LocalGroupCatalogView{}, err
		}
		key := string(snapshot.Provider) + ":" + snapshot.CatalogEntryRef
		current, exists := latest[key]
		if !exists || snapshot.Version > current.Version || (snapshot.Version == current.Version && snapshot.ObservedAt.After(current.ObservedAt)) {
			latest[key] = snapshot
		}
	}

	items := make(map[string]LocalGroupCatalogItem)
	for _, snapshot := range latest {
		for _, group := range snapshot.Groups {
			key := group.Ref.Key()
			item := items[key]
			item.Ref = group.Ref
			item.DisplayName = group.DisplayName
			item.Availability = AvailabilityObserved
			item.SnapshotID = snapshot.ID
			item.SnapshotVersion = snapshot.Version
			observedAt := group.ObservedAt
			if observedAt.IsZero() {
				observedAt = snapshot.ObservedAt
			}
			item.LastObservedAt = &observedAt
			items[key] = item
		}
	}

	for _, translationItem := range translation.Items {
		for _, ref := range translationItem.ProviderRefs {
			key := ref.Key()
			item, exists := items[key]
			if !exists {
				item = LocalGroupCatalogItem{Ref: ref, Availability: AvailabilityNotObserved}
			}
			item.CanonicalKey = translationItem.CanonicalKey
			item.TranslatedName = translationItem.DisplayName
			item.Aliases = append([]string(nil), translationItem.Aliases...)
			if item.DisplayName == "" {
				item.DisplayName = translationItem.DisplayName
			}
			items[key] = item
		}
	}

	result := LocalGroupCatalogView{
		TranslationSchemaVersion: translation.SchemaVersion,
		TranslationDataVersion:   translation.DataVersion,
		Items:                    make([]LocalGroupCatalogItem, 0, len(items)),
		AsOf:                     asOf,
	}
	for _, item := range items {
		result.Items = append(result.Items, item)
	}
	SortLocalGroupCatalogItems(result.Items)
	return result, nil
}

func SortLocalGroupCatalogItems(items []LocalGroupCatalogItem) {
	sort.Slice(items, func(i, j int) bool { return items[i].Ref.Key() < items[j].Ref.Key() })
}
