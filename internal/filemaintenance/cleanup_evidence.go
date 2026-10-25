package filemaintenance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"

	"bansoko/internal/fileidentity"
	executorpkg "bansoko/internal/filemaintenance/executor"
	"bansoko/internal/library/domain"
)

type cleanupAttemptEvidence struct {
	CleanupID              string                 `json:"cleanup_id"`
	Effect                 string                 `json:"effect"`
	Stage                  string                 `json:"stage"`
	SourcePath             string                 `json:"source_path"`
	SourceFingerprint      string                 `json:"source_fingerprint"`
	QuarantinePath         string                 `json:"quarantine_path"`
	SourceObservation      domain.FileObservation `json:"source_observation"`
	QuarantineObservation  domain.FileObservation `json:"quarantine_observation"`
	Removed                bool                   `json:"removed"`
	PostObservationMissing bool                   `json:"post_observation_missing"`
}

func decodeCleanupAttempt(raw []byte) (cleanupAttemptEvidence, error) {
	if len(raw) == 0 {
		return cleanupAttemptEvidence{}, errors.New("cleanup attempt is missing")
	}
	var evidence cleanupAttemptEvidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return cleanupAttemptEvidence{}, err
	}
	return evidence, nil
}

func completeCleanupObservation(observation domain.FileObservation, fingerprint string) bool {
	return cleanupObservationMatchesFingerprint(observation, fingerprint) &&
		observation.SizeBytes >= 0 && fileidentity.IsPresent(observation.Identity)
}

// observeCleanupFile uses the observation mode of the frozen identity. The
// legacy SHA-256 path is unchanged. A recovered placement instead freezes a
// metadata envelope, so its cleanup must use no-follow path metadata and must
// never turn into a full-file content read merely to make the comparison work.
func observeCleanupFile(ctx context.Context, executor executorpkg.CleanupFileExecutor, path, fingerprint string) (domain.FileObservation, error) {
	if _, metadata := metadataFingerprintValue(fingerprint); metadata {
		observer, ok := executor.(executorpkg.CleanupMetadataFileObserver)
		if !ok {
			return domain.FileObservation{}, errors.New("cleanup metadata observer is not wired")
		}
		return observer.ObserveMetadataFile(ctx, path)
	}
	return executor.Observe(ctx, path)
}

func cleanupObservationMatchesFingerprint(observation domain.FileObservation, frozen string) bool {
	if expectedValue, metadata := metadataFingerprintValue(frozen); metadata {
		expected, ok := domain.ParseMetadataFingerprint(expectedValue)
		if !ok {
			return false
		}
		actual, ok := domain.ParseMetadataFingerprint(observation.Fingerprint)
		return ok && observation.SizeBytes == expected.SizeBytes && domain.MetadataFactsMatch(actual, expected)
	}
	return observation.Fingerprint != "" && observation.Fingerprint == frozen
}

func frozenSourceObservation(raw []byte, fingerprint string) (domain.FileObservation, error) {
	evidence, err := decodeCleanupAttempt(raw)
	if err != nil {
		return domain.FileObservation{}, err
	}
	if !completeCleanupObservation(evidence.SourceObservation, fingerprint) {
		return domain.FileObservation{}, errors.New("cleanup attempt lacks complete frozen source evidence")
	}
	return evidence.SourceObservation, nil
}

func frozenObservation(raw []byte, fingerprint string) (domain.FileObservation, error) {
	evidence, err := decodeCleanupAttempt(raw)
	if err != nil {
		return domain.FileObservation{}, err
	}
	observation := evidence.SourceObservation
	if !completeCleanupObservation(observation, fingerprint) {
		observation = evidence.QuarantineObservation
	}
	if !completeCleanupObservation(observation, fingerprint) {
		return domain.FileObservation{}, errors.New("cleanup attempt lacks complete frozen file evidence")
	}
	return observation, nil
}

func sameFrozenCleanupObservation(actual, expected domain.FileObservation) bool {
	metadata := domain.IsMetadataFingerprint(actual.Fingerprint) || domain.IsMetadataFingerprint(expected.Fingerprint)
	if metadata {
		if !cleanupObservationMatchesFingerprint(actual, expected.Fingerprint) || actual.SizeBytes != expected.SizeBytes {
			return false
		}
		// Network mounts may change their client-side device number. Path-level
		// cleanup identity is size + inode; device never participates.
		if !fileidentity.IsPresent(actual.Identity) || !fileidentity.IsPresent(expected.Identity) || actual.Identity.Inode != expected.Identity.Inode {
			return false
		}
		return actual.Identity.GenerationNanos == 0 || expected.Identity.GenerationNanos == 0 || actual.Identity.GenerationNanos == expected.Identity.GenerationNanos
	}
	return actual.Fingerprint == expected.Fingerprint && actual.SizeBytes == expected.SizeBytes && fileidentity.Matches(actual.Identity, expected.Identity)
}

func validateCleanupAttempt(evidence cleanupAttemptEvidence, cleanupID, effect, sourcePath, sourceFingerprint string, quarantinePath sql.NullString) error {
	if evidence.CleanupID != "" && evidence.CleanupID != cleanupID {
		return errors.New("cleanup attempt identity conflicts with owner")
	}
	if evidence.Effect != "" && evidence.Effect != effect {
		return errors.New("cleanup attempt effect conflicts with owner")
	}
	if evidence.Stage != "" && evidence.Stage != "quarantine" && evidence.Stage != "purge" {
		return errors.New("cleanup attempt stage is invalid")
	}
	if effect == "quarantine" && evidence.Stage == "purge" && evidence.Removed && !evidence.PostObservationMissing {
		return errors.New("cleanup purge attempt lacks post-effect absence proof")
	}
	if evidence.SourcePath != "" && evidence.SourcePath != sourcePath {
		return errors.New("cleanup attempt source path conflicts with owner")
	}
	if evidence.SourceFingerprint != "" && evidence.SourceFingerprint != sourceFingerprint {
		return errors.New("cleanup attempt source fingerprint conflicts with owner")
	}
	if evidence.QuarantinePath != "" && quarantinePath.Valid && evidence.QuarantinePath != quarantinePath.String {
		return errors.New("cleanup attempt quarantine path conflicts with owner")
	}
	return nil
}

func cleanupQuarantinePath(evidence cleanupAttemptEvidence, ownerPath sql.NullString) (string, error) {
	if evidence.QuarantinePath != "" {
		return evidence.QuarantinePath, nil
	}
	if ownerPath.Valid && ownerPath.String != "" {
		return ownerPath.String, nil
	}
	return "", errors.New("cleanup quarantine path evidence is missing")
}

func isMissingCleanupError(err error) bool {
	return err != nil && errors.Is(err, os.ErrNotExist)
}
