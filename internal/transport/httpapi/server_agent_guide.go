package httpapi

import (
	"net/http"

	"bansoko/internal/agentguide"
	"bansoko/internal/transport/httpapi/generated"
)

// GetAgentGuide serves the embedded English agent guide as Markdown. It sits
// behind the normal bearer middleware like every other non-public path.
// format=skill wraps the same text as a SKILL.md document.
func (s *Server) GetAgentGuide(w http.ResponseWriter, r *http.Request, params generated.GetAgentGuideParams) {
	body := agentguide.Guide()
	if params.Format != nil {
		switch *params.Format {
		case generated.GetAgentGuideParamsFormatGuide:
		case generated.GetAgentGuideParamsFormatSkill:
			body = agentguide.Skill(agentguide.SkillName)
		default:
			writeError(w, r, unprocessable("AGENT_GUIDE_FORMAT_INVALID", "format must be guide or skill.", nil))
			return
		}
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}
