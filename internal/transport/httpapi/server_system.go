package httpapi

import (
	"net/http"
	"time"

	"bansoko/internal/runtime/readiness"
	"bansoko/internal/transport/httpapi/generated"
)

// GetLivez proves the process and HTTP runtime are alive. It never touches the
// database, providers or qBittorrent, so a business dependency failure is not
// misreported as a dead process. Admission decisions use GetReadyz.
func (s *Server) GetLivez(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r.Context(), http.StatusOK, generated.LivezReport{Status: generated.LivezReportStatusAlive})
}

// GetReadyz returns the structured admission evidence. Hard failures return
// 503 with the report body; ready (possibly degraded) returns 200.
func (s *Server) GetReadyz(w http.ResponseWriter, r *http.Request) {
	report := s.readinessReport(r)
	status := http.StatusOK
	if !report.Ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, r.Context(), status, report)
}

func (s *Server) readinessReport(r *http.Request) generated.ReadinessReport {
	report := s.deps.Readiness.Report(r.Context())
	return toGeneratedReadiness(report)
}

func toGeneratedReadiness(report readiness.Report) generated.ReadinessReport {
	checks := make(map[string]generated.ReadinessCheck, len(report.Checks))
	for name, outcome := range report.Checks {
		checks[name] = generated.ReadinessCheck{
			Ok:     outcome.OK,
			Code:   &outcome.Code,
			Detail: &outcome.Detail,
		}
	}
	result := generated.ReadinessReport{
		Ready:     report.Ready,
		Reason:    report.Reason,
		Checks:    checks,
		CheckedAt: report.CheckedAt,
	}
	if identity := report.DeploymentIdentity; identity != nil {
		result.DeploymentIdentity = &generated.DeploymentIdentity{}
		if value := identity.BaseImageDigest; value != "" {
			result.DeploymentIdentity.BaseImageDigest = &value
		}
		if value := identity.SelectedReleaseID; value != "" {
			result.DeploymentIdentity.SelectedReleaseId = &value
		}
		if value := identity.SelectedSource; value != "" {
			source := generated.DeploymentIdentitySelectedSource(value)
			result.DeploymentIdentity.SelectedSource = &source
		}
		if value := identity.PayloadDigest; value != "" {
			result.DeploymentIdentity.PayloadDigest = &value
		}
		if value := identity.UpdateOperationID; value != "" {
			result.DeploymentIdentity.UpdateOperationId = &value
		}
		if value := identity.ProcessBuildID; value != "" {
			result.DeploymentIdentity.ProcessBuildId = &value
		}
	}
	return result
}

// GetSystemHealth is the compatibility alias for the readiness contract. It is
// behaviorally equivalent to the http_alive + postgres checks of /readyz and is
// NOT a broader readiness signal; deployment consumers must use /readyz.
func (s *Server) GetSystemHealth(w http.ResponseWriter, r *http.Request) {
	if s.deps.Readiness == nil {
		if err := s.deps.DB.PingContext(r.Context()); err != nil {
			writeError(w, r, unavailable("DATABASE_UNAVAILABLE", "The database is not reachable."))
			return
		}
		writeJSON(w, r.Context(), http.StatusOK, generated.SystemHealth{
			Status:    generated.SystemHealthStatus("healthy"),
			Database:  generated.SystemHealthDatabase("healthy"),
			CheckedAt: time.Now().UTC(),
		})
		return
	}
	report := s.deps.Readiness.Report(r.Context())
	postgres := report.Checks["postgres"]
	if !postgres.OK {
		writeError(w, r, unavailable("DATABASE_UNAVAILABLE", "The database is not reachable."))
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, generated.SystemHealth{
		Status:    generated.SystemHealthStatus("healthy"),
		Database:  generated.SystemHealthDatabase("healthy"),
		CheckedAt: report.CheckedAt,
	})
}
