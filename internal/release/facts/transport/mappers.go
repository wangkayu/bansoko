package transport

import (
	"bansoko/internal/release/facts"
	"bansoko/internal/transport/httpapi/generated"
)

func View(value facts.View) generated.ReleaseCandidateFactsView {
	var sizeBytes *int
	if value.SizeBytes != nil {
		size := int(*value.SizeBytes)
		sizeBytes = &size
	}
	return generated.ReleaseCandidateFactsView{
		CoverageKeys:     value.CoverageKeys,
		CreatedAt:        value.CreatedAt,
		Facts:            value.Facts,
		Group:            value.Group,
		InputFingerprint: value.InputFingerprint,
		Language:         value.Language,
		ParserVersion:    value.ParserVersion,
		RawEvidence:      value.RawEvidence,
		ReleaseId:        generated.ID(value.ReleaseID),
		ReleaseVersion:   int(value.ReleaseVersion),
		Resolution:       value.Resolution,
		Revision:         value.Revision,
		SizeBytes:        sizeBytes,
		Source:           value.Source,
	}
}
