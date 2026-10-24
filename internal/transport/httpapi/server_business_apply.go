package httpapi

import (
	draftapplication "bansoko/internal/anime/draft/application"
	"bansoko/internal/transport/httpapi/generated"
	"net/http"
)

func (s *Server) ApplyAnimeConfigCandidate(w http.ResponseWriter, r *http.Request, candidateID generated.CandidateID, params generated.ApplyAnimeConfigCandidateParams) {
	service := s.draftService(w, r)
	if service == nil {
		return
	}
	var input generated.CandidateApplyInput
	if err := decodeJSON(w, r, &input); err != nil {
		writeError(w, r, err)
		return
	}
	versions := input.ExpectedOwnerVersions
	result, err := service.ApplyAnimeConfigCandidate(r.Context(), candidateID, string(params.IdempotencyKey), draftapplication.CandidateApplyInput{Revision: input.Revision, TargetManagedAnimeID: input.TargetManagedAnimeId, ExpectedCandidateVersion: input.ExpectedCandidateVersion, ExpectedOwnerVersions: draftapplication.BusinessOwnerVersions{Rules: versions.Rules, Subgroups: versions.Subgroups, Parser: versions.Parser, Retention: versions.Retention, FileEffect: versions.FileEffect, Business: versions.Business}})
	if err != nil {
		writeError(w, r, err)
		return
	}
	v := result.OwnerVersions
	writeJSON(w, r.Context(), http.StatusOK, generated.CandidateApplyResult{TargetManagedAnimeId: result.TargetManagedAnimeID, CandidateId: result.CandidateID, Revision: result.Revision, BusinessDigest: result.BusinessDigest, OwnerVersions: generated.BusinessOwnerVersions{Rules: v.Rules, Subgroups: v.Subgroups, Parser: v.Parser, Retention: v.Retention, FileEffect: v.FileEffect, Business: v.Business}})
}
