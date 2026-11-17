package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// FreezeSourceTags selects the recorded native tag blocks without interpreting
// them. In particular it must not run AniBT reclassification or today's title
// parser while constructing evidence. The accepted input digest covers this
// exact value; missing blocks remain explicit nulls.
func FreezeSourceTags(sourceFacts []byte) (Value, error) {
	var source map[string]json.RawMessage
	raw := bytes.TrimSpace(sourceFacts)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &source); err != nil {
			return Value{}, fmt.Errorf("freeze source tags: %w", err)
		}
	}
	evidence := Value{Kind: KindObject, Object: map[string]Value{
		"title_tags": {Kind: KindNull},
		"anibt":      {Kind: KindNull},
	}}
	for key := range evidence.Object {
		if block, ok := source[key]; ok {
			value, err := ParseStrict(block)
			if err != nil {
				return Value{}, fmt.Errorf("freeze source tags %s: %w", key, err)
			}
			evidence.Object[key] = value
		}
	}
	if err := Validate("SourceTagsV1", evidence); err != nil {
		return Value{}, fmt.Errorf("freeze source tags: %w", err)
	}
	return evidence, nil
}
