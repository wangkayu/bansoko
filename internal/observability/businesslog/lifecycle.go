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
func WriteSubscriptionStateTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, subscriptionID, managedAnimeID, title string, transition SubscriptionTransition, newVersion int64) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	var kind, summary string
	switch transition {
	case SubscriptionTransitionStarted:
		kind, summary = "subscription_started", "订阅已开始"
	case SubscriptionTransitionStopped:
		kind, summary = "subscription_stopped", "订阅已停止"
	case SubscriptionTransitionResumed:
		kind, summary = "subscription_resumed", "订阅已恢复"
	default:
		return fmt.Errorf("unknown subscription transition %q", transition)
	}
	reason := kind
	return writer.AppendTx(ctx, scope, observabilityport.ActivityRecord{
		SubscriptionID: subscriptionID, ManagedAnimeID: managedAnimeID, Kind: string(kind), Outcome: "info",
		ReasonCode: reason, SubjectType: "subscription", SubjectID: subscriptionID, SubjectVersion: newVersion,
		Title: title, Summary: summary,
	})
}

// RulesSectionSummaryNames are the product names of the rules sections a
// policy_changed summary lists (the same names the rules tab uses).
var RulesSectionSummaryNames = map[string]string{
	"parser": "解析", "release_rules": "标题过滤", "file_rules": "文件筛选",
	"naming_rules": "命名", "file_effect": "入库", "tag_filters": "标签过滤",
}

// WriteRulesPolicyChangedTx appends one business main-line entry for a committed
// rules owner CAS save that changed at least one section. It uses the rules
// owner version as the replay identity; changedSections (rules section names,
// in section order) say what changed and are frozen into the details.
func WriteRulesPolicyChangedTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, subscriptionID, managedAnimeID, title string, newVersion int64, changedSections []string) error {
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
		Title: title, Summary: RulesChangedSummary(changedSections), Details: details,
	})
}

// RulesChangedSummary is the stored summary of a rules change: the changed
// sections by their display name (see RulesSectionSummaryNames), or the
// generic text when none is known.
func RulesChangedSummary(changedSections []string) string {
	names := make([]string, 0, len(changedSections))
	for _, section := range changedSections {
		if name, ok := RulesSectionSummaryNames[section]; ok {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "规则已更新"
	}
	return "规则：" + strings.Join(names, "、")
}

// WriteRSSSourceOffsetPolicyChangedTx appends one business main-line entry for a
// committed RSSSource offset CAS save. The source identity and updated owner
// version keep equal policy changes on different sources distinct and replay-safe.
// groupName names the source's subgroup (empty: not named).
func WriteRSSSourceOffsetPolicyChangedTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, subscriptionID, managedAnimeID, rssSourceID, groupName, title string, newVersion int64) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	summary := "集数偏移已更新"
	values := map[string]any{}
	if name := strings.TrimSpace(groupName); name != "" {
		name = BoundText(name, 256)
		values["group_name"] = name
		summary = name + " · " + summary
	}
	details, err := MarshalDetails(values)
	if err != nil {
		return fmt.Errorf("encode offset change details: %w", err)
	}
	return writer.AppendTx(ctx, scope, observabilityport.ActivityRecord{
		SubscriptionID: subscriptionID, ManagedAnimeID: managedAnimeID, Kind: "policy_changed", Outcome: "info",
		ReasonCode: "rss_source_offset_changed", SubjectType: "rss_source", SubjectID: rssSourceID, SubjectVersion: newVersion,
		RSSSourceID: rssSourceID, Title: title, Summary: summary, Details: details,
	})
}

// WriteGroupChangedTx appends one business main-line entry when a subgroups
// owner save actually changed the selection set, mode, or score. Callers must
// compare the effective value first; unchanged saves write nothing.
func WriteGroupChangedTx(ctx context.Context, scope txscope.Tx, writer observabilityport.ActivityWriter, subscriptionID, managedAnimeID, title string, newVersion int64) error {
	if writer == nil {
		return fmt.Errorf("activity writer is nil")
	}
	return writer.AppendTx(ctx, scope, observabilityport.ActivityRecord{
		SubscriptionID: subscriptionID, ManagedAnimeID: managedAnimeID, Kind: "group_changed", Outcome: "info",
		ReasonCode: "group_changed", SubjectType: "subgroups", SubjectID: managedAnimeID, SubjectVersion: newVersion,
		Title: title, Summary: "字幕组选择已更新",
	})
}
