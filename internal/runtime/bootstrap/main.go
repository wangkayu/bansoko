// Package bootstrap implements the unified binary's bootstrap mode. The image
// anchor is the immutable PID1 entrypoint baked into the base image. Its contract
// (embedded self-update program, design §2):
//
//   - /app is never overlaid: the anchor and base application are part of the
//     of the digest-addressed base image and can never be replaced by update
//     payload content;
//   - overlay release trees live on the updates volume
//     (/config/updates by default) in releases/<release-id>/, with
//     a `current` pointer file naming the promoted release id. Promotion uses
//     atomic rename semantics and is owned by later stages of the chain; this
//     binary only consumes pointers that already verify cleanly.
//
// Modes:
//
//	bootstrap [server]       consume a pending update marker, or migrate the selected
//	                         release's schema and then serve it
//	bootstrap migrate        select and exec the unified binary's migrate mode
//	                         against the matching tree
//	bootstrap recover status inspect read-only journal and rollback-blocked state
//	bootstrap reset-password reset the owner credentials from inside the container
//
// Deployment inputs: BANSOKO_DATABASE_URL is the schema-owner connection. Before
// any plan runs, the wrapper resolves the generated secrets in the config
// directory (internal/runtime/bootsecrets) and exports the derived runtime
// login connection (BANSOKO_RUNTIME_DATABASE_URL) plus the effective session
// secret and encryption key to every child it starts.
//
// Selection and safety rules:
//   - marker startup runs without an application process and creates the journal only
//     after exact fence acquisition and a verified backup plus scratch restore proof;
//   - without a marker, the disposable pre-marker workspace is discarded; an
//     active journal outside the handoff family parks in maintenance;
//   - rollback_blocked remains active evidence and is never auto-discarded;
//   - the server-kind entry consumes pending_migration through rollback/success;
//   - current-release pointer or signed-tree verification failures do not downgrade
//     to an unverified overlay.
package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/runtime/bootprepare"
	"bansoko/internal/runtime/bootsecrets"
	"bansoko/internal/runtime/buildinfo"
	"bansoko/internal/runtime/config"
	"bansoko/internal/runtime/deploymentidentity"
	"bansoko/internal/runtime/deploymentprovenance"
	updateapplication "bansoko/internal/update/application"
	embeddedupdate "bansoko/internal/update/journal"
	"bansoko/internal/update/packagemaker"
)

const (
	baseServerPath  = "/app/bansoko-server"
	baseSchemaDir   = "/app/db/migrations"
	baseFrontendDir = "/app/frontend/dist"

	// embeddedBootstrapVersion identifies this wrapper's release capabilities. A
	// release manifest may require it (`embedded_update.bootstrap_min_version`)
	// and the server refuses a candidate whose floor this wrapper does not reach.
	// The unified runtime is a new base-image capability: older bootstraps cannot
	// consume its single-binary layout, so their operators must switch the image.
	embeddedBootstrapVersion = "embedded-bootstrap-unified"

	journalFileName    = "journal.json"
	pendingMarkerName  = "pending.json"
	currentPointerName = "current"

	releasesDirName = "releases"

	envSchemaDir   = "BANSOKO_SCHEMA_DIR"
	envFrontendDir = "BANSOKO_FRONTEND_DIR"
)

// deploymentIdentityEnv builds the BANSOKO_DEPLOYMENT_* / base-digest environment
// overrides injected into the exec'd target (design §6). Every key is always
// present so inherited values can never leak across a base/overlay switch:
// unknown fields are cleared by writing them empty.
//
// The bootstrap version is this binary's own constant, not an inherited value:
// the running /app/bansoko-server is the only authority for what the base image
// contains, so the updater's comparison can never be satisfied by deployment
// environment text alone.
func deploymentIdentityEnv(source, releaseID, payloadDigest, operationID string) map[string]string {
	// The anchor kind is derived from the executable on every launch.
	build, docker := readImageAnchor()
	anchor := deploymentidentity.AnchorDocker
	if !docker {
		anchor = deploymentidentity.AnchorNative
	}
	var baseBuild string
	if docker && build.BuildID != "" {
		raw, _ := json.Marshal(build)
		baseBuild = string(raw)
	}
	return map[string]string{
		deploymentidentity.EnvSource:               source,
		deploymentidentity.EnvSelectedReleaseID:    releaseID,
		deploymentidentity.EnvPayloadDigest:        payloadDigest,
		deploymentidentity.EnvUpdateOperationID:    operationID,
		deploymentidentity.EnvAnchorKind:           anchor,
		deploymentidentity.EnvExpectedProcessBuild: "",
		deploymentidentity.EnvBootstrapVersion:     embeddedBootstrapVersion,
		deploymentidentity.EnvBaseBuild:            baseBuild,
		deploymentidentity.EnvSelectionReasonCode:  "overlay_update_active",
		deploymentidentity.EnvSelectionReason:      "embedded update selected the running tree",
		deploymentidentity.EnvTakeoverOperationID:  "",
	}
}

