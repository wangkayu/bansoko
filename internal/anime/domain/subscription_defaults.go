package domain

import (
	"encoding/json"
	"fmt"
)

// SectionSubscriptionDefaults is the Global default of the per-subscription
// admission toggles. It is a one-time-input default like tag_filters: a new
// draft copies it once, the subscription freezes the copy, and evaluation
// never reads Global.
const SectionSubscriptionDefaults = "subscription_defaults"

// SubscriptionDefaultsSchemaVersion pins the stored shape of the section.
const SubscriptionDefaultsSchemaVersion = "subscription-defaults-v1"

// SubscriptionDefaults is the complete section value. Every field is a plain
// boolean default; new admission toggles join this section, not a new one.
type SubscriptionDefaults struct {
	// IgnoreMultiVideoReleases seeds the draft's ignore-multi-video option
	// (subscriptions.ignore_multi_video_releases at subscribe).
	IgnoreMultiVideoReleases bool `json:"ignore_multi_video_releases"`
}

// DefaultSubscriptionDefaults is the section's value before it is first saved.
func DefaultSubscriptionDefaults() SubscriptionDefaults {
	return SubscriptionDefaults{IgnoreMultiVideoReleases: false}
}

// ValidateSubscriptionDefaultsJSON checks one complete section value. Every
// member is required, so a partial value, an unknown key or a renamed field
// fails closed instead of being stored silently.
func ValidateSubscriptionDefaultsJSON(raw json.RawMessage) error {
	_, err := DecodeSubscriptionDefaults(raw)
	return err
}

// DecodeSubscriptionDefaults decodes and validates one stored or submitted
// section value.
func DecodeSubscriptionDefaults(raw json.RawMessage) (SubscriptionDefaults, error) {
	var probe struct {
		IgnoreMultiVideoReleases *bool `json:"ignore_multi_video_releases"`
	}
	if err := decodeStrictJSON(raw, &probe); err != nil || probe.IgnoreMultiVideoReleases == nil {
		return SubscriptionDefaults{}, fmt.Errorf("subscription defaults value must carry a boolean ignore_multi_video_releases")
	}
	return SubscriptionDefaults{IgnoreMultiVideoReleases: *probe.IgnoreMultiVideoReleases}, nil
}
