package application

import (
	"reflect"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	"bansoko/internal/parser/reltitletags"
)

func TestSourceFactsFreezeTitleTags(t *testing.T) {
	item := acquisitiondomain.FeedItem{GUID: "rel-1", Title: "[G] Show - 01 [WebRip 1080p HEVC-10bit AAC][CHS]"}
	_, _, encoded, err := EncodeSourceFacts(item, "G")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStoredFeedItem([]byte(`{"title":"[G] Show - 01 [WebRip 1080p HEVC-10bit AAC][CHS]"}`), nil, encoded, nil, "rel-1")
	if err != nil {
		t.Fatal(err)
	}
	want := reltitletags.Extract(item.Title)
	if decoded.TitleTags == nil || !reflect.DeepEqual(*decoded.TitleTags, want) {
		t.Fatalf("decoded title tags = %#v, want %#v", decoded.TitleTags, want)
	}
	if decoded.TitleTags.Implementation != reltitletags.ImplementationVersion || decoded.TitleTags.ConfigDigest != reltitletags.ConfigDigest() {
		t.Fatalf("frozen provenance = %#v", decoded.TitleTags)
	}

	// A frozen set is re-encoded verbatim; the title is not parsed again.
	frozen := acquisitiondomain.TitleTagSet{Schema: reltitletags.Schema, Implementation: "reltitle@older", ConfigDigest: "lexicon:sha256:old", Tags: []string{"resolution:720p"}}
	decoded.TitleTags = &frozen
	_, _, encoded, err = EncodeSourceFacts(decoded, "G")
	if err != nil {
		t.Fatal(err)
	}
	again, err := DecodeStoredFeedItem([]byte(`{"title":"x"}`), nil, encoded, nil, "rel-1")
	if err != nil || again.TitleTags == nil || !reflect.DeepEqual(*again.TitleTags, frozen) {
		t.Fatalf("frozen round trip = %#v, %v", again.TitleTags, err)
	}

	// A release persisted before the title row reads back without a set.
	legacy, err := DecodeStoredFeedItem([]byte(`{"title":"x"}`), nil, []byte(`{"identity_kind":"guid","identity_value":"rel-1"}`), nil, "rel-1")
	if err != nil || legacy.TitleTags != nil {
		t.Fatalf("legacy source facts = %#v, %v", legacy.TitleTags, err)
	}
}

func TestSourceFactsFreezeValueTags(t *testing.T) {
	item := FreezeTitleTags(acquisitiondomain.FeedItem{GUID: "rel-2", Title: "[SampleGroup] Sample Title - 03 [1080p][CHS].mp4"})
	if item.TitleTags == nil || item.TitleTags.Schema != acquisitiondomain.TitleTagSchema || len(item.TitleTags.ValueTags) == 0 {
		t.Fatalf("frozen item tags = %#v", item.TitleTags)
	}
	_, _, encoded, err := EncodeSourceFacts(item, "G")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeStoredFeedItem([]byte(`{"title":"x"}`), nil, encoded, nil, "rel-2")
	if err != nil || decoded.TitleTags == nil || !reflect.DeepEqual(decoded.TitleTags.ValueTags, item.TitleTags.ValueTags) {
		t.Fatalf("name tags round trip = %#v, %v", decoded.TitleTags, err)
	}
	// FreezeTitleTags keeps a set the item already carries.
	kept := FreezeTitleTags(decoded)
	if kept.TitleTags != decoded.TitleTags {
		t.Fatal("FreezeTitleTags replaced a frozen set")
	}
}