func deploymentIdentityEnvWithBuild(source, releaseID, payloadDigest, operationID, expectedBuildID string) map[string]string {
	env := deploymentIdentityEnv(source, releaseID, payloadDigest, operationID)
	env[deploymentidentity.EnvExpectedProcessBuild] = strings.TrimSpace(expectedBuildID)
	return env
}

type overlayIdentityProof struct {
	Manifest               hardeningdomain.ReleaseManifest
	PayloadDigest          string
	OperationID            string
	ExpectedProcessBuildID string
	Build                  buildinfo.BuildInfo
}

// verifyOverlayIdentity proves the signed release tree before reading the
// deployment-owned provenance. Provenance is not a trust root: its release,
// payload and build fields must match the independently verified tree.
func verifyOverlayIdentity(updatesRoot, releaseID string) (overlayIdentityProof, error) {
	if !validReleaseToken(releaseID) {
		return overlayIdentityProof{}, fmt.Errorf("invalid overlay release id %q", releaseID)
	}
	releaseDir := filepath.Join(updatesRoot, releasesDirName, releaseID)
	meta, manifest, err := verifyReleaseTreeIdentity(releaseDir)
	if err != nil {
		return overlayIdentityProof{}, err
	}
	if meta.ReleaseID != releaseID {
		return overlayIdentityProof{}, errors.New("verified release metadata does not match selected release")
	}
	provenance, err := deploymentprovenance.Read(updatesRoot, releaseID)
	if err != nil {
		return overlayIdentityProof{}, fmt.Errorf("read overlay deployment provenance: %w", err)
	}
	if err := provenance.Validate(releaseID, manifest.EmbeddedUpdate.PayloadDigest, meta.BuildID); err != nil {
		return overlayIdentityProof{}, err
	}
	if err := validateUnifiedReleaseMetadata(meta); err != nil {
		return overlayIdentityProof{}, err
	}
	return overlayIdentityProof{
		PayloadDigest:          provenance.PayloadDigest,
		OperationID:            provenance.ProducingOperationID,
		ExpectedProcessBuildID: provenance.ExpectedProcessBuildID,
		Build:                  buildinfo.BuildInfo{Order: meta.Order, Version: meta.Version, Commit: meta.SourceCommit, BuildID: meta.BuildID, SchemaVersion: meta.TargetSchemaVersion, APIVersion: manifest.APIContractVersion},
		Manifest:               manifest,
	}, nil
}

// overlayIdentityFromJournal is a legacy journal-only inspection helper kept
// for evidence tests. Runtime launch paths must use verifyOverlayIdentity,
// because terminal journal records can be archived and provenance survives
// those archives.
func overlayIdentityFromJournal(updatesRoot, releaseID string) (payloadDigest, operationID string) {
	if releaseID == "" {
		return "", ""
	}
	flowJournal, err := embeddedupdate.Open(filepath.Join(updatesRoot, journalFileName))
	if err != nil {
		return "", ""
	}
	// The journal lock is held only for the decision window. A close error is
	// handled explicitly below by the caller that owns the launch decision.
	defer func() { _ = flowJournal.Close() }()
	record, found, loadErr := flowJournal.Load()
	if loadErr != nil || !found {
		return "", ""
	}
	if record.Step != embeddedupdate.StepSucceeded ||
		record.CurrentReleaseID != releaseID ||
		!validReleaseToken(releaseID) {
		return "", ""
	}
	return strings.TrimSpace(record.PayloadDigest), record.OperationID
}

var releaseIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// consumeFamilySteps are the journal checkpoints the server-kind bootstrap
// entry owns end-to-end: the B4 state machine consumes each of them with an
// explicit resume branch (design §2 §5 recovery matrix).
var consumeFamilySteps = map[embeddedupdate.Step]bool{
	embeddedupdate.StepPendingMigration:    true,
	embeddedupdate.StepMigrationSucceeded:  true,
	embeddedupdate.StepPromoted:            true,
	embeddedupdate.StepReadinessFailed:     true,
	embeddedupdate.StepRollbackStarted:     true,
	embeddedupdate.StepDatabaseRestored:    true,
	embeddedupdate.StepFenceClosurePending: true,
}

type launchMode int

const (
	modeBase launchMode = iota
	modeOverlay
	modeMaintenance
	modeConsume
	modePendingMarker
	modeTakeover
)

func (m launchMode) String() string {
	switch m {
	case modeOverlay:
		return "overlay"
	case modeMaintenance:
		return "maintenance"
	case modeConsume:
		return "consume"
	case modePendingMarker:
		return "pending-marker"
	case modeTakeover:
		return "start_takeover"
	default:
		return "base"
	}
}

// plan is the decision output before any exec happens. Maintenance plans never
// exec application binaries; server-kind exposes only the bootstrap health
// responder, while migrate-kind parks without listening.
type plan struct {
	mode             launchMode
	kind             string // "server" or "migrate"
	binaryPath       string
	migratePath      string // server plans: the same tree's migration executable
	schemaDir        string
	frontendDir      string // empty for migrate jobs (they serve no assets)
	releaseID        string
	handoffReleaseID string
	reason           string
	reasonCode       string
	baseBuild        buildinfo.BuildInfo
	maintenanceOp    string // operation id when parked on journal state
	maintenanceStep  string // journal step string when parked
	takeoverOp       string
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[bootstrap] "+format+"\n", args...)
}

// validReleaseToken rejects pointer payloads that could escape the releases
// root via traversal or absolute paths.
func validReleaseToken(token string) bool {
	return token != "" && token != "." && token != ".." && !strings.ContainsAny(token, "/\\") &&
		releaseIDPattern.MatchString(token)
}

// safeRelPath rejects release-manifest entries that escape the release layout.
func safeRelPath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return false
	}
	cleaned := filepath.Clean("/" + value)
	return cleaned == "/"+value
}

