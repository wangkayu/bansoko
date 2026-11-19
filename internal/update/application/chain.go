package application

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	hardeningdomain "bansoko/internal/hardening/domain"
	observabilityport "bansoko/internal/observability/port"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/update/packagemaker"
)

const (
	FixtureStopAfterEnv        = "BANSOKO_EMBEDDED_FIXTURE_STOP_AFTER"
	FixtureStopBeforeMarkerEnv = "BANSOKO_EMBEDDED_FIXTURE_STOP_BEFORE_MARKER"
	FixtureStopExitCode        = 70
)

var fixtureStopExit = os.Exit
var (
	beforePayloadPublish            = func(string) {}
	afterPayloadPrivateVerification = func(string) {}
	beforePayloadRename             = func(string, string) {}
	afterPayloadBackup              = func(string) {}
)

func FixtureStopAfter(step string) bool {
	return strings.TrimSpace(os.Getenv("BANSOKO_ENVIRONMENT")) != "production" && strings.TrimSpace(os.Getenv(FixtureStopAfterEnv)) == step
}

// ApplyFixtureStop terminates the process when the durable checkpoint just
// written matches FixtureStopAfterEnv. The caller must only invoke it after the
// effect it names is durable, so the crash leaves exactly the state a real
// crash in that window would. The banner is part of the fixture contract: the
// real-Docker matrix detects the staging window by reading it from the app log,
// so the text must stay stable.
func ApplyFixtureStop(step string) {
	if !FixtureStopAfter(step) {
		return
	}
	fmt.Fprintf(os.Stderr, "[embedded-update] FIXTURE INJECTION: durable checkpoint %q matches %s; exiting %d to simulate a process crash in this window (non-production only)\n",
		step, FixtureStopAfterEnv, FixtureStopExitCode)
	fixtureStopExit(FixtureStopExitCode)
}

// applyFixtureStopBeforeMarker is the pre-marker crash window: the release tree
// is published but no pending marker exists, so the next start only has to
// discard the unreferenced work. It has its own banner because that window has
// no durable checkpoint to name.
func applyFixtureStopBeforeMarker() {
	if strings.TrimSpace(os.Getenv("BANSOKO_ENVIRONMENT")) == "production" || strings.TrimSpace(os.Getenv(FixtureStopBeforeMarkerEnv)) != "1" {
		return
	}
	fmt.Fprintf(os.Stderr, "[embedded-update] FIXTURE INJECTION: before marker (%s=1); exiting %d to simulate a process crash in this window (non-production only)\n",
		FixtureStopBeforeMarkerEnv, FixtureStopExitCode)
	fixtureStopExit(FixtureStopExitCode)
}

func (s *Service) runChain(manifest hardeningdomain.ReleaseManifest, digest, manifestURL, operationID string, source *UpdateHTTPSource) {
	defer s.finishChain()
	ctx := context.Background()
	var err error
	stage, failureMessage := "release_input", "Update stopped because the release input could not be prepared."
	failureClass := prepSourceInvalid
	if source == nil {
		// Only the admitted operation reaches the chain with its own source; a
		// caller without one rebuilds the deployment policy as before.
		source, err = s.packageSourceFor(manifestURL)
	}
	if err == nil && manifest.EmbeddedUpdate == nil {
		err = errors.New("release manifest has no embedded update segment")
		failureClass = prepManifestInvalid
	}
	var payload string
	if err == nil {
		stage, failureMessage = "download_payload", "Update stopped because the release package could not be downloaded or verified."
		failureClass = prepTransfer
		payload, err = s.ensurePayload(ctx, source, operationID, manifest.EmbeddedUpdate)
	}
	if err == nil {
		stage, failureMessage = "stage_release", "Update stopped because the release package could not be staged or verified."
		failureClass = prepArchive
		_, err = s.stagePayload(payload, operationID, manifest)
	}
	if err == nil && FixtureStopAfter("staged") {
		ApplyFixtureStop("staged")
	}
	if err == nil {
		applyFixtureStopBeforeMarker()
	}
	if err == nil {
		stage, failureMessage = "prepare_handoff", "Update stopped before restart because the handoff could not be prepared."
		failureClass = prepHandoff
		pointer := deploymentidentity.ReadCurrentPointer(s.updatesRoot)
		if pointer.Presented && !pointer.Readable {
			err = errors.New("current release pointer is corrupt; refusing pending update marker")
			failureClass = prepPointerInvalid
		}
		previous := ""
		if pointer.Readable {
			previous = pointer.Token
		}
		if err == nil {
			err = WritePendingMarker(s.updatesRoot, PendingMarker{OperationID: operationID, ReleaseID: manifest.ReleaseID, ManifestDigest: digest, ReleaseSequence: manifest.ReleaseSequence, PreviousReleaseID: previous})
			if errors.Is(err, os.ErrExist) {
				failureClass = prepHandoffExists
			}
		}
	}
	if err != nil {
		detail := failureMessage
		version, schema := manifest.Version, manifest.TargetMigrationVersion
		s.mu.Lock()
		created := s.lastStatus.CreatedAt
		s.mu.Unlock()
		now := time.Now().UTC()
		if s.clock != nil {
			now = s.clock().UTC()
		}
		if created.IsZero() {
			created = now
		}
		status := Status{OperationID: operationID, Channel: Channel, Step: "failed", TargetReleaseID: manifest.ReleaseID, TargetVersion: &version, TargetSchemaVersion: &schema, LastError: &detail, CreatedAt: created, UpdatedAt: now}
		s.setStatus(status)
		if s.updatesRoot != "" {
			if err := s.persistPreparationFailure(status); err != nil && s.logger != nil {
				s.logger.Warn(ctx, observabilityport.RuntimeEvent{
					Message: "The update failed before restart; its status could not be saved.", Component: "update", Operation: "apply_update",
					Stage: "save_failure_status", Outcome: "failure", ReasonCode: "update_failure_status_unconfirmed", OwnerType: "update_operation", OwnerID: operationID,
				})
			}
		}
		if s.logger != nil {
			s.logger.Warn(ctx, observabilityport.RuntimeEvent{
				Message: detail, Component: "update", Operation: "apply_update", Stage: stage, Outcome: "failure",
				ReasonCode: "update_preparation_failed", ErrorClass: string(classifyPreparation(err, failureClass)), OwnerType: "update_operation", OwnerID: operationID,
			})
		}
		return
	}
	ApplyFixtureStop("pending_marker")
	s.setStatus(Status{OperationID: operationID, Channel: Channel, Step: "pending_backup", TargetReleaseID: manifest.ReleaseID})
	s.requestExit()
}

