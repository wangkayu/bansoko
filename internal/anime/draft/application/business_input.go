package application

import (
	animeapplication "bansoko/internal/anime/application"
	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/apperrors"
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
)

// Resolve a submitted frozen business value without querying current Global.
// The digest is provenance of the submitted content, not permission to reread it.
func submittedBusiness(draft animedomain.AnimeDraft) (*animedomain.BusinessSnapshot, error) {
	if draft.Business == nil {
		if draft.BusinessDigest != nil {
			return nil, businessInputError("digest requires business snapshot")
		}
		return nil, nil
	}
	value, err := draft.Business.Freeze()
	if err != nil {
		return nil, businessInputError(err.Error())
	}
	digest, err := value.Digest()
	if err != nil {
		return nil, err
	}
	if draft.BusinessDigest == nil || *draft.BusinessDigest != digest {
		return nil, businessInputError("business digest does not match submitted snapshot")
	}
	return &value, nil
}
func businessInputError(message string) error {
	return &apperrors.Business{Status: 422, Code: "BUSINESS_SNAPSHOT_INVALID", Message: message}
}
func (s *Service) resolveCandidateInputTx(ctx context.Context, tx *sql.Tx, draft animedomain.AnimeDraft) (animedomain.ResolvedSections, animedomain.ParserPolicyEnvelope, error) {
	business, err := submittedBusiness(draft)
	if err != nil {
		return animedomain.ResolvedSections{}, animedomain.ParserPolicyEnvelope{}, err
	}
	if business == nil {
		// Every draft carries the complete business it copied from Global when
		// it was created; the save never fills one in from the current Global.
		return animedomain.ResolvedSections{}, animedomain.ParserPolicyEnvelope{}, businessInputError("draft must carry a complete business snapshot")
	}
	// The draft's own section values are what it shows and previews; the
	// snapshot must carry exactly those values, or the save fails closed.
	drafted, err := animedomain.ResolveDraftSectionsFromBusiness(draft, nil)
	if err != nil {
		return animedomain.ResolvedSections{}, animedomain.ParserPolicyEnvelope{}, sectionResolutionError(err)
	}
	for _, name := range animeapplication.RulesSectionOrder {
		if !jsonValuesEqual(drafted.Sections[name], business.Sections[name]) {
			return animedomain.ResolvedSections{}, animedomain.ParserPolicyEnvelope{}, businessInputError("rules section " + name + " differs from the draft's business snapshot")
		}
	}
	resolved := animedomain.ResolvedSections{Sections: business.Sections, Versions: business.SectionVersions, SchemaVersions: business.SectionSchemas}
	return resolved, business.Parser, nil
}

// jsonValuesEqual compares two JSON documents by value, so key order and
// whitespace cannot make equal sections look different.
func jsonValuesEqual(left, right json.RawMessage) bool {
	var l, r any
	if json.Unmarshal(left, &l) != nil || json.Unmarshal(right, &r) != nil {
		return false
	}
	return reflect.DeepEqual(l, r)
}
func businessJSON(value *animedomain.BusinessSnapshot) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}
