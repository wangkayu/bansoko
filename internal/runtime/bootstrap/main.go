// Package bootstrap implements the unified binary's bootstrap mode. The image
// anchor is the immutable PID1 entrypoint baked into the base image. Its contract
// (embedded self-update program, design §2):
//
//   - /app is never overlaid: the anchor and base application are part of the
//     of the digest-addressed base image and can never be replaced by update
//     payload content;
//   - overlay release trees live on the updates volume
//     (/var/lib/bansoko/updates by default) in releases/<release-id>/, with
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
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	hardeningdomain "bansoko/internal/hardening/domain"
	"bansoko/internal/runtime/bootprepare"
	"bansoko/internal/runtime/bootsecrets"
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
// unknown fields are cleared by writing them empty. The base image digest is
// passed through from the deployment environment (the container image itself
// is the source of that layer).
//
// The bootstrap version is this binary's own constant, not an inherited value:
// the running /app/bansoko-server is the only authority for what the base image
// contains, so the updater's comparison can never be satisfied by deployment
// environment text alone.
func deploymentIdentityEnv(source, releaseID, payloadDigest, operationID string) map[string]string {
	baseDigest := strings.TrimSpace(os.Getenv(deploymentidentity.EnvBaseImageDigest))
	return map[string]string{
		deploymentidentity.EnvSource:               source,
		deploymentidentity.EnvSelectedReleaseID:    releaseID,
		deploymentidentity.EnvPayloadDigest:        payloadDigest,
		deploymentidentity.EnvUpdateOperationID:    operationID,
		deploymentidentity.EnvBaseImageDigest:      baseDigest,
		deploymentidentity.EnvExpectedProcessBuild: "",
		deploymentidentity.EnvBootstrapVersion:     embeddedBootstrapVersion,
	}
}

func deploymentIdentityEnvWithBuild(source, releaseID, payloadDigest, operationID, expectedBuildID string) map[string]string {
	env := deploymentIdentityEnv(source, releaseID, payloadDigest, operationID)
	env[deploymentidentity.EnvExpectedProcessBuild] = strings.TrimSpace(expectedBuildID)
	return env
}

