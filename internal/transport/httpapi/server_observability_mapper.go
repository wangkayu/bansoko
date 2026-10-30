package httpapi

import (
	"encoding/json"
	"fmt"

	observabilityapplication "bansoko/internal/observability/application"
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
	var entryKinds *[]string
	if value.EntryKind != nil {
		mapped := make([]string, len(*value.EntryKind))
		for i, kind := range *value.EntryKind {
			mapped[i] = string(kind)
		}
		entryKinds = &mapped
	}
	var state *string
	if value.State != nil {
		mapped := string(*value.State)
		state = &mapped
	}
	var managedAnimeID *string
	if value.ManagedAnimeId != nil {
		mapped := string(*value.ManagedAnimeId)
		managedAnimeID = &mapped
	}
	var eventView *string
	if value.EventView != nil {
		mapped := string(*value.EventView)
		eventView = &mapped
	}
	return observabilityapplication.RecordQuery{
		Cursor: value.Cursor, Limit: value.Limit, Kind: kinds, EntryKind: entryKinds, State: state, Q: value.Q,
		EventView: eventView, From: value.From, To: value.To, ManagedAnimeID: managedAnimeID,
	}
}

func mapObservabilityDiagnosticQuery(value generated.ListObservabilityDiagnosticsParams) observabilityapplication.DiagnosticQuery {
	var state *string
	if value.State != nil {
		mapped := string(*value.State)
		state = &mapped
	}
	return observabilityapplication.DiagnosticQuery{Cursor: value.Cursor, Limit: value.Limit, State: state}
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

func mapDiagnosticEpisode(value observabilityapplication.DiagnosticEpisode) generated.DiagnosticEpisode {
	mapped := generated.DiagnosticEpisode{
		Id: generated.ID(value.ID), Kind: generated.DiagnosticConditionKind(value.Kind),
		Variant: generated.DiagnosticConditionVariant(value.Variant),
		Target: generated.DiagnosticTarget{
			Kind: generated.DiagnosticConditionKind(value.Target.Kind), Id: value.Target.ID,
			Name: value.Target.Name, Host: value.Target.Host,
		},
		Recent:        make([]generated.DiagnosticOccurrence, 0, len(value.Recent)),
		FirstFailedAt: value.FirstFailedAt, LastFailedAt: value.LastFailedAt, SucceededAfterAt: value.SucceededAfterAt,
		OccurrenceCount: value.OccurrenceCount, Severity: generated.DiagnosticSeverity(value.Severity),
		RetentionUntil: value.RetentionUntil,
		Retry:          generated.DiagnosticRetry{Mode: generated.DiagnosticRetryMode(value.Retry.Mode)},
	}
	if value.Retry.SettingsTarget != nil {
		target := generated.DiagnosticSettingsTarget(*value.Retry.SettingsTarget)
		mapped.Retry.SettingsTarget = &target
	}
	if value.Detail != nil {
		detail := generated.DiagnosticDetail{ErrorClass: generated.DiagnosticDetailErrorClass(value.Detail.ErrorClass), HttpStatus: value.Detail.HTTPStatus}
		if value.Detail.Via != nil {
			via := generated.DiagnosticDetailVia(*value.Detail.Via)
			detail.Via = &via
		}
		mapped.Detail = &detail
	}
	if value.Affected != nil {
		mapped.Affected = &generated.DiagnosticAffected{Kind: generated.DiagnosticAffectedKind(value.Affected.Kind), Count: value.Affected.Count}
	}
	for _, occurrence := range value.Recent {
		item := generated.DiagnosticOccurrence{At: occurrence.At, ErrorClass: occurrence.ErrorClass, Host: occurrence.Host}
		if occurrence.Via != nil {
			via := generated.DiagnosticOccurrenceVia(*occurrence.Via)
			item.Via = &via
		}
		mapped.Recent = append(mapped.Recent, item)
	}
	return mapped
}

func mapDiagnosticPage(value observabilityapplication.DiagnosticPage) generated.DiagnosticEpisodePage {
	episodes := make([]generated.DiagnosticEpisode, 0, len(value.Episodes))
	for _, episode := range value.Episodes {
		episodes = append(episodes, mapDiagnosticEpisode(episode))
	}
	page := generated.DiagnosticEpisodePage{Episodes: episodes, NextCursor: value.NextCursor}
	if value.Internal != nil {
		page.Internal = &generated.DiagnosticInternalSummary{
			OccurrenceCount: value.Internal.OccurrenceCount, LastFailedAt: value.Internal.LastFailedAt,
			LastRequestId: value.Internal.LastRequestID,
		}
	}
	return page
}

func mapRuntimeLogPage(entries []observabilityapplication.RuntimeLogEntry) generated.RuntimeLogPage {
	lines := make([]generated.RuntimeLogLine, 0, len(entries))
	for _, entry := range entries {
		line := generated.RuntimeLogLine{
			At: entry.At, Level: generated.RuntimeLogLineLevel(entry.Level), Message: entry.Message,
			Component: optionalText(entry.Component), Operation: optionalText(entry.Operation),
			Outcome: optionalText(entry.Outcome), ReasonCode: optionalText(entry.ReasonCode),
			RequestId: optionalText(entry.RequestID), CorrelationId: optionalText(entry.CorrelationID),
			ErrorClass: optionalText(entry.ErrorClass),
		}
		lines = append(lines, line)
	}
	return generated.RuntimeLogPage{Lines: lines}
}

func optionalText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
