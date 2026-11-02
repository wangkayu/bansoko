package httpapi

import (
	"context"
	"errors"
	"net/http"

	"bansoko/internal/parser/titleparser"
	"bansoko/internal/transport/httpapi/generated"
)

// ParserComponentService is the reltitle parser component read model and
// update boundary. It is implemented by internal/parser/titleparser.
type ParserComponentService interface {
	Status(ctx context.Context) (titleparser.Status, error)
	Check(ctx context.Context) (titleparser.Status, error)
	Install(ctx context.Context) (titleparser.Status, error)
}

// SetTitleParser wires the parser component service. It is set once at
// composition, before the server starts serving.
func (s *Server) SetTitleParser(service ParserComponentService) {
	if s != nil {
		s.deps.TitleParser = service
	}
}

// GetTitleParser returns the parser component read model. It never contacts
// the channel; check answers are cached by the service.
func (s *Server) GetTitleParser(w http.ResponseWriter, r *http.Request) {
	if s.deps.TitleParser == nil {
		writeError(w, r, serviceNotConfigured("title parser"))
		return
	}
	status, err := s.deps.TitleParser.Status(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, toGeneratedTitleParserStatus(status))
}

// CheckTitleParser reads the published channel once and returns the resulting
// status. A channel that cannot be read is a 502, not a silent success; the
// cache still keeps the last successful answer for GetTitleParser.
func (s *Server) CheckTitleParser(w http.ResponseWriter, r *http.Request) {
	if s.deps.TitleParser == nil {
		writeError(w, r, serviceNotConfigured("title parser"))
		return
	}
	status, err := s.deps.TitleParser.Check(r.Context())
	if err != nil {
		writeTitleParserError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, toGeneratedTitleParserStatus(status))
}

// UpdateTitleParser installs a strictly newer channel build and switches the
// running parser when the adoption chain accepts it. Replacing executable code
// is for the owner at the keyboard only, not an API token or an agent.
func (s *Server) UpdateTitleParser(w http.ResponseWriter, r *http.Request) {
	if s.deps.TitleParser == nil {
		writeError(w, r, serviceNotConfigured("title parser"))
		return
	}
	if account, ok := AccountFrom(r.Context()); !ok || !account.WebSession {
		writeError(w, r, webSessionRequired())
		return
	}
	status, err := s.deps.TitleParser.Install(r.Context())
	if err != nil {
		writeTitleParserError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, toGeneratedTitleParserStatus(status))
}

// writeTitleParserError keeps external channel failures at the gateway
// boundary; every other failure keeps the repository's typed mapping.
func writeTitleParserError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, titleparser.ErrUpdate):
		writeError(w, r, &APIError{
			Status:  http.StatusBadGateway,
			Code:    "TITLE_PARSER_UPDATE_FAILED",
			Message: "The downloaded parser could not be adopted; the previous parser keeps running.",
		})
	case errors.Is(err, titleparser.ErrChannel):
		writeError(w, r, &APIError{
			Status:  http.StatusBadGateway,
			Code:    "TITLE_PARSER_CHANNEL_UNAVAILABLE",
			Message: "The reltitle channel could not be read.",
		})
	default:
		writeError(w, r, err)
	}
}

func toGeneratedTitleParserStatus(status titleparser.Status) generated.TitleParserStatus {
	result := generated.TitleParserStatus{
		Source:          generated.TitleParserStatusSource(status.Source),
		Version:         status.Version,
		Line:            status.Line,
		ConfigDigest:    status.ConfigDigest,
		BinaryPath:      status.BinaryPath,
		Note:            status.Note,
		ExternalFailure: status.ExternalFailure,
		Lane:            generated.TitleParserStatusLane(status.Lane),
		Message:         status.Message,
	}
	if status.Channel != nil {
		result.Channel = &generated.TitleParserChannel{
			CheckedAt:  status.Channel.CheckedAt,
			Version:    status.Channel.Version,
			Notes:      generated.TitleParserChannelNotes{ZhCn: status.Channel.Notes.ZhCN, En: status.Channel.Notes.EN},
			ReleaseUrl: status.Channel.ReleaseURL,
			Newer:      status.Channel.Newer,
			Error:      status.Channel.Error,
		}
	}
	return result
}