type overlayIdentityProof struct {
	PayloadDigest          string
	OperationID            string
	ExpectedProcessBuildID string
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
	if manifest.EmbeddedUpdate == nil {
		return overlayIdentityProof{}, errors.New("verified release has no embedded update identity")
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
	maintenanceOp    string // operation id when parked on journal state
	maintenanceStep  string // journal step string when parked
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

	rootInfo, statErr := os.Stat(updatesRoot)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		return basePlan(out, "no updates volume present")
	case statErr != nil:
		return maintenancePlan(out, "", "", fmt.Sprintf("updates volume %s unusable: %v", updatesRoot, statErr))
	case !rootInfo.IsDir():
		return maintenancePlan(out, "", "", fmt.Sprintf("updates path %s is not a directory", updatesRoot))
	}

	entrypointJournal, err := embeddedupdate.Open(filepath.Join(updatesRoot, journalFileName))
	if err != nil {
		if errors.Is(err, embeddedupdate.ErrJournalCorrupted) {
			return maintenancePlan(out, "", "", fmt.Sprintf("%v (corrupt file quarantined for diagnosis)", err))
		}
		return maintenancePlan(out, "", "", fmt.Sprintf("journal unavailable: %v", err))
	}
	defer func() {
		if closeErr := entrypointJournal.Close(); closeErr != nil {
			out = maintenancePlan(out, out.maintenanceOp, out.maintenanceStep,
				fmt.Sprintf("update journal close failed: %v", closeErr))
		}
	}()

	record, found, loadErr := entrypointJournal.Load()
	if loadErr != nil {
		return maintenancePlan(out, "", "", fmt.Sprintf("journal unreadable after open: %v", loadErr))
	}
	mode := updateMode()
	if !config.IsValidUpdateMode(mode) {
		return maintenancePlan(out, "", "", fmt.Sprintf("unsupported BANSOKO_UPDATE_MODE %q", mode))
	}
	markerPath := filepath.Join(updatesRoot, pendingMarkerName)
	if _, markerErr := os.Lstat(markerPath); markerErr == nil {
		if kind == "server" && mode == "embedded" {
			out.mode = modePendingMarker
			out.reason = "pending embedded update marker"
			return out
		}
		return maintenancePlan(out, "", "", "pending embedded update marker requires embedded server bootstrap")
	} else if !errors.Is(markerErr, os.ErrNotExist) {
		return maintenancePlan(out, "", "", fmt.Sprintf("pending marker unavailable: %v", markerErr))
	}
	// rollback_blocked is a terminal journal step but remains active evidence:
	// it deliberately keeps the deployment fenced until an operator proves
	// recovery. Non-embedded modes must never consume any embedded evidence.
	activeEmbeddedEvidence := found && (record.Active() || record.Step == embeddedupdate.StepRollbackBlocked)
	if activeEmbeddedEvidence && mode != "embedded" {
		return maintenancePlan(out, record.OperationID, string(record.Step),
			fmt.Sprintf("active embedded update requires BANSOKO_UPDATE_MODE=embedded (current mode %q)", mode))
	}
	if found && record.Step == embeddedupdate.StepRollbackBlocked {
		// A rollback-blocked terminal record is deliberately not a normal
		// terminal outcome. It is the durable proof that neither the target nor
		// the fallback was proven safe, so PID1 must keep the instance parked in
		// maintenance and preserve the open fence for operator recovery.
		return maintenancePlan(out, record.OperationID, string(record.Step),
			"rollback is blocked; serving must remain fail-closed")
	}
	if found && record.Step == embeddedupdate.StepFenceClosurePending && kind != "server" {
		return maintenancePlan(out, record.OperationID, string(record.Step),
			"owner-scoped fence closure must be retried by the server bootstrap consumer")
	}
	if found && record.Active() {
		if kind == "server" && consumeFamilySteps[record.Step] {
			out.mode = modeConsume
			out.maintenanceOp = record.OperationID
			out.maintenanceStep = string(record.Step)
			out.handoffReleaseID = record.TargetReleaseID
			if record.Step == embeddedupdate.StepFenceClosurePending && record.FenceClosureTarget != embeddedupdate.StepSucceeded {
				// Rollback and pre-migration abort remove the rejected target before
				// their retryable fence closure. Continue from the verified fallback
				// release instead of handing off to a tree that no longer exists.
				out.handoffReleaseID = record.PreviousReleaseID
			}
			out.reason = "embedded-update handoff consumption (" + string(record.Step) + ")"
			return out
		}
		// Everything else (pre-handoff updater checkpoints on any kind, and
		// handoff-family steps seen from the migrate job) fails closed into
		// maintenance so a half-applied state stays observable instead of
		// being silently skipped or racing the consuming process.
		return maintenancePlan(out, record.OperationID, string(record.Step),
			"active update operation needs manual recovery")
	}

	target, resolveErr := resolveCurrentRelease(kind, updatesRoot)
	if resolveErr != nil {
		pointer := deploymentidentity.ReadCurrentPointer(updatesRoot)
		if pointer.Presented {
			return maintenancePlan(out, "", "", fmt.Sprintf("current overlay pointer is not usable: %v", resolveErr))
		}
		// No pointer is the normal base-image state after a clean rollback or on a
		// fresh updates volume. A present but broken pointer must never be treated
		// as an invitation to silently downgrade to base.
	} else if _, verifyErr := verifyOverlayIdentity(updatesRoot, target.releaseID); verifyErr != nil {
		return maintenancePlan(out, "", "", fmt.Sprintf("current overlay release identity is not provable: %v", verifyErr))
	} else {
		return overlayPlan(out, target.releaseID, target.binaryPath, target.binaryPath,
			filepath.Join(target.path, "db/migrations"),
			filepath.Join(target.path, "frontend/dist"))
	}
	return basePlan(out, "overlay not usable")
}

