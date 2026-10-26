package filesystem

import (
	"bansoko/internal/fileidentity"
	maintenance "bansoko/internal/filemaintenance/executor"
	"errors"
	"path/filepath"
	"strings"
)

// Owners that capture a file into their private namespace before removing it:
// the replacement-loser deletion (cleanup owner), and a user delete of the
// file manager. Each pairs one capture kind with one purge kind of the receipt
// namespace; the cleanup kinds name the two stages of one cleanup_delete
// work item.
const (
	cleanupOwnerType          = "cleanup_operation"
	cleanupCaptureKind        = "cleanup_capture"
	cleanupPurgeKind          = "cleanup_purge"
	fileManagerOwnerType      = "file_manager_operation"
	fileManagerQuarantineKind = "file_manager_quarantine"
	fileManagerPurgeKind      = "file_manager_purge"
)

func capturesIntoNamespace(ownerType string) bool {
	return ownerType == cleanupOwnerType || ownerType == fileManagerOwnerType
}

// isQuarantineInvocation reports a capture move into the owner namespace.
func isQuarantineInvocation(i maintenance.MaintenanceInvocation) bool {
	return (i.Kind == cleanupCaptureKind && i.OwnerType == cleanupOwnerType) ||
		(i.Kind == fileManagerQuarantineKind && i.OwnerType == fileManagerOwnerType)
}

// isPurgeInvocation reports the irreversible removal of a captured object.
func isPurgeInvocation(i maintenance.MaintenanceInvocation) bool {
	return i.Kind == cleanupPurgeKind || i.Kind == fileManagerPurgeKind
}

func validateMaintenanceNamespace(i maintenance.MaintenanceInvocation) error {
	if !capturesIntoNamespace(i.OwnerType) {
		return nil
	}
	if i.OwnerType == fileManagerOwnerType && i.Kind != fileManagerQuarantineKind && i.Kind != fileManagerPurgeKind {
		// A user move or copy targets an ordinary path; only its delete
		// stages are bound to the private namespace.
		return nil
	}
	if i.OwnerID == "" || strings.ContainsAny(i.OwnerID, "/\\\x00") || i.OwnerID == "." || i.OwnerID == ".." {
		return errors.New("invalid cleanup owner namespace")
	}
	if filepath.Base(i.Target) != "object" || filepath.Base(filepath.Dir(i.Target)) != ".bansoko-cleanup-"+i.OwnerID {
		return errors.New("cleanup namespace does not bind exact owner")
	}
	return nil
}
func directoryPathMatches(d *verifiedDirectory) error {
	identity, err := fileidentity.FromPath(d.canonical)
	if err != nil {
		return err
	}
	if !fileidentity.Matches(identity, d.identity) {
		return errors.New("verified directory renamed/replaced during effect")
	}
	return nil
}
