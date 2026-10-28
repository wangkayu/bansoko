package port

import (
	"fmt"
	"sort"
)

// The business journal stores no display text. Each entry names one message
// of a closed catalog (message_code, registered under exactly one kind) plus
// the scalar values the message interpolates (message_params); the frontend
// renders it from its zh-CN / en catalogs. The catalog below is the single
// source: the writer accepts only these codes with exactly these params,
// scripts/ci/generate-observability-catalog.sh publishes it to the frontend
// (MESSAGE_CODES / MESSAGE_SPECS) and api/openapi.yaml mirrors it
// (SubscriptionLogMessageCode / SubscriptionLogMessageParams).

// ActivityKind is the closed set of business journal entry kinds.
type ActivityKind string

const (
	ActivityKindSubscriptionCreated    ActivityKind = "subscription_created"
	ActivityKindSubscriptionStarted    ActivityKind = "subscription_started"
	ActivityKindSubscriptionStopped    ActivityKind = "subscription_stopped"
	ActivityKindSubscriptionResumed    ActivityKind = "subscription_resumed"
	ActivityKindMetadataApplied        ActivityKind = "metadata_applied"
	ActivityKindGroupChanged           ActivityKind = "group_changed"
	ActivityKindPolicyChanged          ActivityKind = "policy_changed"
	ActivityKindRSSNewContent          ActivityKind = "rss_new_content"
	ActivityKindReleaseProcessed       ActivityKind = "release_processed"
	ActivityKindDownloadChanged        ActivityKind = "download_changed"
	ActivityKindArtifactVerified       ActivityKind = "artifact_verified"
	ActivityKindPlacementChanged       ActivityKind = "placement_changed"
	ActivityKindReplacementDecided     ActivityKind = "replacement_decided"
	ActivityKindCleanupCompleted       ActivityKind = "cleanup_completed"
	ActivityKindCleanupFailed          ActivityKind = "cleanup_failed"
	ActivityKindFileAlignmentCompleted ActivityKind = "file_alignment_completed"
	ActivityKindFileAlignmentFailed    ActivityKind = "file_alignment_failed"
	ActivityKindAcquisitionSettled     ActivityKind = "acquisition_settled"
)

// MessageCode is one message of the closed business journal catalog. Codes
// are unique across kinds, so the frontend keys its text by code alone.
type MessageCode string

