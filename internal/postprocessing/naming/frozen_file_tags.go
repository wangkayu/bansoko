package naming

import (
	"bytes"
	"encoding/json"
	"fmt"

	acquisitiondomain "bansoko/internal/acquisition/domain"
)

// FrozenFileTitleTags reads only the parse captured by an operation. Missing or
// explicitly null evidence stays absent; it must never trigger a new parse of
// either the original name or a renamed library path. An empty tag set remains
// a prepared empty result, including its parser version and diagnostic.
func FrozenFileTitleTags(snapshot []byte) (*acquisitiondomain.TitleTagSet, error) {
	raw := bytes.TrimSpace(snapshot)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("%w: invalid frozen render snapshot: %v", ErrRenderFactsUnavailable, err)
	}
	block := bytes.TrimSpace(document["file_title_tags"])
	if len(block) == 0 || bytes.Equal(block, []byte("null")) {
		return nil, nil
	}
	var stored acquisitiondomain.StoredTitleTags
	if err := json.Unmarshal(block, &stored); err != nil {
		return nil, fmt.Errorf("%w: invalid frozen file title tags: %v", ErrRenderFactsUnavailable, err)
	}
	return acquisitiondomain.TitleTagSetFromStored(&stored), nil
}
