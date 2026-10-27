// Package port defines the narrow cross-context boundary for business plugin
// delivery admission. Owner packages depend on this contract, not on the
// plugin target application implementation.
package port

import (
	"context"
	"encoding/json"
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
)

// Triggers is the closed notification trigger registry in catalog order.
var Triggers = []string{
	TriggerDownloadSubmitted, TriggerFileOperationCompleted, TriggerFileOperationFailed,
	TriggerReleaseDiscovered, TriggerDownloadFailed, TriggerSubscriptionChanged, TriggerUnknownFilesImported,
	TriggerReplacementDeletionFailed,
}

// OwnerTypes lists the source owners a business admission may name.
var OwnerTypes = []string{OwnerDownloadTask, OwnerFileOperation, OwnerRelease, OwnerSubscription, OwnerAcquisition, OwnerReplacementDeletion}

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

// BusinessDeliveryEnqueuer admits matching business deliveries in the caller's
// transaction. Implementations must not begin, commit, or roll back tx.
type BusinessDeliveryEnqueuer interface {
	EnqueueBusinessDelivery(context.Context, txscope.Tx, BusinessDeliveryRequest) error
}
