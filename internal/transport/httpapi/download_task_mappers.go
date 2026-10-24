package httpapi

import (
	"encoding/json"
	"fmt"

	animeapplication "bansoko/internal/anime/application"
	"bansoko/internal/transport/httpapi/generated"
)

func mapFrozenDownloadInput(input animeapplication.FrozenDownloadInput) (generated.FrozenDownloadInput, error) {
	var result generated.FrozenDownloadInput
	if err := mapJSONValue(input, &result); err != nil {
		return generated.FrozenDownloadInput{}, fmt.Errorf("map frozen download input: %w", err)
	}
	return result, nil
}

func mapFrozenPolicySnapshot(snapshot animeapplication.FrozenPolicySnapshot) (generated.FrozenPolicySnapshot, error) {
	var result generated.FrozenPolicySnapshot
	if err := mapJSONValue(snapshot, &result); err != nil {
		return generated.FrozenPolicySnapshot{}, fmt.Errorf("map frozen policy snapshot: %w", err)
	}
	return result, nil
}

func mapJSONValue(input any, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, output)
}
