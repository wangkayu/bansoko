package port

import (
	"sort"
	"strings"

	"bansoko/internal/observability/outbound"
)

// ConditionKind names what kind of target a fault episode is about.
type ConditionKind string

// ConditionVariant names what is wrong with that target.
type ConditionVariant string

// Audience says whether the owner sees the condition as its own row.
type Audience string

// RetryMode says what the product does about a condition on its own.
type RetryMode string

// SettingsTarget names the settings area that fixes a RetryModeSettings condition.
type SettingsTarget string

// AffectedKind names what the affected count counts.
type AffectedKind string

const (
	KindMetadata   ConditionKind = "metadata"
	KindUpdate     ConditionKind = "update"
	KindSite       ConditionKind = "site"
	KindDownloader ConditionKind = "downloader"
	KindLibrary    ConditionKind = "library"
	KindNotify     ConditionKind = "notify"
	KindInternal   ConditionKind = "internal"

	VariantUnreachable     ConditionVariant = "unreachable"
	VariantAuthFailed      ConditionVariant = "auth_failed"
	VariantPaused          ConditionVariant = "paused"
	VariantUnreadable      ConditionVariant = "unreadable"
	VariantRefreshFailed   ConditionVariant = "refresh_failed"
	VariantNotWritable     ConditionVariant = "not_writable"
	VariantNoSpace         ConditionVariant = "no_space"
	VariantSecret          ConditionVariant = "secret"
	VariantTemplate        ConditionVariant = "template"
	VariantRequest         ConditionVariant = "request"
	VariantAddressRejected ConditionVariant = "address_rejected"

	AudienceUser     Audience = "user"
	AudienceInternal Audience = "internal"

	// RetryAuto: the product retries on its own schedule.
	RetryAuto RetryMode = "auto"
	// RetryResume: the paused work continues once the target answers again.
	RetryResume RetryMode = "resume"
	// RetrySettings: the owner has to change a setting first.
	RetrySettings RetryMode = "settings"
	// RetryManual: the owner has to trigger the retry.
	RetryManual RetryMode = "manual"

	SettingsMetadataConnection   SettingsTarget = "metadata_connection"
	SettingsDownloaderConnection SettingsTarget = "downloader_connection"
	SettingsNotificationTarget   SettingsTarget = "notification_target"

	AffectedAnime AffectedKind = "anime"
	AffectedTask  AffectedKind = "task"
)

// Condition is the owner-recognised identity of an episode on its target.
type Condition struct {
	Kind    ConditionKind
	Variant ConditionVariant
}

// ConditionSpec is the catalog entry of one condition.
type ConditionSpec struct {
	Condition
	Audience Audience
	Retry    RetryMode
	Settings SettingsTarget
	Affected AffectedKind
}

// Internal condition sources: the closed set of identities outside the
// owner-facing catalog. Each keeps one episode keyed by the source label.
const (
	InternalAutomationDispatch = "automation.dispatch"
	InternalReplacementTrigger = "replacement.trigger"
	InternalPlacementSink      = "placement.sink"
	InternalRetentionSweep     = "retention.sweep"
	InternalActivityConsume    = "activity.consume"
	InternalHTTP5xx            = "http.5xx"
	InternalDownloadSubmit     = "download.submit"
	InternalFileOperation      = "fileoperation.execute"
	InternalRSSRefresh         = "rss.refresh"
)

var internalSources = map[string]struct{}{
	InternalAutomationDispatch: {}, InternalReplacementTrigger: {}, InternalPlacementSink: {},
	InternalRetentionSweep: {}, InternalActivityConsume: {}, InternalHTTP5xx: {},
	InternalDownloadSubmit: {}, InternalFileOperation: {}, InternalRSSRefresh: {},
}

// IsInternalSource reports whether source is a catalogued internal label.
func IsInternalSource(source string) bool {
	_, ok := internalSources[source]
	return ok
}

// InternalSources returns the internal source labels in stable order.
func InternalSources() []string {
	out := make([]string, 0, len(internalSources))
	for source := range internalSources {
		out = append(out, source)
	}
	sort.Strings(out)
	return out
}

