package application

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"path/filepath"
	"sort"
	"strings"

	"bansoko/internal/fileidentity"
	"bansoko/internal/library/domain"
)

type overwriteExecutionSnapshot struct {
	ProtocolVersion string                   `json:"protocol_version"`
	OperationID     string                   `json:"operation_id"`
	PlanDigest      string                   `json:"plan_digest"`
	Recovery        domain.OverwriteRecovery `json:"recovery"`
}

func operationPlanDigestForPlan(plan operationPlan) string {
	return frozenPlanDigest(
		plan.id,
		plan.operation,
		plan.collisionPolicy,
		plan.sourcePath,
		plan.targetPath,
		plan.sourceFingerprint,
		plan.renderSnapshot,
		plan.configSnapshot,
	)
}

func frozenPlanDigest(id, operation, collisionPolicy, sourcePath, targetPath string, sourceFingerprint, renderSnapshot, configSnapshot []byte) string {
	hash := sha256.New()
	for index, value := range [][]byte{
		[]byte(id), []byte(operation), []byte(collisionPolicy),
		[]byte(sourcePath), []byte(targetPath), sourceFingerprint,
		renderSnapshot, configSnapshot,
	} {
		if index >= 5 {
			value = canonicalDigestJSON(value)
		}
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func canonicalDigestJSON(raw []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return raw
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return raw
	}
	canonical, err := canonicalDigestValue(value)
	if err != nil {
		return raw
	}
	return canonical
}

func canonicalDigestValue(value any) ([]byte, error) {
	switch typed := value.(type) {
	case json.Number:
		rational := new(big.Rat)
		if _, ok := rational.SetString(string(typed)); !ok {
			return nil, fmt.Errorf("invalid JSON number %q", typed)
		}
		return []byte("number:" + rational.Num().String() + "/" + rational.Denom().String()), nil
	case []any:
		parts := make([][]byte, len(typed))
		for index, item := range typed {
			canonical, err := canonicalDigestValue(item)
			if err != nil {
				return nil, err
			}
			parts[index] = canonical
		}
		return json.Marshal(parts)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var builder strings.Builder
		builder.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				builder.WriteByte(',')
			}
			encodedKey, _ := json.Marshal(key)
			encodedValue, err := canonicalDigestValue(typed[key])
			if err != nil {
				return nil, err
			}
			builder.Write(encodedKey)
			builder.WriteByte(':')
			builder.Write(encodedValue)
		}
		builder.WriteByte('}')
		return []byte(builder.String()), nil
	default:
		return json.Marshal(value)
	}
}

func encodeOverwriteExecutionSnapshotForPlan(operationID, planDigest string, recovery domain.OverwriteRecovery) ([]byte, error) {
	if strings.TrimSpace(operationID) == "" {
		return nil, errors.New("overwrite checkpoint operation id is required")
	}
	if !validFingerprintText(planDigest) {
		return nil, errors.New("overwrite checkpoint plan digest is required")
	}
	if err := validateOverwriteRecovery(recovery); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(overwriteExecutionSnapshot{
		ProtocolVersion: overwriteExecutionProtocolVersion,
		OperationID:     operationID,
		PlanDigest:      planDigest,
		Recovery:        recovery,
	})
	if err != nil {
		return nil, fmt.Errorf("encode overwrite execution snapshot: %w", err)
	}
	if len(raw) > 65536 {
		return nil, errors.New("overwrite execution snapshot exceeds the size limit")
	}
	return raw, nil
}

func validateParentIdentityPair(source, target domain.FileIdentity) error {
	if !fileidentity.IsPresent(source) || !fileidentity.IsPresent(target) {
		return errors.New("overwrite checkpoint source and target parent identities are required")
	}
	return nil
}

