// Package port defines the narrow typed boundary for durable observability writes.
package port

import (
	"context"
	"encoding/json"
	"time"

	"bansoko/internal/persistence/txscope"
)

// Stable identity types make recorder requests depend on the exported catalog,
// rather than independently maintained free-string allowlists.
type RoutineKind string
type RoutineScopeKind string
type RoutineOutcome string
type RoutineResultCode string
type DiagnosticSeverity string

const (
	RoutineCatalogRefresh                 RoutineKind        = "catalog_refresh"
	RoutineProviderRefresh                RoutineKind        = "provider_refresh"
	RoutineRSSRefresh                     RoutineKind        = "rss_refresh"
	RoutineMetadataMatch                  RoutineKind        = "metadata_match"
	RoutineAutomationDispatch             RoutineKind        = "automation_dispatch"
	RoutineRetentionPass                  RoutineKind        = "retention_pass"
	RoutineFileManagerOperation           RoutineKind        = "file_manager_operation"
	RoutineScopeGlobal                    RoutineScopeKind   = "global"
	RoutineScopeAutomation                RoutineScopeKind   = "automation"
	RoutineScopeProvider                  RoutineScopeKind   = "provider"
	RoutineScopeSubscription              RoutineScopeKind   = "subscription"
	RoutineScopeRSSSource                 RoutineScopeKind   = "rss_source"
	RoutineScopeComponent                 RoutineScopeKind   = "component"
	RoutineOutcomeSuccess                 RoutineOutcome     = "success"
	RoutineOutcomeWarning                 RoutineOutcome     = "warning"
	RoutineOutcomeFailure                 RoutineOutcome     = "failure"
	RoutineResultCatalogRefreshCompleted  RoutineResultCode  = "catalog_refresh_completed"
	RoutineResultCatalogRefreshFailed     RoutineResultCode  = "catalog_refresh_failed"
	RoutineResultProviderRefreshCompleted RoutineResultCode  = "provider_refresh_completed"
	RoutineResultProviderRefreshFailed    RoutineResultCode  = "provider_refresh_failed"
	RoutineResultProviderParserWarning    RoutineResultCode  = "provider_parser_warning"
	RoutineResultRSSRefreshSucceeded      RoutineResultCode  = "refresh_succeeded"
	RoutineResultRSSRefreshFailed         RoutineResultCode  = "refresh_failed"
	RoutineResultMetadataPending          RoutineResultCode  = "metadata_match_pending"
	RoutineResultMetadataCompleted        RoutineResultCode  = "metadata_match_completed"
	RoutineResultMetadataPartial          RoutineResultCode  = "metadata_match_partial"
	RoutineResultMetadataNoResult         RoutineResultCode  = "metadata_match_no_result"
	RoutineResultMetadataConfigError      RoutineResultCode  = "metadata_match_config_error"
	RoutineResultMetadataProviderError    RoutineResultCode  = "metadata_match_provider_error"
	RoutineResultDispatchCompleted        RoutineResultCode  = "dispatch_completed"
	RoutineResultDispatchPartial          RoutineResultCode  = "dispatch_partial"
	RoutineResultDispatchFailed           RoutineResultCode  = "dispatch_failed"
	RoutineResultRetentionPassCompleted   RoutineResultCode  = "retention_pass_completed"
	RoutineResultRetentionPassFailed      RoutineResultCode  = "retention_pass_failed"
	RoutineResultFileManagerCompleted     RoutineResultCode  = "file_manager_operation_completed"
	RoutineResultFileManagerFailed        RoutineResultCode  = "file_manager_operation_failed"
	SeverityWarning                       DiagnosticSeverity = "warning"
	SeverityError                         DiagnosticSeverity = "error"
)