func basePlan(out plan, reason string) plan {
	out.mode = modeBase
	if out.kind == "migrate" {
		out.binaryPath = baseServerPath
		out.schemaDir = baseSchemaDir
	} else {
		out.binaryPath = baseServerPath
		out.migratePath = baseServerPath
		out.schemaDir = baseSchemaDir
		out.frontendDir = baseFrontendDir
	}
	out.reason = reason
	return out
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
	raw, err := os.ReadFile(filepath.Join(updatesRoot, currentPointerName))
	if err != nil {
		return overlayTarget{}, fmt.Errorf("read current pointer: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if !validReleaseToken(token) {
		return overlayTarget{}, fmt.Errorf("current pointer carries an invalid release token %q", token)
	}
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

// verifyReleaseTree proves the current release against the immutable public
// key baked into the base image and the retained signed payload descriptor.
// A writable metadata file by itself is never sufficient to launch overlay
// code.
func verifyReleaseTree(releaseDir string) error {
	_, err := verifyReleaseTreeMetadata(releaseDir)
	return err
}

func verifyReleaseTreeIdentity(releaseDir string) (packagemaker.Metadata, hardeningdomain.ReleaseManifest, error) {
	publicKey, err := loadBootstrapPublicKey()
	if err != nil {
		return packagemaker.Metadata{}, hardeningdomain.ReleaseManifest{}, err
	}
	return updateapplication.VerifyReleaseTreeIdentity(releaseDir, publicKey)
}

func verifyReleaseTreeMetadata(releaseDir string) (packagemaker.Metadata, error) {
	publicKey, err := loadBootstrapPublicKey()
	if err != nil {
		return packagemaker.Metadata{}, err
	}
	return updateapplication.VerifyReleaseTreeMetadata(releaseDir, publicKey)
}

func loadBootstrapPublicKey() (ed25519.PublicKey, error) {
	override := strings.TrimSpace(os.Getenv("BANSOKO_UPDATE_PUBLIC_KEY_PATH"))
	if override != "" {
		data, err := os.ReadFile(override)
		if err != nil {
			return nil, fmt.Errorf("read pinned release public key: %w", err)
		}
		return decodeBootstrapPublicKey(data)
	}
	var lastErr error
	for _, path := range []string{"/app/public.key", "deployment/public.key"} {
		data, err := os.ReadFile(path)
		if err == nil {
			return decodeBootstrapPublicKey(data)
		}
		lastErr = err
	}
	return nil, fmt.Errorf("read release public key: %w", lastErr)
}

func decodeBootstrapPublicKey(data []byte) (ed25519.PublicKey, error) {
	value := strings.TrimSpace(string(data))
	if decoded, decodeErr := hex.DecodeString(value); decodeErr == nil && len(decoded) == ed25519.PublicKeySize {
		return ed25519.PublicKey(decoded), nil
	}
	if decoded, decodeErr := base64.RawStdEncoding.DecodeString(value); decodeErr == nil && len(decoded) == ed25519.PublicKeySize {
		return ed25519.PublicKey(decoded), nil
	}
	return nil, errors.New("release public key is not a 32-byte hex or raw-base64 key")
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
	resolved, err := bootsecrets.Resolve(configDir, getenv, nil)
	if err != nil {
		return err
	}
	if len(resolved.Generated) > 0 {
		log("generated %s and stored them in %s; keep this file with the database when moving the deployment",
			strings.Join(resolved.Generated, ", "), resolved.Path)
	}
	runtimeDSN, err := bootsecrets.RuntimeDatabaseURL(ownerDSN, resolved.Values.DatabaseRuntimePassword)
	if err != nil {
		return err
	}
	for key, value := range map[string]string{
		bootsecrets.EnvRuntimeDatabaseURL:  runtimeDSN,
		bootsecrets.EnvSessionSecret:       resolved.Values.SessionSecret,
		bootsecrets.EnvSecretEncryptionKey: resolved.Values.SecretEncryptionKey,
	} {
		if err := setenv(key, value); err != nil {
			return fmt.Errorf("export %s: %w", key, err)
		}
	}
	return nil
}

// runMigrationChild applies the selected tree's schema with the owner
// connection and converges the runtime login role. Its structured output goes
// to the container log.
func runMigrationChild(command processCommand, env []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), migrateTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, command.path, command.args...)
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
	if kind == "server" && chosen.mode != modePendingMarker {
		if err := discardPreMarkerWork(updatesRoot); err != nil {
			logf("MAINTENANCE fail-closed: pre-marker workspace cleanup failed: %v", err)
			blockForever()
			return 124
		}
		chosen = planLaunch(kind, updatesRoot)
	}
	switch chosen.mode {
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
	pairs := map[string]string{envSchemaDir: chosen.schemaDir}
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
		// Embedded deployments verify the running tree against the update
		// chain, so a base launch carries the builder-injected immutable image
		// digest. A manual deployment pulled by tag has no digest to prove:
		// no identity facts are injected and readiness reports the identity as
		// not provided instead of unready.
		if updateMode() == "embedded" {
			for key, value := range deploymentIdentityEnv(deploymentidentity.SourceBase, "", "", "") {
				pairs[key] = value
			}
		}
	}
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
	if err := establishProcessPrivileges(os.Getenv, os.Setenv, bootprepare.OSPrivilegeSystem{}); err != nil {
		logf("MAINTENANCE fail-closed: process ownership could not be established, nothing was started: %v", err)
		return 1
	}
	return dispatch(args, os.Stdin, os.Stdout, os.Stderr)
}
