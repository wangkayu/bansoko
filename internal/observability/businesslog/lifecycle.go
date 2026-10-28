package businesslog

import (
	"context"
	"fmt"
	"strings"

	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/persistence/txscope"
)

// SubscriptionTransition names one subscription state transition.
type SubscriptionTransition string

const (
	// SubscriptionTransitionStarted is the first start (pending -> active).
	SubscriptionTransitionStarted SubscriptionTransition = "started"
	// SubscriptionTransitionStopped is active -> stopped.
	SubscriptionTransitionStopped SubscriptionTransition = "stopped"
	// SubscriptionTransitionResumed is stopped -> active.
	SubscriptionTransitionResumed SubscriptionTransition = "resumed"
)

// WriteSubscriptionStateTx appends one business main-line entry for a
// subscription state transition. It participates in the caller's owner
// transaction; the versioned dedupe key makes idempotent replays safe.
func WriteSubscriptionStateTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, subscriptionID, managedAnimeID string, transition SubscriptionTransition, newVersion int64) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	var kind observabilityport.ActivityKind
	var code observabilityport.MessageCode
	switch transition {
	case SubscriptionTransitionStarted:
		kind, code = observabilityport.ActivityKindSubscriptionStarted, observabilityport.MessageSubscriptionStarted
	case SubscriptionTransitionStopped:
		kind, code = observabilityport.ActivityKindSubscriptionStopped, observabilityport.MessageSubscriptionStopped
	case SubscriptionTransitionResumed:
		kind, code = observabilityport.ActivityKindSubscriptionResumed, observabilityport.MessageSubscriptionResumed
	default:
		return fmt.Errorf("unknown subscription transition %q", transition)
	}
	return writer.AppendTx(ctx, scope, observabilityport.ActivityRecord{
		SubscriptionID: subscriptionID, ManagedAnimeID: managedAnimeID, Kind: string(kind), Outcome: "info",
		ReasonCode: string(kind), SubjectType: "subscription", SubjectID: subscriptionID, SubjectVersion: newVersion,
		MessageCode: code,
	})
}

// WriteRulesPolicyChangedTx appends one business main-line entry for a committed
// rules owner CAS save that changed at least one section. It uses the rules
// owner version as the replay identity; changedSections (rules section names,
// in section order) say what changed and are frozen into the details, which
// the rules_changed message lists.
func WriteRulesPolicyChangedTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, subscriptionID, managedAnimeID string, newVersion int64, changedSections []string) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	details, err := MarshalDetails(map[string]any{"changed_sections": changedSections})
	if err != nil {
		return fmt.Errorf("encode rules change details: %w", err)
	}
	return writer.AppendTx(ctx, scope, observabilityport.ActivityRecord{
		SubscriptionID: subscriptionID, ManagedAnimeID: managedAnimeID, Kind: "policy_changed", Outcome: "info",
		ReasonCode: "rules_policy_changed", SubjectType: "rules", SubjectID: managedAnimeID, SubjectVersion: newVersion,
		MessageCode: observabilityport.MessageRulesChanged, Details: details,
	})
}

// WriteRSSSourceOffsetPolicyChangedTx appends one business main-line entry for a
// committed RSSSource offset CAS save. The source identity and updated owner
// version keep equal policy changes on different sources distinct and replay-safe.
// groupName names the source's subgroup (empty: not named).
func WriteRSSSourceOffsetPolicyChangedTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, subscriptionID, managedAnimeID, rssSourceID, groupName string, newVersion int64) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	values := map[string]any{}
	params := map[string]any{}
	if name := strings.TrimSpace(groupName); name != "" {
		name = BoundText(name, 256)
		values["group_name"] = name
		params[observabilityport.ParamGroupName] = name
	}
	details, err := MarshalDetails(values)
	if err != nil {
		return fmt.Errorf("encode offset change details: %w", err)
	}
	return writer.AppendTx(ctx, scope, observabilityport.ActivityRecord{
		SubscriptionID: subscriptionID, ManagedAnimeID: managedAnimeID, Kind: "policy_changed", Outcome: "info",
		ReasonCode: "rss_source_offset_changed", SubjectType: "rss_source", SubjectID: rssSourceID, SubjectVersion: newVersion,
		RSSSourceID: rssSourceID, MessageCode: observabilityport.MessageEpisodeOffsetChanged, MessageParams: params, Details: details,
	})
}

// WriteGroupChangedTx appends one business main-line entry when a subgroups
// owner save actually changed the selection set, mode, or score. Callers must
// compare the effective value first; unchanged saves write nothing.
func WriteGroupChangedTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, subscriptionID, managedAnimeID string, newVersion int64) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	return writer.AppendTx(ctx, scope, observabilityport.ActivityRecord{
		SubscriptionID: subscriptionID, ManagedAnimeID: managedAnimeID, Kind: "group_changed", Outcome: "info",
		ReasonCode: "group_changed", SubjectType: "subgroups", SubjectID: managedAnimeID, SubjectVersion: newVersion,
		MessageCode: observabilityport.MessageGroupChanged,
	})
}
