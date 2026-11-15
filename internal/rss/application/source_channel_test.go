package application

import (
	"encoding/json"
	"testing"

	acquisitiondomain "bansoko/internal/acquisition/domain"
	animedomain "bansoko/internal/anime/domain"
)

func TestCustomChannelDoesNotReplaceParserProvider(t *testing.T) {
	item := acquisitiondomain.FeedItem{Provider: "anibt", Title: "Sample", DetailsURL: "https://anibt.net/release/sample"}
	raw, err := encodeInternalFeedFactsForChannel(item, "custom")
	if err != nil {
		t.Fatal(err)
	}
	var stored storedFeedFacts
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Channel != "custom" || stored.Provider != "anibt" || !animedomain.IsAniBTRelease(item) {
		t.Fatalf("channel changed adapter semantics: %+v", stored)
	}
}
