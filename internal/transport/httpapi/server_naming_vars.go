package httpapi

import (
	"context"
	"net/http"

	"bansoko/internal/namingvars"
	"bansoko/internal/namingvars/editor"
	"bansoko/internal/transport/httpapi/generated"
)

// NamingVarsService is the in-app naming Vars program editor boundary. It is
// implemented by internal/namingvars/editor.
type NamingVarsService interface {
	GetState(ctx context.Context) (editor.State, error)
	GetVersion(ctx context.Context, revision int64) (namingvars.Version, error)
	ListReleases(ctx context.Context) ([]editor.ReleaseOption, error)
	DryRun(ctx context.Context, source string, targets []editor.DryRunTarget) (editor.DryRunResult, error)
	Save(ctx context.Context, source, note string, expected int64) (namingvars.Version, error)
	Activate(ctx context.Context, revision, expected int64) (namingvars.Version, error)
	Export(ctx context.Context) (editor.Document, error)
	Import(ctx context.Context, document editor.Document, expected int64) (namingvars.Version, error)
}

func (s *Server) namingVars(w http.ResponseWriter, r *http.Request) NamingVarsService {
	if s.deps.NamingVars == nil {
		writeError(w, r, serviceNotConfigured("naming vars editor"))
		return nil
	}
	return s.deps.NamingVars
}

// requireOwner rejects a caller that is not the owner account. Export and
// import move whole programs in and out of the deployment, so the check lives
// here at the boundary, not in the page.
func requireOwner(w http.ResponseWriter, r *http.Request) bool {
	if account, ok := AccountFrom(r.Context()); ok && account.Role == "owner" {
		return true
	}
	writeError(w, r, &APIError{Status: http.StatusForbidden, Code: "OWNER_REQUIRED", Message: "Only the owner may do this."})
	return false
}

// GetNamingVars returns the active program, its versions and editor reference data.
func (s *Server) GetNamingVars(w http.ResponseWriter, r *http.Request) {
	service := s.namingVars(w, r)
	if service == nil {
		return
	}
	state, err := service.GetState(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, mapNamingVarsState(state))
}

// GetNamingVarsVersion returns one published version.
func (s *Server) GetNamingVarsVersion(w http.ResponseWriter, r *http.Request, revision int64) {
	service := s.namingVars(w, r)
	if service == nil {
		return
	}
	version, err := service.GetVersion(r.Context(), revision)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapNamingVarsProgram(version))
}

// ListNamingVarsReleases lists persisted releases a dry run can use.
func (s *Server) ListNamingVarsReleases(w http.ResponseWriter, r *http.Request) {
	service := s.namingVars(w, r)
	if service == nil {
		return
	}
	options, err := service.ListReleases(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := make([]generated.NamingVarsReleaseOption, 0, len(options))
	for _, option := range options {
		out = append(out, generated.NamingVarsReleaseOption{Id: option.ID, Title: option.Title, AnimeTitle: option.AnimeTitle})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, out)
}

// DryRunNamingVars evaluates a draft program without storing anything.
func (s *Server) DryRunNamingVars(w http.ResponseWriter, r *http.Request) {
	service := s.namingVars(w, r)
	if service == nil {
		return
	}
	var input generated.NamingVarsDryRunInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	targets := make([]editor.DryRunTarget, 0, len(input.Targets))
	for _, target := range input.Targets {
		mapped := editor.DryRunTarget{Kind: string(target.Kind)}
		if target.Sample != nil {
			mapped.Sample = *target.Sample
		}
		if target.ReleaseId != nil {
			mapped.ReleaseID = *target.ReleaseId
		}
		if target.Title != nil {
			mapped.Title = *target.Title
		}
		if target.Files != nil {
			for _, file := range *target.Files {
				mapped.Files = append(mapped.Files, editor.CustomFile{Path: file.Path, Size: file.Size})
			}
		}
		targets = append(targets, mapped)
	}
	result, err := service.DryRun(r.Context(), input.Source, targets)
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, mapNamingVarsDryRun(result))
}

// SaveNamingVarsVersion publishes and activates a new version.
func (s *Server) SaveNamingVarsVersion(w http.ResponseWriter, r *http.Request, params generated.SaveNamingVarsVersionParams) {
	service := s.namingVars(w, r)
	if service == nil {
		return
	}
	var input generated.NamingVarsSaveInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	note := ""
	if input.Note != nil {
		note = *input.Note
	}
	version, err := service.Save(r.Context(), input.Source, note, int64(params.IfMatch))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, mapNamingVarsProgram(version))
}

// ActivateNamingVarsVersion moves the active pointer to a published revision.
func (s *Server) ActivateNamingVarsVersion(w http.ResponseWriter, r *http.Request, params generated.ActivateNamingVarsVersionParams) {
	service := s.namingVars(w, r)
	if service == nil {
		return
	}
	var input generated.NamingVarsActivateInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	version, err := service.Activate(r.Context(), input.Revision, int64(params.IfMatch))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusOK, mapNamingVarsProgram(version))
}

// ExportNamingVars returns the active program as an export document (owner only).
func (s *Server) ExportNamingVars(w http.ResponseWriter, r *http.Request) {
	if !requireOwner(w, r) {
		return
	}
	service := s.namingVars(w, r)
	if service == nil {
		return
	}
	document, err := service.Export(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, r.Context(), http.StatusOK, mapNamingVarsDocument(document))
}

// ImportNamingVars publishes an export document as a new active version (owner only).
func (s *Server) ImportNamingVars(w http.ResponseWriter, r *http.Request, params generated.ImportNamingVarsParams) {
	if !requireOwner(w, r) {
		return
	}
	service := s.namingVars(w, r)
	if service == nil {
		return
	}
	var input generated.NamingVarsDocument
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	version, err := service.Import(r.Context(), editor.Document{
		Format: string(input.Format), FormatVersion: input.FormatVersion, Runtime: input.Runtime, Digest: input.Digest,
		Revision: input.Revision, Note: input.Note, ExportedAt: input.ExportedAt, Source: input.Source,
	}, int64(params.IfMatch))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r.Context(), http.StatusCreated, mapNamingVarsProgram(version))
}