// envWithOverrides returns the parent environment with KEY=VALUE overrides
// replacing same-key entries instead of duplicating them.
func envWithOverrides(overrides map[string]string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrides[key]; overridden {
			continue
		}
		env = append(env, entry)
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func updateMode() string {
	return config.UpdateModeFromEnv(os.Getenv)
}

// planLaunch decides which binary tree runs. kind is "server" or "migrate".
func planLaunch(kind, updatesRoot string) (out plan) {
	out = plan{kind: kind}
	facts := selectionFacts{kind: kind, mode: updateMode()}
	defer func() {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]any{
			"event": "bootstrap_selection", "kind": kind, "update_mode": facts.mode, "action": out.mode.String(),
			"reason_code": out.reasonCode, "reason": out.reason, "overlay_release_id": out.releaseID,
			"base_build_id": out.baseBuild.BuildID, "operation_id": out.maintenanceOp,
		})
	}()
	park := func(reason string) plan {
		out.reasonCode = "update_evidence_unusable"
		return maintenancePlan(out, out.maintenanceOp, out.maintenanceStep, reason)
	}
	if !config.IsValidUpdateMode(facts.mode) {
		return park(fmt.Sprintf("unsupported BANSOKO_UPDATE_MODE %q", facts.mode))
	}
	facts.base, facts.imageAnchor = readImageAnchor()
	out.baseBuild = facts.base
	rootInfo, statErr := os.Lstat(updatesRoot)
	if errors.Is(statErr, syscall.ENOENT) {
		out.reasonCode = arbitrateBaseVsOverlay(facts).reasonCode
		return basePlan(out, "no updates volume present")
	}
	if statErr != nil || !rootInfo.IsDir() {
		return park(fmt.Sprintf("updates volume %s unusable: %v", updatesRoot, statErr))
	}
	entrypointJournal, err := embeddedupdate.Open(filepath.Join(updatesRoot, journalFileName))
	if err != nil {
		return park(fmt.Sprintf("journal unavailable: %v", err))
	}
	defer func() {
		if closeErr := entrypointJournal.Close(); closeErr != nil {
			out = park(fmt.Sprintf("update journal close failed: %v", closeErr))
		}
	}()
	record, found, err := entrypointJournal.Load()
	if err != nil {
		return park(fmt.Sprintf("journal unreadable after open: %v", err))
	}
	if !found {
		quarantined, err := filepath.Glob(filepath.Join(updatesRoot, "journal.corrupt-*"))
		if err != nil || len(quarantined) > 0 {
			return park("quarantined journal requires operator review")
		}
	}
	facts.active = found && record.Active()
	facts.rollbackBlocked = found && record.Step == embeddedupdate.StepRollbackBlocked
	facts.consumeFamily = consumeFamilySteps[record.Step]
	facts.activeTakeover = facts.active && record.OperationKind == embeddedupdate.KindBaseTakeover
	if facts.active && record.SchemaVersion == 1 {
		out.reasonCode = "legacy_journal_read_only"
		return maintenancePlan(out, record.OperationID, string(record.Step), "legacy journal requires operator review")
	}
	if facts.active || facts.rollbackBlocked {
		out.maintenanceOp, out.maintenanceStep = record.OperationID, string(record.Step)
	}
	markerPath := filepath.Join(updatesRoot, pendingMarkerName)
	if _, err := os.Lstat(markerPath); err == nil {
		marker, err := readPendingMarker(markerPath)
		facts.pending = err == nil
		facts.unusable = err != nil || validateMarkerPointer(updatesRoot, marker) != nil || (facts.active && !facts.rollbackBlocked && (record.Step != embeddedupdate.StepPendingMigration ||
			marker.OperationID != record.OperationID || marker.ReleaseID != record.TargetReleaseID || marker.ReleaseSequence != record.TargetReleaseSequence ||
			marker.ManifestDigest != record.ManifestDigest || marker.PreviousReleaseID != record.PreviousReleaseID))
	} else if !errors.Is(err, syscall.ENOENT) {
		facts.unusable = true
	}
	var target overlayTarget
	if !facts.pending && !facts.active && !facts.rollbackBlocked && !facts.unusable {
		pointer := deploymentidentity.ReadCurrentPointer(updatesRoot)
		facts.pointerPresent = pointer.Presented
		if !pointer.Presented && facts.imageAnchor && record.Takeover != nil && record.Step == embeddedupdate.StepSucceeded {
			anchor := currentBaseAnchor(facts.base)
			if anchor.Valid() && record.CurrentReleaseID == "" && record.ManifestDigest == anchor.Fingerprint() {
				facts.previousTakeover, out.takeoverOp = "succeeded", record.OperationID
			}
		}
		if !pointer.Presented && found && record.CurrentReleaseID != "" {
			facts.evidenceConflict = true
		}
		if pointer.Readable {
			if found && record.CurrentReleaseID != "" && record.CurrentReleaseID != pointer.Token {
				facts.evidenceConflict = true
			}
			target = overlayTarget{releaseID: pointer.Token, path: relDirOf(updatesRoot, pointer.Token)}
			target.binaryPath = releaseServerBin(target.path)
			proof, err := verifyOverlayIdentity(updatesRoot, pointer.Token)
			facts.overlayProven = err == nil
			facts.overlay = proof.Build
			facts.compatible = err == nil && compatibleImageAnchor(facts.base, proof.Manifest)
			if facts.compatible && facts.base.Ordered() && facts.overlay.Ordered() && facts.base.Series == facts.overlay.Series && facts.base.Lane == facts.overlay.Lane && facts.base.ReleaseSequence > facts.overlay.ReleaseSequence {
				a := currentBaseAnchor(facts.base)
				facts.compatible = a.Valid() && facts.base.BuildID != pointer.Token
				if facts.compatible {
					prior, err := takeoverOutcome(entrypointJournal, record, found, a, previousOverlay(pointer.Token, proof))
					if err != nil {
						return park("cannot read prior takeover outcomes")
					}
					if prior.OperationID != "" {
						facts.previousTakeover, out.takeoverOp = string(prior.Step), prior.OperationID
					}
				}
			}
		}
	}
	decision := arbitrateBaseVsOverlay(facts)
	out.mode, out.reasonCode = decision.action, decision.reasonCode
	switch decision.action {
	case modeBase:
		return basePlan(out, "no current overlay")
	case modeOverlay:
		return overlayPlan(out, target.releaseID, target.binaryPath, target.binaryPath, filepath.Join(target.path, "db/migrations"), filepath.Join(target.path, "frontend/dist"))
	case modeConsume:
		out.reason = "embedded-update handoff consumption (" + string(record.Step) + ")"
		out.handoffReleaseID = record.TargetReleaseID
		if record.Step == embeddedupdate.StepFenceClosurePending && record.FenceClosureTarget != embeddedupdate.StepSucceeded {
			out.handoffReleaseID = record.PreviousReleaseID
		}
	case modePendingMarker:
		out.reason = "pending embedded update marker"
	case modeTakeover:
		out.reason = decision.reasonCode
		out.releaseID = target.releaseID
	case modeMaintenance:
		out.reason = decision.reasonCode
	}
	return out
}

