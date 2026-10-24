package httpapi

import (
	"net/http"

	automationtransport "bansoko/internal/automation/transport"
	"bansoko/internal/transport/httpapi/generated"
)

func (s *Server) automationService(w http.ResponseWriter, r *http.Request) AutomationService {
	return s.deps.Automation
}

// ListAutomationSchedules returns every automation schedule row.
func (s *Server) ListAutomationSchedules(w http.ResponseWriter, r *http.Request) {
	automation := s.automationService(w, r)
	if automation == nil {
		return
	}
	schedules, err := automation.ListSchedules(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, automationtransport.Schedules(schedules))
}

// PatchAutomationSchedule replaces one schedule's enabled/interval fields.
func (s *Server) PatchAutomationSchedule(w http.ResponseWriter, r *http.Request, kind generated.AutomationKindPath) {
	automation := s.automationService(w, r)
	if automation == nil {
		return
	}
	var patch generated.AutomationSchedulePatch
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, r, err)
		return
	}
	schedule, err := automation.PatchSchedule(r.Context(), string(kind), automationtransport.SchedulePatch(patch))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, automationtransport.Schedule(schedule))
}

// RunAutomation queues one manual dispatch-run for the given kind.
func (s *Server) RunAutomation(w http.ResponseWriter, r *http.Request, kind generated.AutomationKindPath, params generated.RunAutomationParams) {
	automation := s.automationService(w, r)
	if automation == nil {
		return
	}
	result, err := automation.RunNow(r.Context(), string(kind), string(params.IdempotencyKey))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusAccepted, automationtransport.RunAccepted(result))
}

// GetAutomationRun returns one dispatch-run row by ID.
func (s *Server) GetAutomationRun(w http.ResponseWriter, r *http.Request, runID generated.AutomationRunID) {
	automation := s.automationService(w, r)
	if automation == nil {
		return
	}
	run, err := automation.GetRun(r.Context(), string(runID))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, automationtransport.DispatchRun(run))
}
