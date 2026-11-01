// Package port defines the narrow cross-context boundary for business plugin
// delivery admission. Owner packages depend on this contract, not on the
// plugin target application implementation.
package port

import (
	"context"
	"encoding/json"
	"errors"
	"path"

	"bansoko/internal/persistence/txscope"
)

const (
	OwnerDownloadTask  = "download_task"
	OwnerFileOperation = "file_operation"
	// OwnerRelease is a release first seen for one subscribed anime; the
	// result key names the anime, so each (anime, release) admits once.
	OwnerRelease = "release"
	// OwnerSubscription is a subscription lifecycle transition; the owner
	// version is the subscription version the transition commits and the
	// result key names the action.
	OwnerSubscription = "subscription"
	// OwnerAcquisition is one acquisition whose file operations all settled.
	OwnerAcquisition = "acquisition"
	// OwnerReplacementDeletion is one deletion obligation of a replacement: an
	// old managed file that had to be removed; the owner version is the
	// obligation version at the exhausted attempt.
	OwnerReplacementDeletion = "replacement_deletion"
	// OwnerReplacementDecision is one settled replace decision; the owner
	// version is the decision version the settle commits.
	OwnerReplacementDecision = "replacement_decision"
	// OwnerPlacement is one managed file another file took over; the owner
	// version is the placement version the displacement stamped.
	OwnerPlacement = "placement"

	TriggerDownloadSubmitted      = "download_submitted"
	TriggerFileOperationCompleted = "file_operation_completed"
	TriggerFileOperationFailed    = "file_operation_failed"
	TriggerReleaseDiscovered      = "release_discovered"
	TriggerDownloadFailed         = "download_failed"
	TriggerSubscriptionChanged    = "subscription_changed"
	TriggerUnknownFilesImported   = "unknown_files_imported"
	// TriggerReplacementDeletionFailed fires when an old file a replacement
	// decided to delete stayed undeletable until the automatic retries ran out.
	TriggerReplacementDeletionFailed = "replacement_deletion_failed"
	// TriggerReplacementApplied fires when a settled replace decision takes
	// over the versions it named.
	TriggerReplacementApplied = "replacement_applied"
	// TriggerFileDisplaced fires when one managed file loses its place to
	// another file.
	TriggerFileDisplaced = "file_displaced"
)

// Triggers is the closed notification trigger registry in catalog order.
var Triggers = []string{
	TriggerDownloadSubmitted, TriggerFileOperationCompleted, TriggerFileOperationFailed,
	TriggerReleaseDiscovered, TriggerDownloadFailed, TriggerSubscriptionChanged, TriggerUnknownFilesImported,
	TriggerReplacementDeletionFailed, TriggerReplacementApplied, TriggerFileDisplaced,
}

// OwnerTypes lists the source owners a business admission may name.
var OwnerTypes = []string{OwnerDownloadTask, OwnerFileOperation, OwnerRelease, OwnerSubscription, OwnerAcquisition,
	OwnerReplacementDeletion, OwnerReplacementDecision, OwnerPlacement}

// IsTrigger reports whether trigger is in the registry.
func IsTrigger(trigger string) bool {
	for _, known := range Triggers {
		if known == trigger {
			return true
		}
	}
	return false
}

// IsOwnerType reports whether owner is an allowlisted business source owner.
func IsOwnerType(owner string) bool {
	for _, known := range OwnerTypes {
		if known == owner {
			return true
		}
	}
	return false
}

// BusinessDeliveryRequest contains only facts already formed by the source
// owner. Payload is a bounded, redacted JSON object; it must not contain
// credentials, URLs, private absolute paths, or raw provider responses.
type BusinessDeliveryRequest struct {
	SourceOwnerType    string
	SourceOwnerID      string
	SourceOwnerVersion int64
	ManagedAnimeID     string
	TriggerKind        string
	Payload            json.RawMessage
	ResultKey          string
}

// Subscription transition actions carried by subscription_changed.
const (
	SubscriptionStarted = "started"
	SubscriptionStopped = "stopped"
	SubscriptionResumed = "resumed"
	SubscriptionRetired = "retired"
)

// SubscriptionChangedRequest builds the subscription_changed admission of one
// lifecycle transition. version is the subscription version the transition
// commits, so each transition admits once. A retired anime is fenced from new
// rows that name it, so its notice carries the anime in the payload only.
func SubscriptionChangedRequest(subscriptionID string, version int64, managedAnimeID, animeTitle, action string) (BusinessDeliveryRequest, error) {
	title := any(nil)
	if animeTitle != "" {
		title = animeTitle
	}
	payload, err := json.Marshal(map[string]any{
		"anime.id": managedAnimeID, "anime.title": title, "event.action": action,
	})
	if err != nil {
		return BusinessDeliveryRequest{}, err
	}
	fenced := managedAnimeID
	if action == SubscriptionRetired {
		fenced = ""
	}
	return BusinessDeliveryRequest{
		SourceOwnerType: OwnerSubscription, SourceOwnerID: subscriptionID, SourceOwnerVersion: version,
		ManagedAnimeID: fenced, TriggerKind: TriggerSubscriptionChanged, Payload: payload, ResultKey: action,
	}, nil
}

// ReplacementDeletionFailedResultKey is the result key of the one notice per
// deletion obligation.
const ReplacementDeletionFailedResultKey = "busy_exhausted"