const (
	MessageSubscriptionCreated MessageCode = "subscription_created"
	MessageSubscriptionStarted MessageCode = "subscription_started"
	MessageSubscriptionStopped MessageCode = "subscription_stopped"
	MessageSubscriptionResumed MessageCode = "subscription_resumed"
	MessageMetadataApplied     MessageCode = "metadata_applied"
	MessageGroupChanged        MessageCode = "group_changed"
	// MessageRulesChanged lists the changed sections in details.changed_sections.
	MessageRulesChanged          MessageCode = "rules_changed"
	MessageEpisodeOffsetChanged  MessageCode = "episode_offset_changed"
	MessageRSSNewContent         MessageCode = "rss_new_content"
	MessageReleaseDiscovered     MessageCode = "release_discovered"
	MessageReleaseWaitingMeta    MessageCode = "release_waiting_metadata"
	MessageReleaseSubStopped     MessageCode = "release_subscription_stopped"
	MessageReleaseSourceDown     MessageCode = "release_source_unavailable"
	MessageReleaseParseFailed    MessageCode = "release_parse_failed"
	MessageReleaseAccepted       MessageCode = "release_accepted"
	MessageReleaseFiltered       MessageCode = "release_filtered"
	MessageReleaseAdopted        MessageCode = "release_adopted_existing"
	MessageReleaseAcquireFailed  MessageCode = "release_acquisition_failed"
	MessageReleaseSubmitted      MessageCode = "release_submitted"
	MessageReleaseAwaitingCompet MessageCode = "release_awaiting_competition"
	MessageReleaseResumeBaseline MessageCode = "release_resume_baseline"
	MessageReleaseStartBaseline  MessageCode = "release_start_baseline"
	MessageDownloadSubmitted     MessageCode = "download_submitted"
	MessageDownloadCompleted     MessageCode = "download_completed"
	// MessageDownloadRemoved is a task deleted in the downloader (cancelled,
	// not a failure).
	MessageDownloadRemoved  MessageCode = "download_removed"
	MessageDownloadFailed   MessageCode = "download_failed"
	MessageArtifactVerified MessageCode = "artifact_verified"
	MessageFileImported     MessageCode = "file_imported"
	MessageFileAdopted      MessageCode = "file_adopted"
	MessageFileRetained     MessageCode = "file_retained"
	MessageFileImportFailed MessageCode = "file_import_failed"
	MessageAcquisitionDone  MessageCode = "acquisition_succeeded"
	MessageAcquisitionFail  MessageCode = "acquisition_failed"
	MessageAcquisitionStop  MessageCode = "acquisition_cancelled"
	// MessageReplacementKeptExisting is a keep/retain verdict: an existing
	// version stays and the candidate does not replace it.
	MessageReplacementKeptExisting MessageCode = "replacement_kept_existing"
	// MessageReplacementKeptAll is a queue verdict: every version stays.
	MessageReplacementKeptAll MessageCode = "replacement_kept_all"
	// MessageReplacementBlocked is a blocked verdict: no comparison possible.
	MessageReplacementBlocked MessageCode = "replacement_blocked"
	// MessageReplacementReplaced is written once, when a replace decision
	// settles and the old versions made way.
	MessageReplacementReplaced MessageCode = "replacement_replaced"
	// MessageReplacementNotReplaced is written once per replace decision that
	// will never take effect; details.reason_code says why.
	MessageReplacementNotReplaced MessageCode = "replacement_not_replaced"
	MessagePlacementReleased      MessageCode = "placement_released"
	MessageUnknownFileReleased    MessageCode = "unknown_file_released"
	MessagePlacementRecorded      MessageCode = "placement_recorded"
	MessageFileRecovered          MessageCode = "file_recovered"
	// MessagePlacementsExcluded is written once per instance by the seasonless
	// key rewrite (migration 096): count files of another season stay on disk
	// and listed but no longer take part in replacement or coverage.
	MessagePlacementsExcluded MessageCode = "placements_excluded"
	// MessageOffsetAligned is written once per offset change of a link that
	// re-keyed imported files: how many moved to another episode, and what
	// retention and the collision policy did with them.
	MessageOffsetAligned     MessageCode = "offset_aligned"
	MessageOldVersionDeleted MessageCode = "old_version_deleted"
	// MessageOldVersionKept is a deletion the identity check skipped; the
	// reason is the entry's reason_code.
	MessageOldVersionKept MessageCode = "old_version_kept"
	// MessageOldVersionDeleteFailed is a deletion that gave up; the reason is
	// the entry's reason_code.
	MessageOldVersionDeleteFailed MessageCode = "old_version_delete_failed"
)

// MessageParamType is the scalar type of one message param.
type MessageParamType string

const (
	MessageParamString  MessageParamType = "string"
	MessageParamInteger MessageParamType = "integer"
)

// MaxMessageParamBytes bounds one string param; the writer redacts and cuts a
// longer value instead of refusing the entry.
const MaxMessageParamBytes = 256

// Message param names. A string param that is empty is omitted, so an
// optional param is simply absent.
const (
	// ParamName is the torrent name the downloader reports.
	ParamName = "name"
	// ParamFileName is the base name of the file a file result is about.
	ParamFileName = "file_name"
	// ParamGroupName is the subgroup display name.
	ParamGroupName = "group_name"
	// ParamCount is a non-negative item count.
	ParamCount = "count"
	// The offset_aligned counts: files re-keyed to another episode, deleted
	// after the retention comparison, given a version suffix, overwriting a
	// file, and left in place by the skip policy.
	ParamRekeyed     = "rekeyed"
	ParamDeleted     = "deleted"
	ParamVersioned   = "versioned"
	ParamOverwritten = "overwritten"
	ParamSkipped     = "skipped"
)

