package workitems

import "encoding/json"

// SubmitInputSnapshot preserves the existing trigger and one-shot merge
// encoding. Both legacy Store.Create and native acceptance use the same merge.
func SubmitInputSnapshot(trigger string, ref *OneShotRef) (json.RawMessage, error) {
	snapshot, err := json.Marshal(map[string]string{"trigger": trigger})
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return snapshot, nil
	}
	if err := ref.validate(); err != nil {
		return nil, err
	}
	return mergeOneShotSnapshot(snapshot, ref)
}
