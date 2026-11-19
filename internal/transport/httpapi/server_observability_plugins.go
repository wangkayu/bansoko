package httpapi

import (
	"errors"
	"net/http"
	"strings"

	observabilityapplication "bansoko/internal/observability/application"
	"bansoko/internal/transport/httpapi/generated"
)

// ListObservabilityDiagnostics returns diagnostic episodes from the read service.
func (s *Server) ListObservabilityDiagnostics(w http.ResponseWriter, r *http.Request, params generated.ListObservabilityDiagnosticsParams) {
	result, err := s.deps.Observability.ListDiagnostics(r.Context(), mapObservabilityDiagnosticQuery(params))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapDiagnosticPage(result))
}

// runtimeLogSource is implemented by the runtime logger when it keeps either
// the on-disk history or the bounded in-memory tail.
type runtimeLogSource interface {
	RuntimeLogs(observabilityapplication.RuntimeLogQuery) (observabilityapplication.RuntimeLogPage, error)
}

// GetRuntimeLog returns a page of the retained runtime log history. It is for
// the owner at the keyboard only: an API token (and so MCP or any agent) is
// refused whatever its scopes.
func (s *Server) GetRuntimeLog(w http.ResponseWriter, r *http.Request, params generated.GetRuntimeLogParams) {
	if account, ok := AccountFrom(r.Context()); !ok || !account.WebSession {
		writeError(w, r, webSessionRequired())
		return
	}
	level := observabilityapplication.RuntimeLogAll
	if params.Level != nil {
		level = observabilityapplication.RuntimeLogLevel(*params.Level)
	}
	limit := 200
	if params.Limit != nil {
		limit = *params.Limit
	}
	query := ""
	if params.Q != nil {
		query = strings.TrimSpace(*params.Q)
	}
	workItemID := ""
	if params.WorkItemId != nil {
		workItemID = strings.TrimSpace(*params.WorkItemId)
	}
	before := ""
	if params.Before != nil {
		before = strings.TrimSpace(*params.Before)
	}
	after := ""
	if params.After != nil {
		after = strings.TrimSpace(*params.After)
	}
	if before != "" && after != "" {
		writeError(w, r, unprocessable("RUNTIME_LOG_CURSOR_CONFLICT", "Use either before or after, not both.", nil))
		return
	}
	page := observabilityapplication.RuntimeLogPage{Source: observabilityapplication.RuntimeLogSourceMemory}
	if source, ok := s.deps.RuntimeLogger.(runtimeLogSource); ok {
		result, err := source.RuntimeLogs(observabilityapplication.RuntimeLogQuery{
			Level:      level,
			Query:      query,
			WorkItemID: workItemID,
			Limit:      limit,
			Before:     before,
			After:      after,
		})
		if err != nil {
			if errors.Is(err, observabilityapplication.ErrRuntimeLogCursorInvalid) {
				writeError(w, r, unprocessable("RUNTIME_LOG_CURSOR_INVALID", "The log cursor is not valid.", nil))
				return
			}
			writeError(w, r, err)
			return
		}
		page = result
	}
	writeJSON(w, r.Context(), http.StatusOK, mapRuntimeLogPage(page))
}