// MessageParam declares one param of a message.
type MessageParam struct {
	Name     string
	Type     MessageParamType
	Required bool
}

// MessageSpec is one registered message: its code, the one kind it belongs to
// and the params it interpolates.
type MessageSpec struct {
	Code   MessageCode
	Kind   ActivityKind
	Params []MessageParam
}

var (
	optionalFileName = []MessageParam{{Name: ParamFileName, Type: MessageParamString}}
	messageSpecs     = []MessageSpec{
		{Code: MessageSubscriptionCreated, Kind: ActivityKindSubscriptionCreated},
		{Code: MessageSubscriptionStarted, Kind: ActivityKindSubscriptionStarted},
		{Code: MessageSubscriptionStopped, Kind: ActivityKindSubscriptionStopped},
		{Code: MessageSubscriptionResumed, Kind: ActivityKindSubscriptionResumed},
		{Code: MessageMetadataApplied, Kind: ActivityKindMetadataApplied},
		{Code: MessageGroupChanged, Kind: ActivityKindGroupChanged},
		{Code: MessageRulesChanged, Kind: ActivityKindPolicyChanged},
		{Code: MessageEpisodeOffsetChanged, Kind: ActivityKindPolicyChanged, Params: []MessageParam{{Name: ParamGroupName, Type: MessageParamString}}},
		{Code: MessageRSSNewContent, Kind: ActivityKindRSSNewContent, Params: []MessageParam{{Name: ParamCount, Type: MessageParamInteger, Required: true}}},
		{Code: MessageReleaseDiscovered, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseWaitingMeta, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseSubStopped, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseSourceDown, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseParseFailed, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseAccepted, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseFiltered, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseAdopted, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseAcquireFailed, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseSubmitted, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseAwaitingCompet, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseResumeBaseline, Kind: ActivityKindReleaseProcessed},
		{Code: MessageReleaseStartBaseline, Kind: ActivityKindReleaseProcessed},
		{Code: MessageDownloadSubmitted, Kind: ActivityKindDownloadChanged, Params: []MessageParam{{Name: ParamName, Type: MessageParamString}}},
		{Code: MessageDownloadCompleted, Kind: ActivityKindDownloadChanged},
		{Code: MessageDownloadRemoved, Kind: ActivityKindDownloadChanged},
		{Code: MessageDownloadFailed, Kind: ActivityKindDownloadChanged},
		{Code: MessageArtifactVerified, Kind: ActivityKindArtifactVerified},
		{Code: MessageFileImported, Kind: ActivityKindFileAlignmentCompleted, Params: optionalFileName},
		{Code: MessageFileAdopted, Kind: ActivityKindFileAlignmentCompleted, Params: optionalFileName},
		{Code: MessageFileRetained, Kind: ActivityKindFileAlignmentCompleted, Params: optionalFileName},
		{Code: MessageFileImportFailed, Kind: ActivityKindFileAlignmentFailed, Params: optionalFileName},
		{Code: MessageAcquisitionDone, Kind: ActivityKindAcquisitionSettled},
		{Code: MessageAcquisitionFail, Kind: ActivityKindAcquisitionSettled},
		{Code: MessageAcquisitionStop, Kind: ActivityKindAcquisitionSettled},
		{Code: MessageReplacementKeptExisting, Kind: ActivityKindReplacementDecided},
		{Code: MessageReplacementKeptAll, Kind: ActivityKindReplacementDecided},
		{Code: MessageReplacementBlocked, Kind: ActivityKindReplacementDecided},
		{Code: MessageReplacementReplaced, Kind: ActivityKindReplacementDecided},
		{Code: MessageReplacementNotReplaced, Kind: ActivityKindReplacementDecided},
		{Code: MessagePlacementReleased, Kind: ActivityKindPlacementChanged},
		{Code: MessageUnknownFileReleased, Kind: ActivityKindPlacementChanged},
		{Code: MessagePlacementRecorded, Kind: ActivityKindPlacementChanged},
		{Code: MessageFileRecovered, Kind: ActivityKindPlacementChanged},
		{Code: MessagePlacementsExcluded, Kind: ActivityKindPlacementChanged, Params: []MessageParam{{Name: ParamCount, Type: MessageParamInteger, Required: true}}},
		{Code: MessageOffsetAligned, Kind: ActivityKindPlacementChanged, Params: []MessageParam{
			{Name: ParamGroupName, Type: MessageParamString, Required: true},
			{Name: ParamRekeyed, Type: MessageParamInteger, Required: true},
			{Name: ParamDeleted, Type: MessageParamInteger, Required: true},
			{Name: ParamVersioned, Type: MessageParamInteger, Required: true},
			{Name: ParamOverwritten, Type: MessageParamInteger, Required: true},
			{Name: ParamSkipped, Type: MessageParamInteger, Required: true},
		}},
		{Code: MessageOldVersionDeleted, Kind: ActivityKindCleanupCompleted},
		{Code: MessageOldVersionKept, Kind: ActivityKindCleanupCompleted},
		{Code: MessageOldVersionDeleteFailed, Kind: ActivityKindCleanupFailed},
	}
	messageSpecIndex = func() map[MessageCode]MessageSpec {
		index := make(map[MessageCode]MessageSpec, len(messageSpecs))
		for _, spec := range messageSpecs {
			index[spec.Code] = spec
		}
		return index
	}()
	activityKindIndex = func() map[ActivityKind]struct{} {
		index := map[ActivityKind]struct{}{}
		for _, spec := range messageSpecs {
			index[spec.Kind] = struct{}{}
		}
		return index
	}()
)

