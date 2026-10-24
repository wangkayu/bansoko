//go:build darwin || (!darwin && !linux)

package fileidentity

import (
	"os"

	"bansoko/internal/library/domain"
)

func platformHandleEvidenceFromFile(_ *os.File) (*domain.HandleEvidence, error) {
	return nil, EvidenceUnavailableError{}
}
