package httpapi

import (
	"net/http"

	"bansoko/internal/parser/reltitletags"
	"bansoko/internal/qualityprofile"
	"bansoko/internal/transport/httpapi/generated"
)

// rtCompareDimensions maps a ranked rt.* comparison item to its reltitle
// dictionary dimension.
var rtCompareDimensions = map[string]string{
	qualityprofile.KeyRtResolution: "resolution",
	qualityprofile.KeyRtSubLang:    "sub_lang",
	qualityprofile.KeyRtSubFlag:    "sub_flag",
	qualityprofile.KeyRtSource:     "source_type",
	qualityprofile.KeyRtCodec:      "video_codec",
}

// rtValueCompareDimensions maps a ranked rt.* item that reltitle reads as a
// value (outside the filter-tag dictionary) to its attribute name; its
// candidates are the pinned lexicon spellings.
var rtValueCompareDimensions = map[string]string{
	qualityprofile.KeyRtContentRating: "content_rating",
}

// GetReplacementCompareVocabulary returns the candidate values of every ranked
// comparison item, in registry order: release.* and anibt.* from the
// FileMaintenance owner, rt.* from the pinned reltitle dictionary (empty when
// it is unavailable).
func (s *Server) GetReplacementCompareVocabulary(w http.ResponseWriter, r *http.Request, params generated.GetReplacementCompareVocabularyParams) {
	animeID := ""
	if params.ManagedAnimeId != nil {
		animeID = string(*params.ManagedAnimeId)
	}
	values, err := s.deps.FileMaintenance.CompareVocabularyApplication(r.Context(), animeID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if reltitletags.Ready() == nil {
		byDimension := map[string][]string{}
		for _, entry := range reltitletags.Vocabulary() {
			byDimension[entry.Dimension] = entry.Values
		}
		for key, dimension := range rtCompareDimensions {
			values[key] = append([]string{}, byDimension[dimension]...)
		}
		for key, dimension := range rtValueCompareDimensions {
			values[key] = append([]string{}, reltitletags.ValueVocabulary(dimension)...)
		}
	}
	view := generated.ReplacementCompareVocabulary{Items: []generated.RetentionCompareVocabularyItem{}}
	for _, item := range qualityprofile.Items {
		if !item.Ranked() {
			continue
		}
		list := values[item.Key]
		if list == nil {
			list = []string{}
		}
		view.Items = append(view.Items, generated.RetentionCompareVocabularyItem{Key: generated.QualityCompareKey(item.Key), Values: list})
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}
