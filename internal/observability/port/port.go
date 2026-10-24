// Package port defines the narrow typed boundary for durable observability writes.
package port

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
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
type DiagnosticComponent string
type DiagnosticOperation string
type DiagnosticErrorCode string
type DiagnosticScopeKind string

const (
	RoutineCatalogRefresh                 RoutineKind         = "catalog_refresh"
	RoutineProviderRefresh                RoutineKind         = "provider_refresh"
	RoutineRSSRefresh                     RoutineKind         = "rss_refresh"
	RoutineMetadataMatch                  RoutineKind         = "metadata_match"
	RoutineAutomationDispatch             RoutineKind         = "automation_dispatch"
	RoutineRetentionPass                  RoutineKind         = "retention_pass"
	RoutineFileManagerOperation           RoutineKind         = "file_manager_operation"
	RoutineScopeGlobal                    RoutineScopeKind    = "global"
	RoutineScopeAutomation                RoutineScopeKind    = "automation"
	RoutineScopeProvider                  RoutineScopeKind    = "provider"
	RoutineScopeSubscription              RoutineScopeKind    = "subscription"
	RoutineScopeRSSSource                 RoutineScopeKind    = "rss_source"
	RoutineScopeComponent                 RoutineScopeKind    = "component"
	RoutineOutcomeSuccess                 RoutineOutcome      = "success"
	RoutineOutcomeWarning                 RoutineOutcome      = "warning"
	RoutineOutcomeFailure                 RoutineOutcome      = "failure"
	RoutineResultCatalogRefreshCompleted  RoutineResultCode   = "catalog_refresh_completed"
	RoutineResultCatalogRefreshFailed     RoutineResultCode   = "catalog_refresh_failed"
	RoutineResultProviderRefreshCompleted RoutineResultCode   = "provider_refresh_completed"
	RoutineResultProviderRefreshFailed    RoutineResultCode   = "provider_refresh_failed"
	RoutineResultProviderParserWarning    RoutineResultCode   = "provider_parser_warning"
	RoutineResultRSSRefreshSucceeded      RoutineResultCode   = "refresh_succeeded"
	RoutineResultRSSRefreshFailed         RoutineResultCode   = "refresh_failed"
	RoutineResultMetadataPending          RoutineResultCode   = "metadata_match_pending"
	RoutineResultMetadataCompleted        RoutineResultCode   = "metadata_match_completed"
	RoutineResultMetadataPartial          RoutineResultCode   = "metadata_match_partial"
	RoutineResultMetadataNoResult         RoutineResultCode   = "metadata_match_no_result"
	RoutineResultMetadataConfigError      RoutineResultCode   = "metadata_match_config_error"
	RoutineResultMetadataProviderError    RoutineResultCode   = "metadata_match_provider_error"
	RoutineResultDispatchCompleted        RoutineResultCode   = "dispatch_completed"
	RoutineResultDispatchPartial          RoutineResultCode   = "dispatch_partial"
	RoutineResultDispatchFailed           RoutineResultCode   = "dispatch_failed"
	RoutineResultRetentionPassCompleted   RoutineResultCode   = "retention_pass_completed"
	RoutineResultRetentionPassFailed      RoutineResultCode   = "retention_pass_failed"
	RoutineResultFileManagerCompleted     RoutineResultCode   = "file_manager_operation_completed"
	RoutineResultFileManagerFailed        RoutineResultCode   = "file_manager_operation_failed"
	SeverityWarning                       DiagnosticSeverity  = "warning"
	SeverityError                         DiagnosticSeverity  = "error"
	ComponentAutomation                   DiagnosticComponent = "automation"
	ComponentPluginDelivery               DiagnosticComponent = "plugin_delivery"
	ComponentReplacement                  DiagnosticComponent = "replacement"
	ComponentRSS                          DiagnosticComponent = "rss"
	ComponentDownload                     DiagnosticComponent = "download"
	ComponentFileOperation                DiagnosticComponent = "fileoperation"
	ComponentProvider                     DiagnosticComponent = "provider"
	ComponentHTTPAPI                      DiagnosticComponent = "httpapi"
	ComponentRetention                    DiagnosticComponent = "retention"
	ComponentActivity                     DiagnosticComponent = "activity"
	OperationDispatch                     DiagnosticOperation = "dispatch"
	OperationPrepare                      DiagnosticOperation = "prepare"
	OperationRefresh                      DiagnosticOperation = "refresh"
	OperationQBSubmit                     DiagnosticOperation = "qb_submit"
	OperationExecute                      DiagnosticOperation = "execute"
	OperationEvaluateTrigger              DiagnosticOperation = "evaluate_trigger"
	OperationPlacementAdmit               DiagnosticOperation = "placement_admit"
	OperationRequest                      DiagnosticOperation = "request"
	OperationSweep                        DiagnosticOperation = "sweep"
	OperationConsume                      DiagnosticOperation = "consume"
	ErrorDispatchFailed                   DiagnosticErrorCode = "dispatch_failed"
	ErrorDispatchPartial                  DiagnosticErrorCode = "dispatch_partial"
	ErrorNotificationTemplateInvalid      DiagnosticErrorCode = "notification_template_invalid"
	ErrorSecretResolveFailed              DiagnosticErrorCode = "secret_resolve_failed"
	ErrorRequestBuildFailed               DiagnosticErrorCode = "request_build_failed"
	ErrorRSSRefreshFailed                 DiagnosticErrorCode = "rss_refresh_failed"
	ErrorQBSubmitFailed                   DiagnosticErrorCode = "qb_submit_failed"
	ErrorFileOperationFailed              DiagnosticErrorCode = "file_operation_failed"
	ErrorProviderParserEmpty              DiagnosticErrorCode = "provider_parser_empty"
	ErrorProviderParserSchemaMismatch     DiagnosticErrorCode = "provider_parser_schema_mismatch"
	ErrorReplacementTriggerFailed         DiagnosticErrorCode = "replacement_trigger_failed"
	ErrorPlacementSinkFailed              DiagnosticErrorCode = "placement_sink_failed"
	ErrorRetentionSweepFailed             DiagnosticErrorCode = "retention_sweep_failed"
	ErrorRSSProviderPaused                DiagnosticErrorCode = "rss_provider_paused"
	ErrorActivityConsumeFailed            DiagnosticErrorCode = "activity_consume_failed"
	ScopeGlobal                           DiagnosticScopeKind = "global"
	ScopeProvider                         DiagnosticScopeKind = "provider"
	ScopeSubscription                     DiagnosticScopeKind = "subscription"
	ScopeRSSSource                        DiagnosticScopeKind = "rss_source"
	ScopeOwner                            DiagnosticScopeKind = "owner"
	ScopeWorker                           DiagnosticScopeKind = "worker"
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
	diagnosticComponents = map[DiagnosticComponent]struct{}{ComponentAutomation: {}, ComponentPluginDelivery: {}, ComponentReplacement: {}, ComponentRSS: {}, ComponentDownload: {}, ComponentFileOperation: {}, ComponentProvider: {}, ComponentHTTPAPI: {}, ComponentRetention: {}, ComponentActivity: {}}
	diagnosticOperations = map[DiagnosticOperation]struct{}{OperationDispatch: {}, OperationPrepare: {}, OperationRefresh: {}, OperationQBSubmit: {}, OperationExecute: {}, OperationEvaluateTrigger: {}, OperationPlacementAdmit: {}, OperationRequest: {}, OperationSweep: {}, OperationConsume: {}}
	diagnosticErrorCodes = map[DiagnosticErrorCode]struct{}{ErrorDispatchFailed: {}, ErrorDispatchPartial: {}, ErrorNotificationTemplateInvalid: {}, ErrorSecretResolveFailed: {}, ErrorRequestBuildFailed: {}, ErrorRSSRefreshFailed: {}, ErrorQBSubmitFailed: {}, ErrorFileOperationFailed: {}, ErrorProviderParserEmpty: {}, ErrorProviderParserSchemaMismatch: {}, ErrorReplacementTriggerFailed: {}, ErrorPlacementSinkFailed: {}, ErrorRetentionSweepFailed: {}, ErrorRSSProviderPaused: {}, ErrorActivityConsumeFailed: {}}
	diagnosticScopes     = map[DiagnosticScopeKind]struct{}{ScopeGlobal: {}, ScopeProvider: {}, ScopeSubscription: {}, ScopeRSSSource: {}, ScopeOwner: {}, ScopeWorker: {}}
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
func IsDiagnosticComponent(value DiagnosticComponent) bool {
	_, ok := diagnosticComponents[value]
	return ok
}
func IsDiagnosticOperation(value DiagnosticOperation) bool {
	_, ok := diagnosticOperations[value]
	return ok
}
func IsDiagnosticScopeKind(value DiagnosticScopeKind) bool {
	_, ok := diagnosticScopes[value]
	return ok
}
func IsDiagnosticErrorCode(value DiagnosticErrorCode) bool {
	if _, ok := diagnosticErrorCodes[value]; ok {
		return true
	}
	if !strings.HasPrefix(string(value), "http_") {
		return false
	}
	status := strings.TrimPrefix(string(value), "http_")
	return len(status) == 3 && status[0] >= '1' && status[0] <= '5' && status[1] >= '0' && status[1] <= '9' && status[2] >= '0' && status[2] <= '9'
}

// DiagnosticComponents returns the complete component allowlist in stable order.
func DiagnosticComponents() []DiagnosticComponent {
	components := make([]DiagnosticComponent, 0, len(diagnosticComponents))
	for component := range diagnosticComponents {
		components = append(components, component)
	}
	sort.Slice(components, func(i, j int) bool { return components[i] < components[j] })
	return components
}

// DiagnosticIdentities returns the complete immutable diagnostic identity catalog.
func DiagnosticIdentities() []DiagnosticIdentity {
	return []DiagnosticIdentity{
		AutomationDispatchFailure(ScopeWorker), AutomationDispatchPartial(ScopeWorker),
		PluginPrepareFailure(ErrorNotificationTemplateInvalid), PluginPrepareFailure(ErrorSecretResolveFailed), PluginPrepareFailure(ErrorRequestBuildFailed),
		ProviderRefreshFailure(ErrorProviderParserEmpty), ProviderRefreshFailure(ErrorProviderParserSchemaMismatch), HTTPRequestFailure(DiagnosticErrorCode("http_500")),
		DiagnosticRetentionSweepFailure(), RSSProviderPaused(),
		AcquisitionActivityConsumeFailure(),
	}
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

// DiagnosticIdentity is the catalogued portion shared by failure and recovery requests.
type DiagnosticIdentity struct {
	Component DiagnosticComponent
	Operation DiagnosticOperation
	ErrorCode DiagnosticErrorCode
	ScopeKind DiagnosticScopeKind
}

// Constructors bind every production failure identity to the typed catalog.
func AutomationDispatchFailure(scope DiagnosticScopeKind) DiagnosticIdentity {
	return DiagnosticIdentity{ComponentAutomation, OperationDispatch, ErrorDispatchFailed, scope}
}
func AutomationDispatchPartial(scope DiagnosticScopeKind) DiagnosticIdentity {
	return DiagnosticIdentity{ComponentAutomation, OperationDispatch, ErrorDispatchPartial, scope}
}
func PluginPrepareFailure(code DiagnosticErrorCode) DiagnosticIdentity {
	return DiagnosticIdentity{ComponentPluginDelivery, OperationPrepare, code, ScopeOwner}
}
func ReplacementTriggerFailure() DiagnosticIdentity {
	return DiagnosticIdentity{ComponentReplacement, OperationEvaluateTrigger, ErrorReplacementTriggerFailed, ScopeOwner}
}
func RSSRefreshFailure() DiagnosticIdentity {
	return DiagnosticIdentity{ComponentRSS, OperationRefresh, ErrorRSSRefreshFailed, ScopeRSSSource}
}

// RSSProviderPaused is the provider-scoped episode open while automatic RSS
// dispatch for that provider is paused by its failure circuit.
func RSSProviderPaused() DiagnosticIdentity {
	return DiagnosticIdentity{ComponentRSS, OperationDispatch, ErrorRSSProviderPaused, ScopeProvider}
}
func DownloadSubmitFailure() DiagnosticIdentity {
	return DiagnosticIdentity{ComponentDownload, OperationQBSubmit, ErrorQBSubmitFailed, ScopeOwner}
}
func FileOperationFailure() DiagnosticIdentity {
	return DiagnosticIdentity{ComponentFileOperation, OperationExecute, ErrorFileOperationFailed, ScopeOwner}
}
func PlacementAdmissionFailure() DiagnosticIdentity {
	return DiagnosticIdentity{ComponentFileOperation, OperationPlacementAdmit, ErrorPlacementSinkFailed, ScopeOwner}
}
func ProviderRefreshFailure(code DiagnosticErrorCode) DiagnosticIdentity {
	return DiagnosticIdentity{ComponentProvider, OperationRefresh, code, ScopeProvider}
}
func HTTPRequestFailure(code DiagnosticErrorCode) DiagnosticIdentity {
	return DiagnosticIdentity{ComponentHTTPAPI, OperationRequest, code, ScopeGlobal}
}

// AcquisitionActivityConsumeFailure is the one global episode open while the
// independent acquisition Activity consumer cannot write the run log. A
// consumer failure never touches the acquisition it describes; the next pass
// retries and a clean pass closes the episode.
func AcquisitionActivityConsumeFailure() DiagnosticIdentity {
	return DiagnosticIdentity{ComponentActivity, OperationConsume, ErrorActivityConsumeFailed, ScopeGlobal}
}

func DiagnosticRetentionSweepFailure() DiagnosticIdentity {
	return DiagnosticIdentity{ComponentRetention, OperationSweep, ErrorRetentionSweepFailed, ScopeGlobal}
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

// DiagnosticFailureRequest records or updates one continuous fault episode.
type DiagnosticFailureRequest struct {
	Identity       DiagnosticIdentity
	Severity       DiagnosticSeverity
	ScopeID        string
	ManagedAnimeID string
	LastError      string
	Details        json.RawMessage
}

// DiagnosticSuccessRequest closes an open episode for the same operation identity.
type DiagnosticSuccessRequest struct {
	Identity DiagnosticIdentity
	ScopeID  string
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
	Title           string
	Summary         string
	Details         json.RawMessage
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
