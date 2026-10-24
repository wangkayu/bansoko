package domain

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type SubscriptionState string

// A Subscription starts pending (not started): its configuration is fully
// editable but it does no work. pending -> active is a normal first start;
// active <-> stopped is stop/resume. There is no transition back to pending
// and no pending -> stopped.
const (
	SubscriptionPending SubscriptionState = "pending"
	SubscriptionActive  SubscriptionState = "active"
	SubscriptionStopped SubscriptionState = "stopped"
)

// Valid reports whether the state is one of the closed Subscription states.
func (s SubscriptionState) Valid() bool {
	return s == SubscriptionPending || s == SubscriptionActive || s == SubscriptionStopped
}

type SourceRefreshIneligibility string

const (
	SourceRefreshEligible            SourceRefreshIneligibility = ""
	SourceRefreshSubscriptionStopped SourceRefreshIneligibility = "subscription_stopped"
	// SourceRefreshSubscriptionPending blocks every refresh, manual included:
	// a subscription that was never started does no work.
	SourceRefreshSubscriptionPending SourceRefreshIneligibility = "subscription_pending"
	SourceRefreshPolicyNotSelected   SourceRefreshIneligibility = "policy_not_selected"
)

type SourceRefreshEligibilityFacts struct {
	SubscriptionState       SubscriptionState
	ProviderSource          bool
	ProviderSelectionExists bool
	ProviderSelected        bool
}

func EvaluateSourceRefreshEligibility(facts SourceRefreshEligibilityFacts) SourceRefreshIneligibility {
	switch facts.SubscriptionState {
	case SubscriptionActive:
	case SubscriptionStopped:
		return SourceRefreshSubscriptionStopped
	default:
		// pending, and any unknown state, fails closed.
		return SourceRefreshSubscriptionPending
	}
	if facts.ProviderSource && (!facts.ProviderSelectionExists || !facts.ProviderSelected) {
		return SourceRefreshPolicyNotSelected
	}
	return SourceRefreshEligible
}

const RSSLocatorSchemeUnsupportedCode = "RSS_LOCATOR_SCHEME_UNSUPPORTED"

var ErrRSSLocatorSchemeUnsupported = errors.New("RSS locator scheme is unsupported")

type RSSLocator struct {
	URL                string `json:"url"`
	HTTPFeedConnection string `json:"http_feed_connection_ref,omitempty"`
	NetworkRoute       string `json:"network_route_ref,omitempty"`
}

func ValidateRSSLocatorExecutionScheme(raw string) error {
	trimmed := strings.TrimSpace(raw)
	separator := strings.IndexByte(trimmed, ':')
	if separator <= 0 {
		return nil
	}
	scheme := strings.ToLower(trimmed[:separator])
	if scheme != "http" && scheme != "https" {
		return ErrRSSLocatorSchemeUnsupported
	}
	return nil
}

func (l RSSLocator) Normalize() (RSSLocator, error) {
	raw := strings.TrimSpace(l.URL)
	if raw == "" || len(raw) > 4096 {
		return RSSLocator{}, errors.New("RSS URL is required and must be at most 4096 bytes")
	}
	if err := ValidateRSSLocatorExecutionScheme(raw); err != nil {
		return RSSLocator{}, err
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return RSSLocator{}, errors.New("RSS URL must be an absolute URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return RSSLocator{}, ErrRSSLocatorSchemeUnsupported
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return RSSLocator{}, errors.New("RSS URL must not contain credentials or fragments")
	}
	if IsPrivateHost(parsed.Hostname()) {
		return RSSLocator{}, errors.New("RSS URL targets a private or local host")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	l.URL = parsed.String()
	l.HTTPFeedConnection = strings.TrimSpace(l.HTTPFeedConnection)
	l.NetworkRoute = strings.TrimSpace(l.NetworkRoute)
	if len(l.HTTPFeedConnection) > 256 || len(l.NetworkRoute) > 256 {
		return RSSLocator{}, errors.New("RSS connection or network route reference is too long")
	}
	return l, nil
}

func IsPrivateHost(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") || strings.HasSuffix(strings.ToLower(host), ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

type Subscription struct {
	ID             string
	ManagedAnimeID string
	GroupIdentity  *LogicalGroupIdentity
	State          SubscriptionState
	Version        int64
}

func (s Subscription) Validate() error {
	if s.ID == "" || s.ManagedAnimeID == "" {
		return fmt.Errorf("subscription identity is incomplete")
	}
	if !s.State.Valid() {
		return fmt.Errorf("unsupported subscription state %q", s.State)
	}
	return nil
}
