package admission

import (
	"context"
	"errors"
	"strings"

	"bansoko/internal/download/domain"
)

var (
	ErrInvalidRequest   = errors.New("qB admission request is invalid")
	ErrAmbiguousMatch   = errors.New("qB admission match is ambiguous")
	ErrConflictingMatch = errors.New("qB admission match conflicts with frozen facts")
)

type Matcher interface {
	MatchAdmission(context.Context, Request) (Result, error)
}

type Request struct {
	OperationKey string
	InfoHash     string
	InfoHashV1   string
	InfoHashV2   string
}

type Result struct {
	PathSnapshot domain.PathSnapshot
	Provenance   *Provenance
}

type Provenance struct {
	Kind                string `json:"kind"`
	ProviderTaskID      string `json:"provider_task_id"`
	ProviderInfoHash    string `json:"provider_info_hash"`
	OperationKeyChecked string `json:"operation_key_checked"`
}

func (r Request) HashCandidates() []string {
	values := []string{r.InfoHash, r.InfoHashV1, r.InfoHashV2}
	if len(strings.TrimSpace(r.InfoHashV2)) >= 40 {
		values = append(values, strings.TrimSpace(r.InfoHashV2)[:40])
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
