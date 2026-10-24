package fileidentity

import (
	"encoding/base64"
	"os"

	"bansoko/internal/library/domain"
)

// EvidenceUnavailableError reports that the platform or filesystem cannot
// provide an object handle. Callers may use errors.Is(err, ErrEvidenceUnavailable)
// to choose a fail-closed mutation policy.
type EvidenceUnavailableError struct {
	Err error
}

func (e EvidenceUnavailableError) Error() string {
	if e.Err == nil {
		return "file-handle evidence is unavailable"
	}
	return "file-handle evidence is unavailable: " + e.Err.Error()
}

func (e EvidenceUnavailableError) Unwrap() error { return e.Err }
func (e EvidenceUnavailableError) Is(target error) bool {
	_, ok := target.(EvidenceUnavailableError)
	return ok
}

var ErrEvidenceUnavailable = EvidenceUnavailableError{}

// HandleEvidenceFromFile returns an opaque object handle for an open regular
// file when the platform/filesystem provides one. Unsupported handle evidence
// is returned as ErrEvidenceUnavailable; ordinary observations may explicitly
// retain the other evidence fields and continue without a handle.
func HandleEvidenceFromFile(file *os.File) (*domain.HandleEvidence, error) {
	if file == nil {
		return nil, os.ErrInvalid
	}
	return platformHandleEvidenceFromFile(file)
}

// HandleEvidenceFromPath is retained for tests and path-adapter callers. Runtime
// observation should prefer HandleEvidenceFromFile to bind evidence to a descriptor.
func HandleEvidenceFromPath(path string) (*domain.HandleEvidence, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, os.ErrInvalid
	}
	return HandleEvidenceFromFile(file)
}

func encodeHandleEvidence(mountID int64, handleType int32, opaque []byte) *domain.HandleEvidence {
	return &domain.HandleEvidence{MountID: mountID, Type: handleType, Opaque: base64.RawStdEncoding.EncodeToString(opaque)}
}