// ListObservabilityRecords returns the union record stream from the read service.
func (s *Server) ListObservabilityRecords(w http.ResponseWriter, r *http.Request, params generated.ListObservabilityRecordsParams) {
	result, err := s.deps.Observability.ListRecords(r.Context(), mapObservabilityRecordQuery(params))
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapObservabilityRecordPage(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// ListReleaseBatchEntries pages the detail entries behind one
// release_batch_processed head, in the same record shape as the union feed.
func (s *Server) ListReleaseBatchEntries(w http.ResponseWriter, r *http.Request, batchKey string, params generated.ListReleaseBatchEntriesParams) {
	var cursor *string
	if params.Cursor != nil {
		mapped := string(*params.Cursor)
		cursor = &mapped
	}
	var limit *int
	if params.Limit != nil {
		mapped := int(*params.Limit)
		limit = &mapped
	}
	result, err := s.deps.Observability.ListReleaseBatch(r.Context(), batchKey, observabilityapplication.RecordQuery{Cursor: cursor, Limit: limit})
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapObservabilityRecordPage(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// ListPluginDeliveries returns the bounded read-only delivery list.
func (s *Server) ListPluginDeliveries(w http.ResponseWriter, r *http.Request, params generated.ListPluginDeliveriesParams) {
	result, err := s.deps.PluginTargets.ListDeliveries(r.Context(), mapPluginDeliveryQuery(params))
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapPluginDeliveryPage(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// GetPluginDelivery returns a single delivery with its bounded attempts.
func (s *Server) GetPluginDelivery(w http.ResponseWriter, r *http.Request, deliveryID generated.PluginDeliveryID) {
	result, err := s.deps.PluginTargets.GetDelivery(r.Context(), deliveryID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapPluginDeliveryDetail(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// ListPluginTargets returns every configured target as a non-secret read model.
func (s *Server) ListPluginTargets(w http.ResponseWriter, r *http.Request) {
	result, err := s.deps.PluginTargets.ListTargets(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapPluginTargetPage(result)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// CreatePluginTarget atomically creates a target and its optional secret.
func (s *Server) CreatePluginTarget(w http.ResponseWriter, r *http.Request, params generated.CreatePluginTargetParams) {
	var input generated.PluginTargetCreate
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	neutral, err := mapPluginTargetInput(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	target, err := s.deps.PluginTargets.CreateTarget(r.Context(), neutral, string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapPluginTargetConfig(target)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, mapped)
}

// UpdatePluginTarget applies a CAS update to an existing target.
func (s *Server) UpdatePluginTarget(w http.ResponseWriter, r *http.Request, targetID generated.PluginTargetID, params generated.UpdatePluginTargetParams) {
	var input generated.PluginTargetUpdate
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	neutral, err := mapPluginTargetUpdate(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	target, err := s.deps.PluginTargets.UpdateTarget(r.Context(), targetID, int64(params.IfMatch), neutral)
	if err != nil {
		writeError(w, r, err)
		return
	}
	mapped, err := mapPluginTargetConfig(target)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapped)
}

// DeletePluginTarget soft-deletes a notification target (evidence retained).
func (s *Server) DeletePluginTarget(w http.ResponseWriter, r *http.Request, targetID generated.PluginTargetID) {
	if err := s.deps.PluginTargets.DeleteTarget(r.Context(), targetID); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestPluginTarget creates a real test delivery and its typed WorkItem.
func (s *Server) TestPluginTarget(w http.ResponseWriter, r *http.Request, targetID generated.PluginTargetID, params generated.TestPluginTargetParams) {
	var input generated.PluginTargetTestInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	result, err := s.deps.PluginTargets.TestTarget(r.Context(), targetID, mapPluginTargetTestInput(input), string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusAccepted, mapPluginTestAccepted(result))
}

// GetNamingTemplateCatalog returns the closed naming template catalog.
func (s *Server) GetNamingTemplateCatalog(w http.ResponseWriter, r *http.Request, params generated.GetNamingTemplateCatalogParams) {
	language := ""
	if params.Language != nil {
		language = string(*params.Language)
	}
	namingCatalog, err := s.deps.TemplateCatalog.GetNamingCatalog(r.Context(), language)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapNamingTemplateCatalog(namingCatalog))
}

// GetNotificationTemplateCatalog returns the closed notification template
// catalog for one trigger kind.
func (s *Server) GetNotificationTemplateCatalog(w http.ResponseWriter, r *http.Request, triggerKind generated.TriggerKindPath, params generated.GetNotificationTemplateCatalogParams) {
	catalogVersion := ""
	if params.CatalogVersion != nil {
		catalogVersion = string(*params.CatalogVersion)
	}
	locale := ""
	if params.Locale != nil {
		locale = string(*params.Locale)
	}
	notificationCatalog, err := s.deps.TemplateCatalog.GetNotificationCatalog(r.Context(), string(triggerKind), catalogVersion, locale)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapNotificationTemplateCatalog(notificationCatalog))
}

// PreviewNamingTemplate renders a naming template against fixed sample facts.
func (s *Server) PreviewNamingTemplate(w http.ResponseWriter, r *http.Request) {
	var input generated.NamingTemplatePreviewInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	namingPreview, err := s.deps.TemplateCatalog.PreviewNaming(r.Context(), mapNamingTemplatePreviewInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapNamingTemplatePreview(namingPreview))
}

// PreviewNotificationTemplate renders notification title/body templates against
// fixed sample facts.
func (s *Server) PreviewNotificationTemplate(w http.ResponseWriter, r *http.Request) {
	var input generated.NotificationTemplatePreviewInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	notificationPreview, err := s.deps.TemplateCatalog.PreviewNotification(r.Context(), mapNotificationTemplatePreviewInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapNotificationTemplatePreview(notificationPreview))
}
