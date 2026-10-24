package httpapi

import (
	"testing"

	"bansoko/internal/parser/reltitletags"
)

func TestTitleTagVocabularyViewListsEveryDimension(t *testing.T) {
	view, err := titleTagVocabularyView()
	if err != nil {
		t.Fatal(err)
	}
	if view.Implementation != reltitletags.ImplementationVersion || view.ConfigDigest == "" || len(view.Dimensions) != len(reltitletags.Dimensions) {
		t.Fatalf("view = %+v", view)
	}
	for index, dimension := range view.Dimensions {
		if dimension.Dimension != reltitletags.Dimensions[index] || len(dimension.Values) == 0 {
			t.Fatalf("dimension %d = %+v", index, dimension)
		}
	}
}