var (
	routineKinds    = map[RoutineKind]struct{}{RoutineCatalogRefresh: {}, RoutineProviderRefresh: {}, RoutineRSSRefresh: {}, RoutineMetadataMatch: {}, RoutineAutomationDispatch: {}, RoutineRetentionPass: {}, RoutineFileManagerOperation: {}}
	routineScopes   = map[RoutineScopeKind]struct{}{RoutineScopeGlobal: {}, RoutineScopeAutomation: {}, RoutineScopeProvider: {}, RoutineScopeSubscription: {}, RoutineScopeRSSSource: {}, RoutineScopeComponent: {}}
	routineOutcomes = map[RoutineOutcome]struct{}{RoutineOutcomeSuccess: {}, RoutineOutcomeWarning: {}, RoutineOutcomeFailure: {}}
	routineCodes    = map[RoutineKind]map[RoutineResultCode]struct{}{
		RoutineCatalogRefresh:       {RoutineResultCatalogRefreshCompleted: {}, RoutineResultCatalogRefreshFailed: {}},
		RoutineProviderRefresh:      {RoutineResultProviderRefreshCompleted: {}, RoutineResultProviderRefreshFailed: {}, RoutineResultProviderParserWarning: {}},
		RoutineRSSRefresh:           {RoutineResultRSSRefreshSucceeded: {}, RoutineResultRSSRefreshFailed: {}},
		RoutineMetadataMatch:        {RoutineResultMetadataPending: {}, RoutineResultMetadataCompleted: {}, RoutineResultMetadataPartial: {}, RoutineResultMetadataNoResult: {}, RoutineResultMetadataConfigError: {}, RoutineResultMetadataProviderError: {}},
		RoutineAutomationDispatch:   {RoutineResultDispatchCompleted: {}, RoutineResultDispatchPartial: {}, RoutineResultDispatchFailed: {}},
		RoutineRetentionPass:        {RoutineResultRetentionPassCompleted: {}, RoutineResultRetentionPassFailed: {}},
		RoutineFileManagerOperation: {RoutineResultFileManagerCompleted: {}, RoutineResultFileManagerFailed: {}},
	}
	diagnosticSeverities = map[DiagnosticSeverity]struct{}{SeverityWarning: {}, SeverityError: {}}
)

func IsRoutineKind(value RoutineKind) bool           { _, ok := routineKinds[value]; return ok }
func IsRoutineScopeKind(value RoutineScopeKind) bool { _, ok := routineScopes[value]; return ok }
func IsRoutineOutcome(value RoutineOutcome) bool     { _, ok := routineOutcomes[value]; return ok }
func IsRoutineResultCode(kind RoutineKind, code RoutineResultCode) bool {
	_, ok := routineCodes[kind][code]
	return ok
}
func IsDiagnosticSeverity(value DiagnosticSeverity) bool {
	_, ok := diagnosticSeverities[value]
	return ok
}

