package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"bansoko/internal/runtime/buildinfo"
)

const KindBaseTakeover = "base_takeover"

// BaseAnchor is the immutable evidence bound into the exact takeover fence.
// Keep the fingerprint domain separate from signed overlay manifest digests.
type BaseAnchor struct {
	Platform       string              `json:"platform"`
	Architecture   string              `json:"architecture"`
	ExecutableHash string              `json:"executable_sha256"`
	ImageDigest    string              `json:"image_digest"`
	Build          buildinfo.BuildInfo `json:"build"`
}

func (a BaseAnchor) Fingerprint() string {
	b, _ := json.Marshal(struct {
		Platform, Architecture, ExecutableHash, ImageDigest    string
		Order                                                  buildinfo.Order
		ReleaseID, Version, Commit, Schema, API, Compatibility string
	}{a.Platform, a.Architecture, a.ExecutableHash, a.ImageDigest, a.Build.Order,
		a.Build.BuildID, a.Build.Version, a.Build.Commit, a.Build.SchemaVersion, a.Build.APIVersion, a.Build.BaseCompatibilityVersion})
	sum := sha256.Sum256(append([]byte("bansoko-base-anchor-v1\x00"), b...))
	return hex.EncodeToString(sum[:])
}

func (a BaseAnchor) Valid() bool {
	return a.Platform == "linux" && (a.Architecture == "amd64" || a.Architecture == "arm64") &&
		bareSHA(a.ExecutableHash) && strings.HasPrefix(a.ImageDigest, "sha256:") && bareSHA(strings.TrimPrefix(a.ImageDigest, "sha256:")) &&
		validOrderedBuild(a.Build) && !a.Build.Dirty && a.Build.APIVersion != "" && a.Build.BaseCompatibilityVersion != ""
}

type PreviousOverlay struct {
	ReleaseID     string              `json:"release_id"`
	ProducingOp   string              `json:"producing_operation_id"`
	PayloadDigest string              `json:"payload_digest"`
	Build         buildinfo.BuildInfo `json:"build"`
}

type TakeoverEvidence struct {
	TargetSource   string          `json:"target_source"`
	PreviousSource string          `json:"previous_source"`
	Anchor         BaseAnchor      `json:"anchor"`
	Previous       PreviousOverlay `json:"previous"`
}

type DatabaseRecovery struct {
	Live     string `json:"live"`
	Ready    string `json:"ready"`
	Aside    string `json:"aside"`
	LiveOID  uint32 `json:"live_oid"`
	ReadyOID uint32 `json:"ready_oid"`
	OwnerOID uint32 `json:"owner_oid"`
}

var takeoverTransitions = map[Step][]Step{
	StepPrepared:            {StepAccepted, StepFailed, StepRollbackBlocked},
	StepAccepted:            {StepMaintenanceEntered, StepFenceClosurePending, StepRollbackBlocked},
	StepMaintenanceEntered:  {StepBackupVerified, StepFenceClosurePending, StepRollbackBlocked},
	StepBackupVerified:      {StepPendingMigration, StepFenceClosurePending, StepRollbackBlocked},
	StepPendingMigration:    {StepMigrationStarted, StepFenceClosurePending, StepRollbackBlocked},
	StepMigrationStarted:    {StepMigrationSucceeded, StepRollbackStarted, StepRollbackBlocked},
	StepMigrationSucceeded:  {StepBaseReady, StepRollbackStarted, StepRollbackBlocked},
	StepBaseReady:           {StepPromoted, StepRollbackStarted, StepRollbackBlocked},
	StepPromoted:            {StepFenceClosurePending, StepRollbackStarted, StepRollbackBlocked},
	StepRollbackStarted:     {StepDatabaseRestored, StepRollbackBlocked},
	StepDatabaseRestored:    {StepFenceClosurePending, StepRollbackBlocked},
	StepFenceClosurePending: {StepSucceeded, StepRolledBack, StepFailed, StepRollbackBlocked},
	StepSucceeded:           nil,
	StepFailed:              nil,
	StepRolledBack:          nil,
	StepRollbackBlocked:     nil,
}

func (r Record) canAdvance(to Step) bool {
	if r.SchemaVersion != FormatVersion {
		return false
	}
	if r.OperationKind == KindBaseTakeover {
		return !Terminal(r.Step) && (r.Step == to || slices.Contains(takeoverTransitions[r.Step], to))
	}
	return CanAdvance(r.Step, to)
}

func bareSHA(value string) bool {
	return len(value) == 64 && validSHA256(value)
}

func validOrderedBuild(b buildinfo.BuildInfo) bool {
	return b.Ordered() && b.Series == buildinfo.CISeries && safeID(b.BuildID) && b.Version != "" && b.Version != "dev" &&
		b.Commit != "" && b.Commit != "unknown" && b.SchemaVersion != ""
}

func validateEnvelope(env envelope) error {
	r := env.Record
	if env.SchemaVersion == 1 && r == nil {
		return nil
	}
	if env.SchemaVersion == 1 && r != nil && r.SchemaVersion == 1 && r.OperationKind == "" && r.Takeover == nil && r.DatabaseRecovery == nil && r.BackupDigest == "" && r.InstallID == "" {
		return validateRecord(*r)
	}
	if env.SchemaVersion != FormatVersion || r != nil && r.SchemaVersion != FormatVersion {
		return errors.New("unsupported journal format")
	}
	if r != nil {
		return validateOperation(*r)
	}
	return nil
}

