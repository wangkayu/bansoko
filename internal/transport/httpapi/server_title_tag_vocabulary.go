package httpapi

import (
	"net/http"

	"bansoko/internal/parser/reltitletags"
	"bansoko/internal/transport/httpapi/generated"
)

// titleTagVocabularyView maps the pinned reltitle dictionary to the public
// vocabulary of the tag_filters title row.
func titleTagVocabularyView() (generated.TitleTagVocabulary, error) {
	if err := reltitletags.Ready(); err != nil {
		return generated.TitleTagVocabulary{}, unavailable("TITLE_TAG_PARSER_UNAVAILABLE", "the title-parse dictionary is unavailable")
	}
	vocabulary := reltitletags.Vocabulary()
	dimensions := make([]generated.TitleTagDimension, 0, len(vocabulary))
	for _, entry := range vocabulary {
		dimensions = append(dimensions, generated.TitleTagDimension{Dimension: entry.Dimension, Values: append([]string{}, entry.Values...)})
	}
	return generated.TitleTagVocabulary{Implementation: reltitletags.ImplementationVersion, ConfigDigest: reltitletags.ConfigDigest(), Dimensions: dimensions}, nil
}

func (s *Server) GetTitleTagVocabulary(w http.ResponseWriter, r *http.Request) {
	view, err := titleTagVocabularyView()
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}