// Retry modes below were checked against the code that retries:
//   - metadata.unreachable: every later match attempt calls the source again.
//   - update.unreachable: only the owner's "check for updates" request calls
//     the update source; nothing polls it in the background.
//   - site.*: provider circuit and subscription backoff (rss refresh).
//   - downloader.unreachable: the submit and sync work items stay retryable
//     and continue when the connection answers.
//   - library.*: the failed placement is terminal; the owner retries 入库.
//   - notify.unreachable: the delivery is retried by the delivery worker.
var conditionCatalog = []ConditionSpec{
	{Condition{KindMetadata, VariantUnreachable}, AudienceUser, RetryAuto, "", AffectedAnime},
	{Condition{KindMetadata, VariantAuthFailed}, AudienceUser, RetrySettings, SettingsMetadataConnection, AffectedAnime},
	{Condition{KindUpdate, VariantUnreachable}, AudienceUser, RetryManual, "", ""},
	{Condition{KindSite, VariantPaused}, AudienceUser, RetryAuto, "", ""},
	{Condition{KindSite, VariantUnreadable}, AudienceUser, RetryAuto, "", ""},
	{Condition{KindSite, VariantRefreshFailed}, AudienceUser, RetryAuto, "", AffectedAnime},
	{Condition{KindDownloader, VariantUnreachable}, AudienceUser, RetryResume, "", AffectedTask},
	{Condition{KindDownloader, VariantAuthFailed}, AudienceUser, RetrySettings, SettingsDownloaderConnection, AffectedTask},
	{Condition{KindLibrary, VariantNotWritable}, AudienceUser, RetryManual, "", AffectedTask},
	{Condition{KindLibrary, VariantUnreachable}, AudienceUser, RetryManual, "", AffectedTask},
	{Condition{KindLibrary, VariantNoSpace}, AudienceUser, RetryManual, "", AffectedTask},
	{Condition{KindNotify, VariantUnreachable}, AudienceUser, RetryAuto, "", AffectedTask},
	{Condition{KindNotify, VariantAddressRejected}, AudienceUser, RetrySettings, SettingsNotificationTarget, AffectedTask},
	{Condition{KindNotify, VariantSecret}, AudienceUser, RetrySettings, SettingsNotificationTarget, AffectedTask},
	{Condition{KindNotify, VariantTemplate}, AudienceUser, RetrySettings, SettingsNotificationTarget, AffectedTask},
	{Condition{KindNotify, VariantRequest}, AudienceUser, RetrySettings, SettingsNotificationTarget, AffectedTask},
	{Condition{KindInternal, ""}, AudienceInternal, "", "", ""},
}

// ConditionCatalog returns the complete condition catalog in stable order.
func ConditionCatalog() []ConditionSpec {
	return append([]ConditionSpec(nil), conditionCatalog...)
}

// SpecOf returns the catalog entry of a condition.
func SpecOf(c Condition) (ConditionSpec, bool) {
	for _, spec := range conditionCatalog {
		if spec.Condition == c {
			return spec, true
		}
	}
	return ConditionSpec{}, false
}

// DiagnosticFailureRequest records one failure of a condition on a target.
// For KindInternal the TargetID is a catalogued internal source and the
// request carries the opaque request id; every other condition carries the
// outbound facts of the failing call.
type DiagnosticFailureRequest struct {
	Condition  Condition
	Severity   DiagnosticSeverity
	TargetID   string
	TargetName string
	// Failure is the structured outbound fact; zero when no call was made
	// (for example a library path that is not writable).
	Failure outbound.Failure
	// AffectedID is the anime or task this failure kept from working.
	AffectedID string
	// RequestID is the opaque id of the failing request, internal only.
	RequestID string
}

// DiagnosticSuccessRequest closes the open episode on one target.
type DiagnosticSuccessRequest struct {
	Kind     ConditionKind
	TargetID string
}

// InternalFailure builds the request of an internal-source failure.
func InternalFailure(source, requestID string) DiagnosticFailureRequest {
	return DiagnosticFailureRequest{
		Condition: Condition{Kind: KindInternal}, Severity: SeverityError,
		TargetID: source, RequestID: requestID,
	}
}

// InternalSuccess builds the request closing an internal source's episode.
func InternalSuccess(source string) DiagnosticSuccessRequest {
	return DiagnosticSuccessRequest{Kind: KindInternal, TargetID: source}
}

// Validate checks a failure request against the catalog. It does not
// touch the database.
func (r DiagnosticFailureRequest) Validate() bool {
	spec, ok := SpecOf(r.Condition)
	if !ok || !IsDiagnosticSeverity(r.Severity) {
		return false
	}
	target := strings.TrimSpace(r.TargetID)
	if target == "" || len(target) > 128 {
		return false
	}
	if spec.Audience == AudienceInternal {
		return IsInternalSource(target) && (r.RequestID == "" || IsSafeRequestID(r.RequestID))
	}
	return r.RequestID == ""
}

// SiteMikanParser is the site target of Mikan page-parsing faults (season
// catalog and group lists). It is separate from the "mikan" feed target so a
// successful RSS refresh does not close a persistent parser fault.
const SiteMikanParser = "mikan:parser"