// RoutineIdentities returns every persisted routine kind/result identity.
func RoutineIdentities() []RoutineIdentity {
	return []RoutineIdentity{
		{RoutineCatalogRefresh, RoutineScopeGlobal, RoutineOutcomeSuccess, RoutineResultCatalogRefreshCompleted},
		{RoutineCatalogRefresh, RoutineScopeGlobal, RoutineOutcomeFailure, RoutineResultCatalogRefreshFailed},
		{RoutineProviderRefresh, RoutineScopeProvider, RoutineOutcomeSuccess, RoutineResultProviderRefreshCompleted},
		{RoutineProviderRefresh, RoutineScopeProvider, RoutineOutcomeFailure, RoutineResultProviderRefreshFailed},
		{RoutineProviderRefresh, RoutineScopeProvider, RoutineOutcomeWarning, RoutineResultProviderParserWarning},
		{RoutineRSSRefresh, RoutineScopeRSSSource, RoutineOutcomeSuccess, RoutineResultRSSRefreshSucceeded},
		{RoutineRSSRefresh, RoutineScopeRSSSource, RoutineOutcomeFailure, RoutineResultRSSRefreshFailed},
		{RoutineMetadataMatch, RoutineScopeSubscription, RoutineOutcomeWarning, RoutineResultMetadataPending},
		{RoutineMetadataMatch, RoutineScopeSubscription, RoutineOutcomeSuccess, RoutineResultMetadataCompleted},
		{RoutineMetadataMatch, RoutineScopeSubscription, RoutineOutcomeWarning, RoutineResultMetadataPartial},
		{RoutineMetadataMatch, RoutineScopeSubscription, RoutineOutcomeFailure, RoutineResultMetadataNoResult},
		{RoutineMetadataMatch, RoutineScopeSubscription, RoutineOutcomeFailure, RoutineResultMetadataConfigError},
		{RoutineMetadataMatch, RoutineScopeSubscription, RoutineOutcomeFailure, RoutineResultMetadataProviderError},
		{RoutineAutomationDispatch, RoutineScopeAutomation, RoutineOutcomeSuccess, RoutineResultDispatchCompleted},
		{RoutineAutomationDispatch, RoutineScopeAutomation, RoutineOutcomeWarning, RoutineResultDispatchPartial},
		{RoutineAutomationDispatch, RoutineScopeAutomation, RoutineOutcomeFailure, RoutineResultDispatchFailed},
		RoutineRetentionPassIdentity(RoutineOutcomeSuccess, RoutineResultRetentionPassCompleted),
		RoutineRetentionPassIdentity(RoutineOutcomeFailure, RoutineResultRetentionPassFailed),
		FileManagerOperationRoutine(RoutineOutcomeSuccess, RoutineResultFileManagerCompleted),
		FileManagerOperationRoutine(RoutineOutcomeFailure, RoutineResultFileManagerFailed),
	}
}

// RoutineIdentity is the catalogued portion of a routine request.
type RoutineIdentity struct {
	Kind       RoutineKind
	ScopeKind  RoutineScopeKind
	Outcome    RoutineOutcome
	ResultCode RoutineResultCode
}

func AutomationDispatchRoutine(outcome RoutineOutcome, code RoutineResultCode) RoutineIdentity {
	return RoutineIdentity{RoutineAutomationDispatch, RoutineScopeAutomation, outcome, code}
}
func ProviderRefreshRoutine(outcome RoutineOutcome, code RoutineResultCode) RoutineIdentity {
	return RoutineIdentity{RoutineProviderRefresh, RoutineScopeProvider, outcome, code}
}
func MetadataMatchRoutine(outcome RoutineOutcome, code RoutineResultCode) RoutineIdentity {
	return RoutineIdentity{RoutineMetadataMatch, RoutineScopeSubscription, outcome, code}
}

// FileManagerOperationRoutine is the latest user file operation result of the
// file manager, one row scoped to the file manager component.
func FileManagerOperationRoutine(outcome RoutineOutcome, code RoutineResultCode) RoutineIdentity {
	return RoutineIdentity{RoutineFileManagerOperation, RoutineScopeComponent, outcome, code}
}

// FileManagerRoutineScopeID is the component scope of that routine row.
const FileManagerRoutineScopeID = "file_manager"

func RoutineRetentionPassIdentity(outcome RoutineOutcome, code RoutineResultCode) RoutineIdentity {
	return RoutineIdentity{RoutineRetentionPass, RoutineScopeGlobal, outcome, code}
}

// RoutineRequest records the latest result for one fixed routine identity.
type RoutineRequest struct {
	Identity       RoutineIdentity
	ScopeID        string
	ManagedAnimeID string
	Summary        string
	Details        json.RawMessage
}