func basePlan(out plan, reason string) plan {
	out.mode = modeBase
	binary := baseServerBinary()
	if out.kind == "migrate" {
		out.binaryPath = binary
		out.schemaDir = baseAssetDir(baseSchemaDir)
	} else {
		out.binaryPath = binary
		out.migratePath = binary
		out.schemaDir = baseAssetDir(baseSchemaDir)
		out.frontendDir = baseAssetDir(baseFrontendDir)
	}
	out.reason = reason
	return out
}

func compatibleImageAnchor(base buildinfo.BuildInfo, overlay hardeningdomain.ReleaseManifest) bool {
	if base.Dirty || base.APIVersion == "" || base.APIVersion != overlay.APIContractVersion || base.SchemaVersion == "" {
		return false
	}
	floor, err := overlay.EffectiveMinImageVersion()
	if err != nil || floor == "" {
		return false
	}
	compatible, err := updateapplication.ImageCompatibilityAtLeast(base.BaseCompatibilityVersion, floor)
	return err == nil && compatible
}

func readImageAnchor() (buildinfo.BuildInfo, bool) {
	if os.Getenv(nativeAnchorEnv) != "" || runtime.GOOS != "linux" {
		return buildinfo.BuildInfo{}, false
	}
	info, err := os.Lstat(baseServerPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o222 != 0 {
		return buildinfo.BuildInfo{}, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return buildinfo.BuildInfo{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, baseServerPath, "version", "--json")
	cmd.Env = []string{}
	data, err := cmd.Output()
	var attested struct {
		buildinfo.BuildInfo
		Platform        string `json:"platform"`
		Architecture    string `json:"architecture"`
		ContainerAnchor string `json:"container_anchor"`
	}
	if err != nil || json.Unmarshal(data, &attested) != nil || attested.BuildID == "" || attested.Version == "" ||
		attested.Platform != runtime.GOOS || attested.Architecture != runtime.GOARCH || attested.ContainerAnchor != "docker-v1" {
		return buildinfo.BuildInfo{}, false
	}
	return attested.BuildInfo, true
}

// baseServerBinary resolves the immutable image binary or the native lifecycle anchor.
func baseServerBinary() string {
	if anchor := os.Getenv(nativeAnchorEnv); anchor != "" {
		return anchor
	}
	if info, err := os.Stat(baseServerPath); err == nil && !info.IsDir() {
		return baseServerPath
	}
	if exe, err := os.Executable(); err == nil && exe != "" {
		return exe
	}
	return baseServerPath
}

// baseAssetDir returns the container asset directory only when it exists on
// disk. An empty result means the child must fall back to the asset set
// embedded in the binary (native single-binary deployments), so it is never
// passed down as an explicit override.
func baseAssetDir(dir string) string {
	if os.Getenv(nativeAnchorEnv) != "" {
		return ""
	}
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return dir
	}
	return ""
}

func overlayPlan(out plan, releaseID, binaryPath, migratePath, schemaDir, frontendDir string) plan {
	out.mode = modeOverlay
	out.releaseID = releaseID
	out.binaryPath = binaryPath
	out.schemaDir = schemaDir
	if out.kind != "migrate" {
		out.migratePath = migratePath
		out.frontendDir = frontendDir
	}
	out.reason = "verified current overlay release"
	return out
}

func maintenancePlan(out plan, operationID, step, reason string) plan {
	out.mode = modeMaintenance
	out.reason = reason
	out.maintenanceOp = operationID
	out.maintenanceStep = step
	return out
}

type overlayTarget struct {
	releaseID  string
	path       string
	binaryPath string
}

// resolveCurrentRelease reads the `current` pointer file (one validated
// release-id token written by promotion) and locates the corresponding tree
// under releases/.
func resolveCurrentRelease(_ string, updatesRoot string) (overlayTarget, error) {
	pointer := deploymentidentity.ReadCurrentPointer(updatesRoot)
	if !pointer.Presented {
		return overlayTarget{}, os.ErrNotExist
	}
	if !pointer.Readable {
		return overlayTarget{}, deploymentidentity.ErrPointerCorrupt
	}
	token := pointer.Token
	releaseDir := filepath.Join(updatesRoot, releasesDirName, token)
	info, statErr := os.Stat(releaseDir)
	if statErr != nil || !info.IsDir() {
		return overlayTarget{}, fmt.Errorf("current release %s missing under %s", token, releasesDirName)
	}
	return overlayTarget{
		releaseID:  token,
		path:       releaseDir,
		binaryPath: releaseServerBin(releaseDir),
	}, nil
}

// verifyReleaseTree proves the current release against its retained metadata:
// the manifest's own digest and the payload descriptor. A writable metadata
// file by itself is never sufficient to launch overlay code.
func verifyReleaseTree(releaseDir string) error {
	_, err := verifyReleaseTreeMetadata(releaseDir)
	return err
}

func verifyReleaseTreeIdentity(releaseDir string) (packagemaker.Metadata, hardeningdomain.ReleaseManifest, error) {
	meta, manifest, err := updateapplication.VerifyReleaseTreeIdentity(releaseDir)
	if err == nil {
		err = manifest.EmbeddedUpdate.ValidateTarget(runtime.GOOS, runtime.GOARCH)
	}
	return meta, manifest, err
}

func verifyReleaseTreeMetadata(releaseDir string) (packagemaker.Metadata, error) {
	meta, _, err := verifyReleaseTreeIdentity(releaseDir)
	return meta, err
}

func hashFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func selectBootstrapPlan(kind string, getenv func(string) string) (plan, string) {
	updatesRoot := deploymentidentity.UpdatesRoot(getenv)
	return planLaunch(kind, updatesRoot), updatesRoot
}

// Process seams: tests replace them to observe ordering without replacing the
// test process image or starting real children.
var (
	execProcess         = syscall.Exec
	runStartupMigration = runMigrationChild
	enterConsumeFlow    = runConsumeFlow
	prepareEnvironment  = func() error { return prepareDeploymentEnvironment(os.Getenv, os.Setenv, logf) }
)

// prepareDeploymentEnvironment turns the single user input (the schema-owner
// BANSOKO_DATABASE_URL) into the process environment every child inherits:
// generated or stored secrets from the config directory, and the derived
// runtime login connection. The runtime connection is always overwritten so a
// stale or user-supplied value can never select a different serving login.
func prepareDeploymentEnvironment(getenv func(string) string, setenv func(string, string) error, log func(string, ...any)) error {
	ownerDSN := strings.TrimSpace(getenv(bootsecrets.EnvDatabaseURL))
	if ownerDSN == "" {
		return fmt.Errorf("%s is required: the connection of the database account that owns the schema and can create roles", bootsecrets.EnvDatabaseURL)
	}
	configDir := bootsecrets.ConfigDir(getenv)
	return bootsecrets.EnsureRuntimeEnvironment(configDir, getenv, setenv, func(format string, args ...any) {
		log(format, args...)
	})
}

// runMigrationChild applies the selected tree's schema with the owner
// connection and converges the runtime login role. Its structured output goes
// to the container log.
func runMigrationChild(command processCommand, env []string) error {
	return runMigrationChildContext(context.Background(), command, env)
}

func isolateChildProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

func runMigrationChildContext(parent context.Context, command processCommand, env []string) error {
	ctx, cancel := context.WithTimeout(parent, migrateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command.path, command.args...)
	isolateChildProcessGroup(cmd)
	cmd.Env = env
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s did not finish within %s", command.path, migrateTimeout)
		}
		return fmt.Errorf("%s failed: %w", command.path, err)
	}
	return nil
}