// MessageSpecs returns the catalog in registration order.
func MessageSpecs() []MessageSpec {
	out := make([]MessageSpec, len(messageSpecs))
	for i, spec := range messageSpecs {
		out[i] = spec
		out[i].Params = append([]MessageParam(nil), spec.Params...)
	}
	return out
}

// LookupMessage returns the registered spec of code.
func LookupMessage(code MessageCode) (MessageSpec, bool) {
	spec, ok := messageSpecIndex[code]
	return spec, ok
}

// ActivityKinds returns every kind that has at least one message, sorted.
func ActivityKinds() []ActivityKind {
	kinds := make([]ActivityKind, 0, len(activityKindIndex))
	for kind := range activityKindIndex {
		kinds = append(kinds, kind)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
	return kinds
}

// IsActivityKind reports whether kind is a registered business journal kind.
func IsActivityKind(kind string) bool {
	_, ok := activityKindIndex[ActivityKind(kind)]
	return ok
}

// ReleaseVerdictMessage is the release_processed message of one evaluation
// verdict. The mapping covers exactly the verdicts releases_verdict_check
// admits; any other verdict is an error, which fails the evaluation entry
// instead of writing a message no reader has copy for.
func ReleaseVerdictMessage(verdict string) (MessageCode, error) {
	switch verdict {
	case "discovered":
		return MessageReleaseDiscovered, nil
	case "waiting_metadata":
		return MessageReleaseWaitingMeta, nil
	case "subscription_stopped":
		return MessageReleaseSubStopped, nil
	case "source_unavailable":
		return MessageReleaseSourceDown, nil
	case "parse_failed":
		return MessageReleaseParseFailed, nil
	case "accepted_pending_download":
		return MessageReleaseAccepted, nil
	case "filtered":
		return MessageReleaseFiltered, nil
	case "adopted_existing":
		return MessageReleaseAdopted, nil
	case "acquisition_failed":
		return MessageReleaseAcquireFailed, nil
	case "submitted":
		return MessageReleaseSubmitted, nil
	case "awaiting_competition":
		return MessageReleaseAwaitingCompet, nil
	case "resume_baseline":
		return MessageReleaseResumeBaseline, nil
	case "start_baseline":
		return MessageReleaseStartBaseline, nil
	}
	return "", fmt.Errorf("release verdict %q has no release_processed message", verdict)
}