// Recorder persists the two bounded observability evidence models. Standalone
// methods own their transaction; Tx methods participate in the caller's one.
type Recorder interface {
	UpsertRoutine(context.Context, RoutineRequest) error
	RecordDiagnosticFailure(context.Context, DiagnosticFailureRequest) error
	RecordDiagnosticSuccess(context.Context, DiagnosticSuccessRequest) error
	UpsertRoutineTx(context.Context, txscope.Tx, RoutineRequest) error
	RecordDiagnosticFailureTx(context.Context, txscope.Tx, DiagnosticFailureRequest) error
	RecordDiagnosticSuccessTx(context.Context, txscope.Tx, DiagnosticSuccessRequest) error
}

// ActivityRecord is the bounded, typed envelope for durable business history.
// Optional lineage and legacy owner columns remain nullable while existing owners migrate.
type ActivityRecord struct {
	SubscriptionID  string
	ManagedAnimeID  string
	Kind            string
	Outcome         string
	ReasonCode      string
	SubjectType     string
	SubjectID       string
	SubjectVersion  int64
	StableResultKey string
	CorrelationID   string
	CausationType   string
	CausationID     string
	RSSSourceID     string
	ReleaseID       string
	GroupRef        string
	// MessageCode names the entry's message in the closed catalog
	// (activity_messages.go); it must belong to Kind.
	MessageCode MessageCode
	// MessageParams are the scalar values the message interpolates: exactly
	// the params the code declares (string or integer). Strings are redacted
	// and cut to MaxMessageParamBytes; an empty string is omitted.
	MessageParams map[string]any
	Details       json.RawMessage
	// OccurredAt is the time of the business fact when it differs from the
	// write time (the independent acquisition consumer writes after commit);
	// zero means "now".
	OccurredAt time.Time
	// DedupeKey replaces the derived kind/subject/version/result replay key
	// when the writer's replay identity must not depend on an owner version
	// that can still move (the acquisition consumer keys on the immutable
	// fact instead). Empty keeps the derived key.
	DedupeKey string
}

// ActivityWriter appends business history inside the caller's owner transaction.
type ActivityWriter interface {
	AppendTx(context.Context, txscope.Tx, ActivityRecord) error
}

// RuntimeEvent is the stable, redacted envelope for process diagnostics.
// Optional fields are omitted by runtime logger adapters when they are absent.
type RuntimeEvent struct {
	Message             string
	Component           string
	Operation           string
	Outcome             string
	ReasonCode          string
	RequestID           string
	CorrelationID       string
	OwnerType           string
	OwnerID             string
	WorkItemID          string
	Generation          int64
	Attempt             int
	Retryable           *bool
	DurationMS          int64
	ErrorClass          string
	CandidateCount      *int
	ExecutableCount     *int
	ExcludeReasonCounts map[string]int
}

// MaxRequestIDLength is the maximum length of an externally supplied request ID.
const MaxRequestIDLength = 128

// IsSafeRequestID reports whether value is a bounded, opaque ASCII request ID.
// Request IDs are correlation metadata, not credential-bearing free-form text.
func IsSafeRequestID(value string) bool {
	if value == "" || len(value) > MaxRequestIDLength {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '.', '_', ':', '-':
		default:
			return false
		}
	}
	return true
}

// RuntimeLogger writes structured runtime diagnostics. Implementations must not
// expose raw errors, panic values, payloads, credentials, or private paths.
type RuntimeLogger interface {
	Debug(context.Context, RuntimeEvent)
	Info(context.Context, RuntimeEvent)
	Warn(context.Context, RuntimeEvent)
	Error(context.Context, RuntimeEvent)
}

// NopRuntimeLogger safely discards runtime events. It is useful for isolated
// tests and optional package-level integrations that have no sink configured.
type NopRuntimeLogger struct{}

func (NopRuntimeLogger) Debug(context.Context, RuntimeEvent) {}
func (NopRuntimeLogger) Info(context.Context, RuntimeEvent)  {}
func (NopRuntimeLogger) Warn(context.Context, RuntimeEvent)  {}
func (NopRuntimeLogger) Error(context.Context, RuntimeEvent) {}

var _ RuntimeLogger = NopRuntimeLogger{}