// run decides and executes. It never returns from successful overlay/base
// paths because syscall.Exec replaces the process image.
func run(kind string) int {
	if err := prepareEnvironment(); err != nil {
		logf("MAINTENANCE fail-closed: deployment environment unusable: %v", err)
		blockForever()
		return 124
	}
	chosen, updatesRoot := selectBootstrapPlan(kind, os.Getenv)
	if code, handedOff := handoffUnifiedBootstrap(chosen, updatesRoot); handedOff {
		return code
	}
	if kind == "server" && (chosen.mode == modeBase || chosen.mode == modeOverlay) {
		if same, _ := isCurrentExecutable(baseServerPath); same {
			if err := reconcileRetirement(updatesRoot); err != nil {
				logf("overlay retirement deferred: %v", err)
			}
		}
		if err := discardPreMarkerWork(updatesRoot); err != nil {
			logf("MAINTENANCE fail-closed: pre-marker workspace cleanup failed: %v", err)
			blockForever()
			return 124
		}
		chosen = planLaunch(kind, updatesRoot)
	}
	switch chosen.mode {
	case modeTakeover:
		return runTakeoverEntry(updatesRoot, chosen.reasonCode == "base_newer_verified")
	case modePendingMarker, modeConsume:
		// The consume flow owns migration (backup, target migrate, handoff,
		// rollback); the startup migration below never runs for these modes.
		return enterConsumeFlow(updatesRoot)
	case modeMaintenance:
		logf("MAINTENANCE fail-closed: %s", chosen.reason)
		if chosen.maintenanceOp != "" {
			logf("MAINTENANCE context: operation=%s step=%s", chosen.maintenanceOp, chosen.maintenanceStep)
		}
		if kind == "server" {
			if err := serveMaintenance(updatesRoot, chosen.maintenanceStep, chosen.maintenanceOp); err != nil {
				logf("MAINTENANCE responder failed closed: %v", err)
				return 1
			}
			return 0
		}
		logf("MAINTENANCE migrate job parked without serving; inspect %s.", updatesRoot)
		blockForever()
		return 124
	case modeOverlay:
		logf("launching %s from overlay release %s (%s)", kind, chosen.releaseID, chosen.reason)
	case modeBase:
		logf("launching %s from base image (%s)", kind, chosen.reason)
	}
	return launchPlan(chosen, updatesRoot)
}