func (s *Service) ensurePayload(ctx context.Context, source *UpdateHTTPSource, opID string, target *hardeningdomain.EmbeddedUpdateSegment) (string, error) {
	outPath := filepath.Join(s.updatesRoot, "downloads", opID, PayloadFileName)
	if err := verifyDownloadedPayload(outPath, target); err == nil {
		return outPath, nil
	}
	return s.fetchPayload(ctx, source, opID, target)
}

// verifyDownloadedPayload proves an on-disk payload archive is exactly the
// published segment of one release: regular file, exact size and exact sha256.
// VerifyPayloadFile is the exported form of this same check; the two must never
// delegate to each other.
func verifyDownloadedPayload(path string, target *hardeningdomain.EmbeddedUpdateSegment) error {
	if target == nil {
		return errors.New("release manifest has no embedded update segment")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("retained payload is unavailable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("retained payload is not a regular file")
	}
	if info.Size() != target.PayloadSize {
		return fmt.Errorf("retained payload size %d does not match the manifest size %d", info.Size(), target.PayloadSize)
	}
	digest, err := hashFileForSidecar(path)
	if err != nil {
		return fmt.Errorf("hash retained payload: %w", err)
	}
	if digest != target.PayloadDigest {
		return errors.New("retained payload digest does not match the manifest digest")
	}
	return nil
}

func (s *Service) stagePayload(payloadPath, opID string, manifest hardeningdomain.ReleaseManifest) (_ string, resultErr error) {
	class := prepStorage
	defer func() {
		resultErr = labelPreparationError(resultErr, class)
	}()
	tempDir := filepath.Join(s.updatesRoot, "releases", ".tmp-"+opID)
	if err := ensureOperationDir(s.updatesRoot, tempDir, 0o755); err != nil {
		return "", fmt.Errorf("create staging directory: %w", err)
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.RemoveAll(tempDir)
		}
	}()
	class = prepArchive
	hashes, err := extractPayloadToTemp(payloadPath, tempDir)
	if err != nil {
		return "", err
	}
	class = prepReleaseVerify
	meta, err := verifyStagedTree(tempDir)
	if err != nil {
		return "", err
	}
	if meta.ReleaseID != manifest.ReleaseID {
		return "", fmt.Errorf("payload metadata claims release %s, manifest targets %s", meta.ReleaseID, manifest.ReleaseID)
	}
	class = prepStorage
	if err = copyFileDurable(payloadPath, filepath.Join(tempDir, PayloadArchiveEntryPath), 0o600); err != nil {
		return "", fmt.Errorf("retain verified payload archive: %w", err)
	}
	if err = persistSignedManifest(tempDir, manifest); err != nil {
		return "", fmt.Errorf("retain release descriptor: %w", err)
	}
	class = prepReleaseVerify
	if err = VerifyStagedReleaseTree(tempDir); err != nil {
		return "", fmt.Errorf("verify retained release identity: %w", err)
	}
	class = prepReleasePublish
	releaseDir := filepath.Join(s.updatesRoot, "releases", manifest.ReleaseID)
	if info, statErr := os.Lstat(releaseDir); statErr == nil {
		if !info.IsDir() || !treeMatchesReleaseDir(releaseDir, hashes) {
			class = prepReleaseConflict
			return "", fmt.Errorf("releases/%s already holds different content; refusing to overlay", manifest.ReleaseID)
		}
		if err = VerifyReleaseTree(releaseDir); err != nil {
			class = prepReleaseVerify
			return "", fmt.Errorf("existing releases/%s failed verification: %w", manifest.ReleaseID, err)
		}
		return releaseDir, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	stagedInfo, err := os.Lstat(tempDir)
	if err != nil {
		return "", err
	}
	removeFailedPublish := func(cause error) error {
		// A rename can succeed before the directory sync fails. Remove only
		// the newly published tree this operation owned, never an existing release.
		if current, statErr := os.Lstat(releaseDir); statErr == nil && os.SameFile(current, stagedInfo) {
			if cleanupErr := os.RemoveAll(releaseDir); cleanupErr != nil {
				return errors.Join(cause, cleanupErr)
			}
			return errors.Join(cause, syncDirectory(filepath.Dir(releaseDir)))
		}
		return cause
	}
	if err = publishReleaseTree(tempDir, releaseDir, []string{packagemaker.ServerEntryPath}); err != nil {
		return "", removeFailedPublish(err)
	}
	cleanupTemp = false
	if err = VerifyReleaseTree(releaseDir); err != nil {
		class = prepReleaseVerify
		return "", removeFailedPublish(fmt.Errorf("verify published release tree: %w", err))
	}
	return releaseDir, nil
}

func copyFileDurable(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
