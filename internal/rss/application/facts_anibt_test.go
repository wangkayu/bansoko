package application

import (
	"encoding/json"
	"reflect"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

func TestSourceFactsFreezeAniBTTags(t *testing.T) {
	item := acquisitiondomain.FeedItem{GUID: "rel-1", Title: "[G] Show - 01", Provider: "anibt", AniBTTags: &acquisitiondomain.AniBTTagSet{Schema: acquisitiondomain.AniBTTagSchemaV1, Tags: []string{"resolution:1080p", "language:CHS"}}}
	_, _, encoded, err := EncodeSourceFacts(item, "G")
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["anibt"]; !ok {
		t.Fatalf("source facts lack the anibt block: %s", encoded)
	}
	decoded, err := DecodeStoredFeedItem([]byte(`{"title":"[G] Show - 01"}`), nil, encoded, nil, "rel-1")
	if err != nil {
		t.Fatal(err)
	}
	if decoded.AniBTTags == nil || !reflect.DeepEqual(decoded.AniBTTags.Tags, item.AniBTTags.Tags) || decoded.AniBTTags.Schema != acquisitiondomain.AniBTTagSchemaV1 {
		t.Fatalf("decoded tags = %#v", decoded.AniBTTags)
	}

	// An empty AniBT set is still frozen (it means "AniBT, no tags").
	item.AniBTTags = &acquisitiondomain.AniBTTagSet{Tags: []string{}, Diagnostic: acquisitiondomain.AniBTTagDiagnosticNamespaceLegacy}
	_, _, encoded, err = EncodeSourceFacts(item, "G")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err = DecodeStoredFeedItem([]byte(`{"title":"[G] Show - 01"}`), nil, encoded, nil, "rel-1")
	if err != nil || decoded.AniBTTags == nil || len(decoded.AniBTTags.Tags) != 0 || decoded.AniBTTags.Diagnostic != acquisitiondomain.AniBTTagDiagnosticNamespaceLegacy {
		t.Fatalf("empty set round trip = %#v, %v", decoded.AniBTTags, err)
	}

	// Non-AniBT releases carry no anibt block (the title-parse block applies
	// to every release and is covered by TestSourceFactsFreezeTitleTags).
	item.AniBTTags = nil
	_, _, encoded, err = EncodeSourceFacts(item, "G")
	if err != nil {
		t.Fatal(err)
	}
	probe = map[string]any{}
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatal(err)
	}
	if _, ok := probe["anibt"]; ok {
		t.Fatalf("non-AniBT source facts carry an anibt block: %s", encoded)
	}
}
