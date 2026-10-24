package application

import (
	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/library/domain"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type preparedSatisfaction struct {
	observation domain.PathObservation
	kind        string
}

// errPlanCaptureChanged and errTargetObservationUnavailable are retryable
// planning causes: the source changed under the plan, or the target could not
// be observed for a transient reason (deadline, cancellation, EAGAIN-like).
var (
	errPlanCaptureChanged           = errors.New("plan capture changed")
	errTargetObservationUnavailable = errors.New("target observation unavailable")
)

// observePlanSatisfaction applies path-level verification: an
// existing regular file at the target with the expected size already
// satisfies the delivery and is adopted (or retained for noop) without a
// write. Content is never re-read; the downloader's piece verification is the
// content proof.
func (s *Service) observePlanSatisfaction(ctx context.Context, b *artifactPreparedBatch, p *operationPlan, file artifactFileContext) error {
	var in struct {
		Delivery struct {
			Operation string `json:"operation"`
			Intent    string `json:"intent"`
			Collision string `json:"collision"`
			Cleanup   struct {
				Mode string `json:"mode"`
			} `json:"source_cleanup"`
		} `json:"delivery"`
	}
	if p.failureCode != "" {
		return nil
	}
	if err := json.Unmarshal(b.input, &in); err != nil {
		return err
	}
	if in.Delivery.Operation == "move" || in.Delivery.Intent == "replace" || in.Delivery.Cleanup.Mode != "none" {
		return s.checkHardlinkDevice(ctx, p, file)
	}
	// An overwrite delivery's per-target collision is the plan's own: an
	// absent target was downgraded to block and keeps it, and a present
	// unmanaged target is replaced by the rename, never adopted.
	if in.Delivery.Collision == "overwrite" {
		if p.collisionPolicy == "overwrite" {
			return s.checkHardlinkDevice(ctx, p, file)
		}
		p.operation, p.intent = in.Delivery.Operation, in.Delivery.Intent
	} else {
		p.operation, p.intent, p.collisionPolicy = in.Delivery.Operation, in.Delivery.Intent, in.Delivery.Collision
	}
	observer, ok := s.observer.(domain.PathObserver)
	fail := func(code string) { p.failureCode = code; p.failureReason = code }
	if !ok { // Absence remains independently known, even on unsupported observers.
		if _, err := os.Lstat(p.targetPath); errors.Is(err, os.ErrNotExist) {
			if p.operation == "noop" {
				fail("TARGET_ABSENT")
				return nil
			}
			return s.checkHardlinkDevice(ctx, p, file)
		}
		fail(domain.CodeTargetUnreadable)
		return nil
	}
	target, err := observer.ObservePath(ctx, p.targetPath)
	if errors.Is(err, os.ErrNotExist) {
		if p.operation == "noop" {
			fail("TARGET_ABSENT")
			return nil
		}
		return s.checkHardlinkDevice(ctx, p, file)
	}
	if err != nil {
		if transientObservation(err) {
			return fmt.Errorf("%w: %w", errTargetObservationUnavailable, err)
		}
		fail(domain.CodeTargetUnreadable)
		return nil
	}
	if target.SizeBytes != file.sizeBytes {
		if p.operation == "noop" {
			fail(domain.CodeTargetSizeMismatch)
		} else if p.collisionPolicy == "block" {
			fail("TARGET_OCCUPIED")
			p.failureReason = domain.CodeTargetSizeMismatch
		}
		return s.checkHardlinkDevice(ctx, p, file)
	}
	kind := "adopt"
	if p.operation == "noop" {
		kind = "retain"
	}
	if b.satisfactions == nil {
		b.satisfactions = map[string]preparedSatisfaction{}
	}
	b.satisfactions[file.fileRef] = preparedSatisfaction{target, kind}
	return nil
}