// ReplacementDeletionFailedRequest builds the replacement_deletion_failed
// admission of one deletion obligation that gave up. fileName is the base name
// only, never a path; reason is already user-facing text; category names why
// (busy, read_only, not_restored or path_occupied) for templates to word.
func ReplacementDeletionFailedRequest(deletionID string, version int64, managedAnimeID, animeTitle, fileName, reason, category string) (BusinessDeliveryRequest, error) {
	title := any(nil)
	if animeTitle != "" {
		title = animeTitle
	}
	payload, err := json.Marshal(map[string]any{
		"anime.id": managedAnimeID, "anime.title": title,
		"file.name": path.Base(fileName), "result.reason": reason,
		"result.category": category,
	})
	if err != nil {
		return BusinessDeliveryRequest{}, err
	}
	return BusinessDeliveryRequest{
		SourceOwnerType: OwnerReplacementDeletion, SourceOwnerID: deletionID, SourceOwnerVersion: version,
		ManagedAnimeID: managedAnimeID, TriggerKind: TriggerReplacementDeletionFailed, Payload: payload,
		ResultKey: ReplacementDeletionFailedResultKey,
	}, nil
}

// ReplacementAppliedResultKey is the result key of the one notice per settled
// replace decision.
const ReplacementAppliedResultKey = "replaced"

// DisplacedTarget names one old version a replacement took over: its file name
// and, when the version records the release it came from, that release's
// subgroup and title. Empty fields are left out of the payload instead of
// travelling as empty facts.
type DisplacedTarget struct{ File, Group, Title string }

// ReplacementAppliedRequest builds the replacement_applied admission of one
// settled replace decision. animeTitle names the anime whose versions changed,
// group and releaseTitle the release that won; replaced names the old versions
// it took over, at least one. File names travel as base names, never paths.
func ReplacementAppliedRequest(decisionID string, version int64, managedAnimeID, animeTitle, group, releaseTitle string, replaced []DisplacedTarget) (BusinessDeliveryRequest, error) {
	if len(replaced) == 0 {
		return BusinessDeliveryRequest{}, errors.New("replacement_applied needs at least one replaced version")
	}
	targets := make([]map[string]any, 0, len(replaced))
	for _, target := range replaced {
		entry := map[string]any{}
		if name := baseName(target.File); name != "" {
			entry["path"] = name
		}
		if target.Group != "" {
			entry["group"] = target.Group
		}
		if target.Title != "" {
			entry["title"] = target.Title
		}
		if len(entry) == 0 {
			continue
		}
		targets = append(targets, entry)
	}
	if len(targets) == 0 {
		return BusinessDeliveryRequest{}, errors.New("replacement_applied needs at least one named replaced version")
	}
	facts := map[string]any{
		"anime.id": managedAnimeID, "anime.title": optionalText(animeTitle),
		"replaced.count": len(targets), "replaced.targets": targets,
	}
	if group != "" {
		facts["anime.group"] = group
	}
	if releaseTitle != "" {
		facts["release.title"] = releaseTitle
	}
	payload, err := json.Marshal(facts)
	if err != nil {
		return BusinessDeliveryRequest{}, err
	}
	return BusinessDeliveryRequest{
		SourceOwnerType: OwnerReplacementDecision, SourceOwnerID: decisionID, SourceOwnerVersion: version,
		ManagedAnimeID: managedAnimeID, TriggerKind: TriggerReplacementApplied, Payload: payload,
		ResultKey: ReplacementAppliedResultKey,
	}, nil
}

// FileDisplacedResultKey is the result key of the one notice per displaced
// managed file.
const FileDisplacedResultKey = "displaced"

// FileDisplacedRequest builds the file_displaced admission of one managed file
// another file took over. victimFileName and takerFile are base names only;
// cause is overwrite or redownload; takerGroup and takerTitle name the release
// behind the displacing file and may be empty when the source recorded none.
func FileDisplacedRequest(victimPlacementID string, version int64, managedAnimeID, animeTitle, victimFileName, cause, takerFile, takerGroup, takerTitle string) (BusinessDeliveryRequest, error) {
	facts := map[string]any{
		"anime.id": managedAnimeID, "anime.title": optionalText(animeTitle),
		"file.name": baseName(victimFileName), "event.cause": cause,
	}
	if name := baseName(takerFile); name != "" {
		facts["taker.file"] = name
	}
	if takerGroup != "" {
		facts["taker.group"] = takerGroup
	}
	if takerTitle != "" {
		facts["taker.title"] = takerTitle
	}
	payload, err := json.Marshal(facts)
	if err != nil {
		return BusinessDeliveryRequest{}, err
	}
	return BusinessDeliveryRequest{
		SourceOwnerType: OwnerPlacement, SourceOwnerID: victimPlacementID, SourceOwnerVersion: version,
		ManagedAnimeID: managedAnimeID, TriggerKind: TriggerFileDisplaced, Payload: payload,
		ResultKey: FileDisplacedResultKey,
	}, nil
}

// optionalText keeps an absent fact absent instead of sending an empty string.
func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// baseName is the file name of a path, empty for an empty path.
func baseName(value string) string {
	if value == "" {
		return ""
	}
	return path.Base(value)
}

// BusinessDeliveryEnqueuer admits matching business deliveries in the caller's
// transaction. Implementations must not begin, commit, or roll back tx.
type BusinessDeliveryEnqueuer interface {
	EnqueueBusinessDelivery(context.Context, txscope.Tx, BusinessDeliveryRequest) error
}
