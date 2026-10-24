package httpapi

import (
	"net/http"

	configurationtransport "bansoko/internal/configuration/transport"
	"bansoko/internal/transport/httpapi/generated"
)

// ListGlobalSections returns every global config section with its current
// effective value and version. Sections without a saved row report the code
// default with version 1.
func (s *Server) ListGlobalSections(w http.ResponseWriter, r *http.Request) {
	defer deferHandlerTiming(r, "list_global_sections")()
	sections, err := s.deps.GlobalSections.ListGlobalSections(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, configurationtransport.GlobalSectionViews(sections))
}

// PutGlobalSection saves one global config section with If-Match version CAS.
func (s *Server) PutGlobalSection(w http.ResponseWriter, r *http.Request, sectionName generated.GlobalSectionName, params generated.PutGlobalSectionParams) {
	var input generated.GlobalSectionInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	view, err := s.deps.GlobalSections.PutGlobalSection(r.Context(), string(sectionName), int64(params.IfMatch), configurationtransport.GlobalSectionInput(input))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, configurationtransport.GlobalSectionView(view))
}

// GetNamingTemplateLibrary returns the naming template list and its default.
func (s *Server) GetNamingTemplateLibrary(w http.ResponseWriter, r *http.Request) {
	defer deferHandlerTiming(r, "get_naming_template_library")()
	library, err := s.deps.GlobalSections.GetNamingTemplateLibrary(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := configurationtransport.NamingTemplateLibraryView(library)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}

// PutNamingTemplateLibrary replaces the naming template list with If-Match
// version CAS.
func (s *Server) PutNamingTemplateLibrary(w http.ResponseWriter, r *http.Request, params generated.PutNamingTemplateLibraryParams) {
	var input generated.NamingTemplateLibraryInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	domainInput, err := configurationtransport.NamingTemplateLibraryInput(input)
	if err != nil {
		writeError(w, r, err)
		return
	}
	library, err := s.deps.GlobalSections.PutNamingTemplateLibrary(r.Context(), int64(params.IfMatch), domainInput)
	if err != nil {
		writeError(w, r, err)
		return
	}
	view, err := configurationtransport.NamingTemplateLibraryView(library)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, view)
}