func validateOperation(r Record) error {
	if (r.BackupDigest == "") != (r.InstallID == "") || len(r.InstallID) > 128 || r.BackupDigest != "" && (!bareSHA(r.BackupDigest) || !ValidID(r.BackupID)) {
		return errors.New("invalid pinned backup")
	}
	if p := r.DatabaseRecovery; p != nil {
		if p.Live == "" || p.Ready == "" || p.Aside == "" || p.Live == p.Ready || p.Live == p.Aside || p.Ready == p.Aside ||
			p.LiveOID == 0 || p.ReadyOID == 0 || p.LiveOID == p.ReadyOID || p.OwnerOID == 0 || r.BackupDigest == "" {
			return errors.New("invalid pinned database cutover")
		}
	}
	switch r.OperationKind {
	case "", "overlay_update":
		if r.Takeover != nil || r.Step == StepBaseReady || r.Step == StepMigrationStarted {
			return errors.New("overlay journal cannot carry takeover evidence or checkpoints")
		}
		return nil
	case KindBaseTakeover:
	default:
		return errors.New("unsupported operation kind")
	}
	p := r.Takeover
	if p == nil || p.TargetSource != "base" || p.PreviousSource != "overlay" || !p.Anchor.Valid() ||
		!safeID(r.OperationID) || r.Channel != "embedded" || !r.RollbackSourceCaptured ||
		!safeID(p.Previous.ReleaseID) || !safeID(p.Previous.ProducingOp) || p.Previous.ProducingOp == r.OperationID ||
		!strings.HasPrefix(p.Previous.PayloadDigest, "sha256:") || !bareSHA(strings.TrimPrefix(p.Previous.PayloadDigest, "sha256:")) || !validOrderedBuild(p.Previous.Build) {
		return errors.New("incomplete takeover identity")
	}
	b := p.Anchor.Build
	if r.TargetReleaseID != b.BuildID || r.TargetReleaseID == p.Previous.ReleaseID || r.TargetVersion != b.Version || r.TargetSchemaVersion != b.SchemaVersion ||
		r.TargetLane != b.Lane || r.TargetReleaseSequence != b.ReleaseSequence || r.ManifestDigest != p.Anchor.Fingerprint() || r.PreviousReleaseID != p.Previous.ReleaseID ||
		b.Series != p.Previous.Build.Series || b.Lane != p.Previous.Build.Lane || b.ReleaseSequence <= p.Previous.Build.ReleaseSequence {
		return errors.New("contradictory takeover identity")
	}
	if _, ok := takeoverTransitions[r.Step]; !ok {
		return errors.New("unknown takeover checkpoint")
	}
	outcome := r.Step
	if r.Step == StepFenceClosurePending {
		if !validFenceClosureTarget(r.FenceClosureTarget) || r.FenceClosure != "" {
			return errors.New("takeover closure intent is incomplete or already closed")
		}
		outcome = r.FenceClosureTarget
	}
	if outcome == StepSucceeded && r.CurrentReleaseID != "" ||
		(outcome == StepRolledBack || outcome == StepFailed) && r.CurrentReleaseID != r.PreviousReleaseID {
		return errors.New("takeover pointer contradicts outcome")
	}
	if (r.Step == StepDatabaseRestored || outcome == StepRolledBack) && r.DatabaseRecovery == nil {
		return errors.New("restored takeover lacks its pinned cutover")
	}
	switch outcome {
	case StepBackupVerified, StepPendingMigration, StepMigrationStarted, StepMigrationSucceeded, StepBaseReady, StepPromoted,
		StepRollbackStarted, StepDatabaseRestored, StepSucceeded, StepRolledBack:
		if r.BackupID != "embedded-"+r.OperationID || !bareSHA(r.BackupDigest) {
			return errors.New("takeover lacks its operation-owned backup proof")
		}
	}
	if Terminal(r.Step) && r.Step != StepRollbackBlocked {
		if r.FenceClosureTarget != "" && r.FenceClosureTarget != r.Step {
			return errors.New("takeover terminal outcome contradicts closure intent")
		}
		return validateClosureProof(r)
	}
	return nil
}

func validateOperationAdvance(before, after Record) error {
	if before.SchemaVersion != after.SchemaVersion || before.OperationKind != after.OperationKind || before.Step != StepRollbackStarted && before.DatabaseRecovery == nil && after.DatabaseRecovery != nil {
		return errors.New("operation format or database recovery changed outside its checkpoint")
	}
	if before.DatabaseRecovery != nil && (after.DatabaseRecovery == nil || *before.DatabaseRecovery != *after.DatabaseRecovery) {
		return errors.New("pinned database cutover is immutable")
	}
	if before.BackupDigest != "" && (after.BackupDigest != before.BackupDigest || after.BackupID != before.BackupID || after.InstallID != before.InstallID) {
		return errors.New("pinned backup is immutable")
	}
	if before.OperationKind == KindBaseTakeover {
		if after.Takeover == nil || *before.Takeover != *after.Takeover || before.OperationID != after.OperationID {
			return errors.New("takeover intent is immutable")
		}
		if before.Step == StepFenceClosurePending {
			if after.FenceClosureTarget != before.FenceClosureTarget {
				return errors.New("takeover closure intent is immutable")
			}
		}
		if after.Step == StepFenceClosurePending && before.Step != StepFenceClosurePending {
			want := StepFailed
			switch before.Step {
			case StepPromoted:
				want = StepSucceeded
			case StepDatabaseRestored:
				want = StepRolledBack
			}
			if after.FenceClosureTarget != want {
				return fmt.Errorf("checkpoint %s cannot close as %s", before.Step, after.FenceClosureTarget)
			}
		}
	}
	return validateOperation(after)
}
