package domain

import (
	"encoding/json"
	"fmt"
)

// StoredTitleTags is the persisted (public) shape of one release's
// title-parse tag set inside Release.source_facts.title_tags.
type StoredTitleTags struct {
	Schema         string   `json:"schema"`
	Implementation string   `json:"implementation"`
	ConfigDigest   string   `json:"config_digest,omitempty"`
	Tags           []string `json:"tags"`
	ValueTags      []string `json:"value_tags,omitempty"`
	Diagnostic     string   `json:"diagnostic,omitempty"`
}

// StoredAniBTTags is the persisted (public) shape of one release's AniBT tag
// set inside Release.source_facts.anibt.
type StoredAniBTTags struct {
	Schema     string   `json:"schema,omitempty"`
	Tags       []string `json:"tags"`
	Diagnostic string   `json:"diagnostic,omitempty"`
}

// UnmarshalJSON reads a stored block and reclassifies customTag values frozen
// as "tag:<value>" before the classification table existed, so every reader
// of a persisted set sees the current dimensions.
func (s *StoredAniBTTags) UnmarshalJSON(data []byte) error {
	type plain StoredAniBTTags
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	decoded.Tags = ReclassifyAniBTTags(decoded.Tags)
	*s = StoredAniBTTags(decoded)
	return nil
}

// StoredTitleTagsFromSet is the persisted shape of a title tag set.
func StoredTitleTagsFromSet(set TitleTagSet) *StoredTitleTags {
	return &StoredTitleTags{Schema: set.Schema, Implementation: set.Implementation, ConfigDigest: set.ConfigDigest, Tags: append([]string{}, set.Tags...), ValueTags: append([]string(nil), set.ValueTags...), Diagnostic: set.Diagnostic}
}

// TitleTagSetFromStored rebuilds the frozen title tag set; nil stays nil.
func TitleTagSetFromStored(stored *StoredTitleTags) *TitleTagSet {
	if stored == nil {
		return nil
	}
	return &TitleTagSet{Schema: stored.Schema, Implementation: stored.Implementation, ConfigDigest: stored.ConfigDigest, Tags: append([]string{}, stored.Tags...), ValueTags: append([]string(nil), stored.ValueTags...), Diagnostic: stored.Diagnostic}
}

// StoredAniBTTagsFromSet is the persisted shape of an AniBT tag set; nil stays
// nil so "this provider has no tag block" survives the round trip.
func StoredAniBTTagsFromSet(set *AniBTTagSet) *StoredAniBTTags {
	if set == nil {
		return nil
	}
	return &StoredAniBTTags{Schema: set.Schema, Tags: append([]string{}, set.Tags...), Diagnostic: set.Diagnostic}
}

// AniBTTagSetFromStored rebuilds the frozen AniBT tag set; nil stays nil.
func AniBTTagSetFromStored(stored *StoredAniBTTags) *AniBTTagSet {
	if stored == nil {
		return nil
	}
	return &AniBTTagSet{Schema: stored.Schema, Tags: append([]string{}, stored.Tags...), Diagnostic: stored.Diagnostic}
}

// DecodeSourceFactTagSets reads the two frozen tag blocks out of a persisted
// Release.source_facts document. An absent block stays nil; empty or null
// input yields two nils.
func DecodeSourceFactTagSets(sourceFacts []byte) (*TitleTagSet, *AniBTTagSet, error) {
	if len(sourceFacts) == 0 || string(sourceFacts) == "null" {
		return nil, nil, nil
	}
	var stored struct {
		AniBT *StoredAniBTTags `json:"anibt"`
		Title *StoredTitleTags `json:"title_tags"`
	}
	if err := json.Unmarshal(sourceFacts, &stored); err != nil {
		return nil, nil, fmt.Errorf("decode source fact tag sets: %w", err)
	}
	return TitleTagSetFromStored(stored.Title), AniBTTagSetFromStored(stored.AniBT), nil
}