// launchPlan proves the selected tree, migrates its schema first when the plan
// serves, then replaces this process with the selected binary. A failed
// startup migration never starts the server: the process exits non-zero so the
// container restart policy retries once the cause is fixed.
func launchPlan(chosen plan, updatesRoot string) int {
	pairs := map[string]string{}
	if mode := strings.TrimSpace(os.Getenv("BANSOKO_UPDATE_MODE")); mode != "" {
		pairs["BANSOKO_UPDATE_MODE"] = mode
	}
	// Empty schema/frontend dirs mean the child falls back to the assets
	// embedded in its binary; explicit environment configuration is inherited
	// through envWithOverrides either way.
	if chosen.schemaDir != "" {
		pairs[envSchemaDir] = chosen.schemaDir
	}
	if chosen.frontendDir != "" {
		pairs[envFrontendDir] = chosen.frontendDir
	}
	// Deployment identity overrides replace inherited facts for both base and overlay launches.
	switch chosen.mode {
	case modeOverlay:
		proof, proofErr := verifyOverlayIdentity(updatesRoot, chosen.releaseID)
		if proofErr != nil {
			logf("MAINTENANCE fail-closed: selected overlay identity is unproven: %v", proofErr)
			blockForever()
			return 124
		}
		for key, value := range deploymentIdentityEnvWithBuild(deploymentidentity.SourceOverlay, chosen.releaseID, proof.PayloadDigest, proof.OperationID, proof.ExpectedProcessBuildID) {
			pairs[key] = value
		}
	default:
		for key, value := range deploymentIdentityEnv(deploymentidentity.SourceBase, "", "", "") {
			pairs[key] = value
		}
	}
	pairs[deploymentidentity.EnvSelectionReasonCode] = chosen.reasonCode
	pairs[deploymentidentity.EnvSelectionReason] = chosen.reason
	pairs[deploymentidentity.EnvTakeoverOperationID] = chosen.takeoverOp
	if chosen.kind == "server" {
		if chosen.migratePath == "" {
			logf("MAINTENANCE fail-closed: the selected %s tree has no migration binary", chosen.mode)
			return 1
		}
		logf("applying the %s schema before serving", chosen.mode)
		migrate := commandFor(chosen.migratePath, "migrate")
		if err := runStartupMigration(migrate, envWithOverrides(pairs)); err != nil {
			logf("MAINTENANCE fail-closed: startup migration failed, the server was not started: %v", err)
			return 1
		}
	}
	launch := commandFor(chosen.binaryPath, chosen.kind)
	if execErr := execProcess(launch.path, launch.argv(), envWithOverrides(pairs)); execErr != nil {
		logf("exec %s failed: %v", chosen.binaryPath, execErr)
		return 1
	}
	return 0
}

