package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// View is the transport-neutral read model for one versioned candidate-facts row.
type View struct {
	ReleaseID        string
	ReleaseVersion   int64
	ParserVersion    string
	InputFingerprint string
	RawEvidence      *map[string]any
	Facts            map[string]any
	CoverageKeys     []string
	Resolution       *string
	Revision         *string
	Source           *string
	Language         *string
	Group            *string
	SizeBytes        *int64
	CreatedAt        *time.Time
}

// Service adapts the repository to the HTTP read contract.
type Service struct {
	repo Repository
}

// NewService wires the read adapter.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// GetFacts returns the requested (or latest) facts version.
func (s *Service) GetFacts(ctx context.Context, releaseID string, version *int) (View, error) {
	var (
		facts *ReleaseCandidateFacts
		err   error
	)
	if version != nil {
		facts, err = s.repo.GetVersion(ctx, releaseID, int64(*version))
	} else {
		facts, err = s.repo.GetForRelease(ctx, releaseID)
	}
	if err != nil {
		return View{}, err
	}
	if facts == nil {
		return View{}, &factsNotFound{id: releaseID}
	}
	var rawEvidence, factsMap map[string]any
	_ = json.Unmarshal(facts.RawEvidence, &rawEvidence)
	_ = json.Unmarshal(facts.Facts, &factsMap)
	return View{
		ReleaseID:        facts.ReleaseID,
		ReleaseVersion:   facts.ReleaseVersion,
		ParserVersion:    facts.ParserVersion,
		InputFingerprint: facts.InputFingerprint,
		RawEvidence:      &rawEvidence,
		Facts:            factsMap,
		CoverageKeys:     facts.CoverageKeys,
		Resolution:       nullableStr(facts.Resolution),
		Revision:         nullableStr(facts.Revision),
		Source:           nullableStr(facts.Source),
		Language:         nullableStr(facts.Language),
		Group:            nullableStr(facts.Group),
		SizeBytes:        facts.SizeBytes,
		CreatedAt:        &facts.CreatedAt,
	}, nil
}

type factsNotFound struct{ id string }

func (e *factsNotFound) Error() string { return fmt.Sprintf("candidate facts not found: %s", e.id) }

func nullableStr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
