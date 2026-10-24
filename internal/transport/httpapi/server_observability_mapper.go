package httpapi

import (
	"context"
	"encoding/json"
	"fmt"

	observabilityapplication "bansoko/internal/observability/application"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/transport/httpapi/generated"
)

func mapObservabilityRecordQuery(value generated.ListObservabilityRecordsParams) observabilityapplication.RecordQuery {
	var kinds *[]string
	if value.Kind != nil {
		mapped := make([]string, len(*value.Kind))
		for i, kind := range *value.Kind {
			mapped[i] = string(kind)
		}
		kinds = &mapped
	}
	var outcome *string
	if value.Outcome != nil {
		mapped := string(*value.Outcome)
		outcome = &mapped
	}
	var managedAnimeID *string
	if value.ManagedAnimeId != nil {
		mapped := string(*value.ManagedAnimeId)
		managedAnimeID = &mapped
	}
	return observabilityapplication.RecordQuery{
		Cursor: value.Cursor, Limit: value.Limit, Kind: kinds, Outcome: outcome,
		From: value.From, To: value.To, ManagedAnimeID: managedAnimeID,
	}
}

func mapObservabilityDiagnosticQuery(value generated.ListObservabilityDiagnosticsParams) observabilityapplication.DiagnosticQuery {
	var severity *string
	if value.Severity != nil {
		mapped := string(*value.Severity)
		severity = &mapped
	}
	return observabilityapplication.DiagnosticQuery{
		Cursor: value.Cursor, Limit: value.Limit, Severity: severity,
		Component: value.Component, From: value.From, To: value.To,
	}
}

// mapObservabilityRecord projects one stored record onto the contract's
// discriminated union. ObservabilityRecord is a raw JSON union, so a plain
// unmarshal would forward whatever the read projection selected — including a
// storage-only column such as plugin_deliveries.secret_ref. Decoding into
// the typed member and marshalling that member back makes the transport an
// allowlist boundary: only fields the contract declares can reach the client,
// even if a future column is added to the read projection by mistake.
func mapObservabilityRecord(value observabilityapplication.Record) (generated.ObservabilityRecord, error) {
	var mapped generated.ObservabilityRecord
	if len(value.Raw) == 0 {
		return mapped, fmt.Errorf("observability record is empty")
	}
	var envelope struct {
		RecordType string `json:"record_type"`
	}
	if err := json.Unmarshal(value.Raw, &envelope); err != nil {
		return mapped, fmt.Errorf("decode observability record envelope: %w", err)
	}

	// Each member is decoded into its generated struct (unknown keys dropped)
	// and the struct itself is what the union then carries.
	var member any
	switch envelope.RecordType {
	case "routine":
		typed := generated.RoutineRecord{}
		if err := json.Unmarshal(value.Raw, &typed); err != nil {
			return mapped, fmt.Errorf("decode routine record: %w", err)
		}
		member = typed
	case "business":
		typed := generated.BusinessRecord{}
		if err := json.Unmarshal(value.Raw, &typed); err != nil {
			return mapped, fmt.Errorf("decode business record: %w", err)
		}
		member = typed
	case "automation":
		typed := generated.AutomationRecord{}
		if err := json.Unmarshal(value.Raw, &typed); err != nil {
			return mapped, fmt.Errorf("decode automation record: %w", err)
		}
		member = typed
	case "plugin":
		typed := generated.PluginRecord{}
		if err := json.Unmarshal(value.Raw, &typed); err != nil {
			return mapped, fmt.Errorf("decode plugin record: %w", err)
		}
		member = typed
	default:
		return mapped, fmt.Errorf("decode observability record: unknown record type %q", envelope.RecordType)
	}

	projected, err := json.Marshal(member)
	if err != nil {
		return mapped, fmt.Errorf("encode %s record: %w", envelope.RecordType, err)
	}
	if err := json.Unmarshal(projected, &mapped); err != nil {
		return mapped, fmt.Errorf("project %s record: %w", envelope.RecordType, err)
	}
	return mapped, nil
}

func mapObservabilityRecordPage(value observabilityapplication.RecordPage) (generated.ObservabilityRecordPage, error) {
	records := make([]generated.ObservabilityRecord, 0, len(value.Records))
	for _, record := range value.Records {
		mapped, err := mapObservabilityRecord(record)
		if err != nil {
			return generated.ObservabilityRecordPage{}, err
		}
		records = append(records, mapped)
	}
	return generated.ObservabilityRecordPage{Records: records, NextCursor: value.NextCursor}, nil
}

func mapDiagnosticEpisode(ctx context.Context, value observabilityapplication.DiagnosticEpisode) generated.DiagnosticEpisode {
	mapped := generated.DiagnosticEpisode{
		Component: value.Component, ErrorCode: value.ErrorCode, FirstFailedAt: value.FirstFailedAt,
		Id: generated.ID(value.ID), LastError: value.LastError, LastFailedAt: value.LastFailedAt,
		OccurrenceCount: value.OccurrenceCount, Operation: value.Operation, RetentionUntil: value.RetentionUntil,
		ScopeId: value.ScopeID, ScopeKind: generated.DiagnosticScopeKind(value.ScopeKind),
		Severity: generated.DiagnosticSeverity(value.Severity), SucceededAfterAt: value.SucceededAfterAt,
	}
	if value.ManagedAnimeID != nil {
		id := generated.ID(*value.ManagedAnimeID)
		mapped.ManagedAnimeId = &id
	}
	mapped.DetailsStatus = generated.DiagnosticDetailsStatusUnavailable
	if value.Details != nil {
		var details generated.DiagnosticDetails
		if err := json.Unmarshal(value.Details.Raw, &details); err != nil {
			// Stored details that no longer decode into the contract shape are
			// reported as a bounded invalid status, never silently dropped and
			// never serialised raw. The runtime event records the decode class
			// only; the failing bytes stay out of every log and response.
			mapped.DetailsStatus = generated.DiagnosticDetailsStatusInvalid
			runtimeLoggerFrom(ctx).Error(ctx, observabilityport.RuntimeEvent{
				Message:    "diagnostic details decode failed",
				Component:  "httpapi",
				Operation:  "map_diagnostic_episode",
				Outcome:    "failure",
				ReasonCode: "diagnostic_details_invalid",
				RequestID:  RequestIDFrom(ctx),
				ErrorClass: "diagnostic_details_decode_error",
			})
		} else {
			mapped.Details = &details
			mapped.DetailsStatus = generated.DiagnosticDetailsStatusAvailable
		}
	}
	return mapped
}

func mapDiagnosticPage(ctx context.Context, value observabilityapplication.DiagnosticPage) generated.DiagnosticEpisodePage {
	episodes := make([]generated.DiagnosticEpisode, 0, len(value.Episodes))
	for _, episode := range value.Episodes {
		episodes = append(episodes, mapDiagnosticEpisode(ctx, episode))
	}
	return generated.DiagnosticEpisodePage{Episodes: episodes, NextCursor: value.NextCursor}
}