func blockForever() {
	// An empty select{} would trip the runtime deadlock detector; long sleeps
	// keep the process interruptible and killable for operators.
	for {
		time.Sleep(time.Hour)
	}
}

// maintenanceDatabaseURL returns the schema-owner DSN the maintenance paths
// (embedded migrate subprocess, pg_dump/pg_restore, startup journal recovery)
// use. BANSOKO_DATABASE_URL is always the owner connection; the serving process
// receives the derived runtime connection (BANSOKO_RUNTIME_DATABASE_URL)
// separately.
func maintenanceDatabaseURL() string {
	return strings.TrimSpace(os.Getenv(bootsecrets.EnvDatabaseURL))
}

func dispatch(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return dispatchWithRunners(args, stdin, stdout, stderr, run, runRecoverCommand, runResetPassword)
}

func dispatchWithRunners(args []string, stdin io.Reader, stdout, stderr io.Writer, runKind func(string) int,
	runRecover func(context.Context, []string, io.Writer, io.Writer) int,
	runReset func(context.Context, []string, io.Reader, io.Writer, io.Writer) int) int {
	kind := "server"
	if len(args) > 0 {
		kind = args[0]
	}
	switch kind {
	case "server", "migrate":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "[bootstrap] %s accepts no arguments\n", kind)
			return 2
		}
		return runKind(kind)
	case "recover":
		if len(args) < 2 || args[1] != "status" {
			fmt.Fprintln(stderr, "[bootstrap] only recover status is supported")
			return 2
		}
		return runRecover(context.Background(), args[1:], stdout, stderr)
	case "reset-password":
		return runReset(context.Background(), args[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "[bootstrap] unknown subcommand %q; expected \"server\", \"migrate\", \"recover status\", or \"reset-password\"\n", kind)
		return 2
	}
}

// userDataMountPoints are the conventional data mounts. When one is still
// owned by root (Docker auto-created the bind directory) the directory itself
// is given to PUID:PGID; its contents are never touched.
var userDataMountPoints = []string{"/data", "/downloads", "/library"}

// establishProcessPrivileges runs first for every subcommand (server, migrate,
// recover, and anything run through `docker exec`): started as root it
// prepares the config dir and updates root for PUID:PGID and drops to that
// account for good, so every file the process and its children create belongs
// to the host account. The effective ids are exported as PUID/PGID so the
// server's own boot preparation never tries to chown to a different owner.
func establishProcessPrivileges(getenv func(string) string, setenv func(string, string) error, sys bootprepare.PrivilegeSystem) error {
	uid, gid, err := bootprepare.IDsFromEnvironment(getenv)
	if err != nil {
		return err
	}
	result, err := bootprepare.EstablishPrivileges(sys, bootprepare.PrivilegeOptions{
		UID:         uid,
		GID:         gid,
		OwnedTrees:  []string{bootsecrets.ConfigDir(getenv), deploymentidentity.UpdatesRoot(getenv)},
		MountPoints: userDataMountPoints,
	})
	if err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		logf("warning: %s", warning)
	}
	if result.Outcome == bootprepare.PrivilegesDropped {
		logf("running as %d:%d", uid, gid)
	}
	if err := setenv(bootprepare.EnvPUID, strconv.Itoa(sys.Getuid())); err != nil {
		return err
	}
	return setenv(bootprepare.EnvPGID, strconv.Itoa(sys.Getgid()))
}

// Main runs the bootstrap command after establishing the configured process
// ownership. Successful launch paths replace the current process image.
func Main(args []string) int {
	if len(args) == 1 && args[0] == "native-handoff" && filepath.IsAbs(os.Getenv(nativeAnchorEnv)) {
		return run("server")
	}
	// Only the explicit native lifecycle handoff may select a native anchor.
	_ = os.Unsetenv(nativeAnchorEnv)
	if err := establishProcessPrivileges(os.Getenv, os.Setenv, bootprepare.OSPrivilegeSystem{}); err != nil {
		logf("MAINTENANCE fail-closed: process ownership could not be established, nothing was started: %v", err)
		return 1
	}
	return dispatch(args, os.Stdin, os.Stdout, os.Stderr)
}