// checkHardlinkDevice is the plan-time device check: a hardlink whose source
// and target directory are on different devices fails as
// HARDLINK_CROSS_DEVICE before any execution, and never falls back to copy.
// An unanswerable check is left to execution, which maps EXDEV the same way.
func (s *Service) checkHardlinkDevice(ctx context.Context, p *operationPlan, file artifactFileContext) error {
	if p.failureCode != "" || p.operation != "hardlink" {
		return nil
	}
	observer, ok := s.observer.(domain.DeviceObserver)
	if !ok {
		return nil
	}
	same, err := observer.SameDevice(ctx, file.sourcePath, p.targetPath)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%w: %w", errTargetObservationUnavailable, ctxErr)
		}
		return nil
	}
	if !same {
		p.failureCode, p.failureReason = domain.CodeHardlinkCrossDevice, domain.CodeHardlinkCrossDevice
	}
	return nil
}

func transientObservation(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var temporary interface{ Temporary() bool }
	return errors.As(err, &temporary) && temporary.Temporary()
}

// satisfactionEvidence is PathSatisfactionV1: the target location, its
// observed size and identity, and the plan coordinates it satisfies.
func satisfactionEvidence(b *artifactPreparedBatch, file artifactFileContext, p operationPlan, s preparedSatisfaction, spec string) (map[string]any, error) {
	var capture struct {
		Artifact struct {
			Root string `json:"acquisition_id"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(b.capture.Facts, &capture); err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(stableReadRoot(), p.targetPath)
	if err != nil {
		return nil, err
	}
	namespace, err := canonicalPlanValue(map[string]any{"root": stableReadRoot(), "adapter_version": "local-stable-read-v1"})
	if err != nil {
		return nil, err
	}
	object := pathObject(map[string]any{"namespace_digest": protocol.Digest("local-filesystem-namespace:v1", []byte(namespace)), "root": stableReadRoot(), "relative_path": rel, "canonical_path": p.targetPath}, s.observation)
	evidence := map[string]any{"schema_version": 2, "basis": "path_size", "acquisition_id": capture.Artifact.Root, "plan_id": b.capture.PlanID, "artifact_id": b.capture.ArtifactID, "artifact_version": b.capture.ArtifactVersion, "file_ref": file.fileRef, "input_digest": b.capture.PlanInputDigest, "spec_digest": spec, "expected_size": file.sizeBytes, "observed_object": object}
	canonical, err := canonicalPlanValue(evidence)
	if err != nil {
		return nil, err
	}
	v, err := protocol.ParseStrict([]byte(canonical))
	if err != nil {
		return nil, err
	}
	if err := protocol.ValidateObservedSatisfaction(v); err != nil {
		return nil, err
	}
	wrapper := map[string]any{"canonical": canonical, "digest": protocol.Digest("acquisition-observed-satisfaction:v1", []byte(canonical)), "observation": evidence, "final_object": object}
	return wrapper, nil
}

// adoptReplacementSidecar applies the replacement ruling: under a
// replacement delivery the single approved incumbent stays the video, and a
// subtitle whose target already holds a file of the exact expected size is
// adopted without a write instead of failing the whole plan. Any other
// existing target is not adopted.
func (s *Service) adoptReplacementSidecar(ctx context.Context, b *artifactPreparedBatch, p *operationPlan, file artifactFileContext) (bool, error) {
	if file.mediaKind != "subtitle" {
		return false, nil
	}
	var in struct {
		Delivery struct {
			Operation string `json:"operation"`
			Cleanup   struct {
				Mode string `json:"mode"`
			} `json:"source_cleanup"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal(b.input, &in); err != nil {
		return false, err
	}
	if in.Delivery.Operation == "move" || in.Delivery.Cleanup.Mode != "none" {
		return false, nil
	}
	observer, ok := s.observer.(domain.PathObserver)
	if !ok {
		return false, nil
	}
	target, err := observer.ObservePath(ctx, p.targetPath)
	if err != nil || target.SizeBytes != file.sizeBytes {
		return false, nil
	}
	*p = downgradeAbsentOverwrite(*p)
	if b.satisfactions == nil {
		b.satisfactions = map[string]preparedSatisfaction{}
	}
	b.satisfactions[file.fileRef] = preparedSatisfaction{target, "adopt"}
	return true, nil
}