func validateOverwriteRecovery(recovery domain.OverwriteRecovery) error {
	if err := validateParentIdentityPair(recovery.SourceParentIdentity, recovery.TargetParentIdentity); err != nil {
		return err
	}
	switch recovery.Stage {
	case domain.OverwriteStagePrepared, domain.OverwriteStageStaged, domain.OverwriteStageExchanged,
		domain.OverwriteStageRestored, domain.OverwriteStageBlocked, domain.OverwriteStageAborted, domain.OverwriteStageCommitted:
	default:
		return fmt.Errorf("invalid overwrite checkpoint stage %q", recovery.Stage)
	}
	for name, value := range map[string]string{
		"canonical root":   recovery.CanonicalRoot,
		"canonical source": recovery.CanonicalSource,
		"canonical target": recovery.CanonicalTarget,
		"staging path":     recovery.StagingPath,
	} {
		if name == "canonical root" && value == "" {
			continue
		}
		if strings.TrimSpace(value) == "" || len(value) > maxPathLength || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("overwrite checkpoint %s is invalid", name)
		}
	}
	for name, observation := range map[string]domain.FileObservation{
		"source":    recovery.SourceObservation,
		"target":    recovery.TargetObservation,
		"staged":    recovery.StagedObservation,
		"displaced": recovery.DisplacedObservation,
		"foreign":   recovery.ForeignEvidence,
	} {
		if observation.Fingerprint != "" && !validFingerprintText(observation.Fingerprint) {
			return fmt.Errorf("overwrite checkpoint %s fingerprint is invalid", name)
		}
		if observation.SizeBytes < 0 {
			return fmt.Errorf("overwrite checkpoint %s size is invalid", name)
		}
		if !fileidentity.IsValid(observation.Identity) {
			return fmt.Errorf("overwrite checkpoint %s identity is incomplete", name)
		}
	}
	if recovery.SourceObservation.Fingerprint == "" || recovery.TargetObservation.Fingerprint == "" ||
		!fileidentity.IsPresent(recovery.SourceObservation.Identity) || !fileidentity.IsPresent(recovery.TargetObservation.Identity) {
		return errors.New("overwrite checkpoint source and target observations with identity are required")
	}
	if recovery.Stage != domain.OverwriteStagePrepared &&
		(recovery.StagedObservation.Fingerprint == "" || !fileidentity.IsPresent(recovery.StagedObservation.Identity)) {
		return errors.New("overwrite checkpoint staged observation with identity is required after prepare")
	}
	if recovery.Stage == domain.OverwriteStagePrepared {
		if recovery.StagedObservation != (domain.FileObservation{}) {
			return errors.New("overwrite checkpoint staged evidence is not valid at prepared stage")
		}
		if recovery.DisplacedObservation != (domain.FileObservation{}) || recovery.DisplacedNonRegular ||
			recovery.DisplacedType != "" || !fileidentity.IsEmpty(recovery.DisplacedIdentity) {
			return fmt.Errorf("overwrite checkpoint displaced evidence is not valid at %s stage", recovery.Stage)
		}
	}
	if recovery.Stage == domain.OverwriteStageStaged || recovery.Stage == domain.OverwriteStageAborted {
		if recovery.DisplacedObservation != (domain.FileObservation{}) || recovery.DisplacedNonRegular ||
			recovery.DisplacedType != "" || !fileidentity.IsEmpty(recovery.DisplacedIdentity) {
			return fmt.Errorf("overwrite checkpoint displaced evidence is not valid at %s stage", recovery.Stage)
		}
	}
	if recovery.DisplacedNonRegular {
		if recovery.DisplacedObservation != (domain.FileObservation{}) || strings.TrimSpace(recovery.DisplacedType) == "" || !fileidentity.IsPresent(recovery.DisplacedIdentity) {
			return errors.New("overwrite checkpoint non-regular displaced evidence is incomplete")
		}
	} else {
		if recovery.DisplacedType != "" || !fileidentity.IsEmpty(recovery.DisplacedIdentity) {
			return errors.New("overwrite checkpoint regular displaced evidence is malformed")
		}
		if recovery.Stage == domain.OverwriteStageExchanged || recovery.Stage == domain.OverwriteStageRestored ||
			recovery.Stage == domain.OverwriteStageBlocked || recovery.Stage == domain.OverwriteStageCommitted {
			if recovery.DisplacedObservation.Fingerprint == "" || !fileidentity.IsPresent(recovery.DisplacedObservation.Identity) {
				return fmt.Errorf("overwrite checkpoint %s stage requires displaced observation", recovery.Stage)
			}
		}
	}
	if recovery.Stage == domain.OverwriteStageRestored || recovery.Stage == domain.OverwriteStageBlocked {
		if recovery.DisplacedNonRegular {
			if strings.TrimSpace(recovery.DisplacedType) == "" || !fileidentity.IsPresent(recovery.DisplacedIdentity) {
				return errors.New("overwrite checkpoint restored non-regular displaced evidence is incomplete")
			}
		} else if recovery.DisplacedObservation.Fingerprint == "" || !fileidentity.IsPresent(recovery.DisplacedObservation.Identity) {
			return errors.New("overwrite checkpoint restored displaced observation is required")
		}
		if recovery.Stage == domain.OverwriteStageBlocked && strings.TrimSpace(recovery.BlockedReason) == "" {
			return errors.New("overwrite checkpoint blocked reason is required")
		}
	} else if recovery.BlockedReason != "" {
		return errors.New("overwrite checkpoint blocked reason is only valid for blocked stage")
	}
	if recovery.ForeignEvidencePath != "" {
		if recovery.Stage != domain.OverwriteStageExchanged && recovery.Stage != domain.OverwriteStageRestored && recovery.Stage != domain.OverwriteStageBlocked {
			return errors.New("overwrite checkpoint foreign evidence is only valid for exchanged, restored, or blocked stage")
		}
		if filepath.Clean(recovery.ForeignEvidencePath) != recovery.ForeignEvidencePath || len(recovery.ForeignEvidencePath) > maxPathLength {
			return errors.New("overwrite checkpoint foreign evidence path is invalid")
		}
		if recovery.Stage == domain.OverwriteStageExchanged {
			if recovery.ForeignEvidencePath != recovery.StagingPath {
				return errors.New("overwrite checkpoint exchanged foreign evidence must remain at staging")
			}
		} else if recovery.ForeignEvidencePath != overwriteCheckpointForeignEvidencePath(recovery.StagingPath) {
			return errors.New("overwrite checkpoint foreign evidence path is invalid")
		}
		if recovery.ForeignNonRegularEvidence {
			if recovery.ForeignEvidence != (domain.FileObservation{}) || strings.TrimSpace(recovery.ForeignEvidenceType) == "" || !fileidentity.IsPresent(recovery.ForeignEvidenceIdentity) {
				return errors.New("overwrite checkpoint non-regular foreign evidence is incomplete")
			}
		} else if recovery.ForeignEvidence.Fingerprint == "" || !fileidentity.IsPresent(recovery.ForeignEvidence.Identity) {
			return errors.New("overwrite checkpoint foreign evidence is incomplete")
		} else if recovery.ForeignEvidenceType != "" || !fileidentity.IsEmpty(recovery.ForeignEvidenceIdentity) {
			return errors.New("overwrite checkpoint regular foreign evidence is malformed")
		}
	} else if recovery.ForeignEvidence != (domain.FileObservation{}) || recovery.ForeignNonRegularEvidence || recovery.ForeignEvidenceType != "" || !fileidentity.IsEmpty(recovery.ForeignEvidenceIdentity) {
		return errors.New("overwrite checkpoint foreign evidence path is required")
	}
	return nil
}

func overwriteCheckpointForeignEvidencePath(stagingPath string) string {
	sum := sha256.Sum256([]byte(stagingPath))
	return filepath.Join(filepath.Dir(stagingPath), ".bansoko-quarantine-"+hex.EncodeToString(sum[:12]))
}
