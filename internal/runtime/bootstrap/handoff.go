package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
)

var currentExecutable = os.Executable

// handoffUnifiedBootstrap lets the verified target release own its own consume
// and startup state machine. The image anchor still verifies every handoff;
// the target repeats the normal plan and verification after exec.
func handoffUnifiedBootstrap(chosen plan, updatesRoot string) (int, bool) {
	if chosen.kind != "server" {
		return 0, false
	}
	releaseID := ""
	switch chosen.mode {
	case modeOverlay, modeConsume:
		releaseID = chosen.handoffReleaseID
		if chosen.mode == modeOverlay {
			releaseID = chosen.releaseID
		}
	case modePendingMarker:
		marker, err := readPendingMarker(filepath.Join(updatesRoot, pendingMarkerName))
		if err != nil {
			// The consume path owns malformed-marker diagnosis and fail-closed
			// handling; there is no verified target to hand off in this case.
			return 0, false
		}
		releaseID = marker.ReleaseID
	default:
		return 0, false
	}
	if releaseID == "" {
		return 0, false
	}
	dir := relDirOf(updatesRoot, releaseID)
	meta, err := verifyReleaseTreeMetadata(dir)
	if err == nil {
		err = validateUnifiedReleaseMetadata(meta)
	}
	if err != nil {
		return handoffFailure(chosen, updatesRoot, fmt.Errorf("verify bootstrap handoff target %s: %w", releaseID, err)), true
	}
	target := releaseServerBin(dir)
	same, err := isCurrentExecutable(target)
	if err != nil {
		return handoffFailure(chosen, updatesRoot, fmt.Errorf("compare bootstrap handoff executable: %w", err)), true
	}
	if same {
		return 0, false
	}
	logf("handing off bootstrap to verified unified release %s", releaseID)
	if err := execProcess(target, []string{target, "bootstrap"}, os.Environ()); err != nil {
		logf("bootstrap handoff exec failed: %v", err)
		return 1, true
	}
	return 0, true
}

func isCurrentExecutable(target string) (bool, error) {
	current, err := currentExecutable()
	if err != nil {
		return false, err
	}
	currentInfo, err := os.Stat(current)
	if err != nil {
		return false, err
	}
	targetInfo, err := os.Stat(target)
	if err != nil {
		return false, err
	}
	return os.SameFile(currentInfo, targetInfo), nil
}

func handoffFailure(chosen plan, updatesRoot string, err error) int {
	logf("MAINTENANCE fail-closed: %v", err)
	if serveErr := serveMaintenance(updatesRoot, chosen.maintenanceStep, chosen.maintenanceOp); serveErr != nil {
		logf("MAINTENANCE responder failed closed: %v", serveErr)
		return 1
	}
	return 0
}
