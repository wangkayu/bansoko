package domain

import (
	"encoding/json"
	"fmt"
)

const (
	SectionSubscriptionTitle       = "subscription_title"
	SubscriptionTitleSchemaVersion = "subscription-title-v1"
	SubscriptionTitleKeep          = "keep"
	TitleSourceTMDBCN              = "tmdb_cn"
	TitleSourceTMDBOriginal        = "tmdb_original"
	TitleSourceBGMCN               = "bgm_cn"
	TitleSourceBGM                 = "bgm"
)

type SubscriptionTitle struct {
	Source string `json:"source"`
}

func ValidateSubscriptionTitleJSON(raw json.RawMessage) error {
	var value SubscriptionTitle
	if err := decodeStrictJSON(raw, &value); err == nil {
		switch value.Source {
		case SubscriptionTitleKeep, TitleSourceTMDBCN, TitleSourceTMDBOriginal, TitleSourceBGMCN, TitleSourceBGM:
			return nil
		}
	}
	return fmt.Errorf("subscription_title requires one supported source")
}
