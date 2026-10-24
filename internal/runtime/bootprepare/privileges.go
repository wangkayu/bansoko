// Package bootprepare implements the container bootstrap privilege step: the
// entrypoint starts as root, prepares the application's own state trees for
// PUID:PGID and drops to that account for good. It performs no product work
// and stores no configuration truth.
package bootprepare

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const (
	// DefaultConfigDir is the production bind-mount anchor for generated
	// secrets and bootstrap-owned state.
	DefaultConfigDir = "/config"
	// DefaultUID/GID are the ownership a deployment gets without PUID/PGID:
	// the first regular account on most Linux hosts.
	DefaultUID = 1000
	DefaultGID = 1000
	// EnvPUID/EnvPGID are the standard container ownership variables (the same
	// names the linuxserver images use).
	EnvPUID = "PUID"
	EnvPGID = "PGID"
)

// IDsFromEnvironment parses PUID/PGID with the 1000:1000 defaults. A
// malformed value fails startup so an ownership typo can never silently fall
// back to defaults.
func IDsFromEnvironment(getenv func(string) string) (uid, gid int, err error) {
	parse := func(key string, fallback int) (int, error) {
		value := strings.TrimSpace(getenv(key))
		if value == "" {
			return fallback, nil
		}
		id, convErr := strconv.Atoi(value)
		if convErr != nil || id < 0 {
			return 0, fmt.Errorf("%s must be a non-negative integer, got %q", key, value)
		}
		return id, nil
	}
	if uid, err = parse(EnvPUID, DefaultUID); err != nil {
		return 0, 0, err
	}
	if gid, err = parse(EnvPGID, DefaultGID); err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// PrivilegeOutcome is how the process ended up running.
type PrivilegeOutcome int

const (
	// PrivilegesDropped: started as root, prepared the owned trees and now
	// runs permanently as PUID:PGID.
	PrivilegesDropped PrivilegeOutcome = iota
	// PrivilegesStayRoot: started as root with PUID=0; nothing is dropped.
	PrivilegesStayRoot
	// PrivilegesUnprivileged: started as a non-root user (compose `user:`,
	// rootless runtimes); no chown and no drop.
	PrivilegesUnprivileged
)

func (o PrivilegeOutcome) String() string {
	switch o {
	case PrivilegesDropped:
		return "dropped"
	case PrivilegesStayRoot:
		return "root"
	default:
		return "unprivileged"
	}
}

// PrivilegeOptions describes what the container entry prepares before it drops
// root.
type PrivilegeOptions struct {
	UID int
	GID int
	// OwnedTrees are the application's own small state trees (config dir,
	// updates root): created when missing and chowned recursively.
	OwnedTrees []string
	// MountPoints are user data mounts (/data ...): only the directory itself
	// is chowned, and only while it is still owned by root (the shape Docker
	// gives a bind directory it auto-creates). Their contents are never
	// touched: they can be large or remote.
	MountPoints []string
}

// PrivilegeResult reports the outcome and non-fatal observations.
type PrivilegeResult struct {
	Outcome  PrivilegeOutcome
	Warnings []string
}

// PrivilegeSystem is the narrow process and filesystem surface of the
// privilege step. Tests inject fakes; production uses OSPrivilegeSystem{}.
type PrivilegeSystem interface {
	Geteuid() int
	Getuid() int
	Getgid() int
	MkdirAll(path string, perm fs.FileMode) error
	Lstat(path string) (fs.FileInfo, error)
	Lchown(path string, uid, gid int) error
	WalkDir(root string, fn fs.WalkDirFunc) error
	// Writable reports whether the current user may create entries in dir.
	Writable(dir string) error
	Setgroups(gids []int) error
	Setgid(gid int) error
	Setuid(uid int) error
}

// OSPrivilegeSystem is the production adapter. On Linux, Go applies
// setgroups/setgid/setuid to every thread of the process.
type OSPrivilegeSystem struct{}

const accessWriteOK = 0x2 // W_OK

func (OSPrivilegeSystem) Geteuid() int { return os.Geteuid() }
func (OSPrivilegeSystem) Getuid() int  { return os.Getuid() }
func (OSPrivilegeSystem) Getgid() int  { return os.Getgid() }
func (OSPrivilegeSystem) MkdirAll(path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}
func (OSPrivilegeSystem) Lstat(path string) (fs.FileInfo, error) { return os.Lstat(path) }
func (OSPrivilegeSystem) Lchown(path string, uid, gid int) error { return os.Lchown(path, uid, gid) }
func (OSPrivilegeSystem) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}
func (OSPrivilegeSystem) Writable(dir string) error  { return syscall.Access(dir, accessWriteOK) }
func (OSPrivilegeSystem) Setgroups(gids []int) error { return syscall.Setgroups(gids) }
func (OSPrivilegeSystem) Setgid(gid int) error       { return syscall.Setgid(gid) }
func (OSPrivilegeSystem) Setuid(uid int) error       { return syscall.Setuid(uid) }

// EstablishPrivileges runs before anything else in the container entry.
//
// Started as root with PUID != 0 it creates and recursively chowns the owned
// trees, chowns root-owned mount points (directory only), then drops to
// PUID:PGID for good and verifies the drop. With PUID=0 it stays root. Started
// non-root it changes nothing and only requires the existing owned trees to be
// writable.
func EstablishPrivileges(sys PrivilegeSystem, opts PrivilegeOptions) (PrivilegeResult, error) {
	if opts.UID < 0 || opts.GID < 0 {
		return PrivilegeResult{}, errors.New("PUID/PGID must be non-negative")
	}
	if sys.Geteuid() != 0 {
		for _, tree := range opts.OwnedTrees {
			info, err := sys.Lstat(tree)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return PrivilegeResult{}, fmt.Errorf("inspect %s: %w", tree, err)
			}
			if !info.IsDir() {
				return PrivilegeResult{}, fmt.Errorf("%s is not a directory", tree)
			}
			if err := sys.Writable(tree); err != nil {
				return PrivilegeResult{}, fmt.Errorf("%s is not writable by uid %d: start the container as root with PUID/PGID, or give that user the directory (%v)",
					tree, sys.Getuid(), err)
			}
		}
		return PrivilegeResult{Outcome: PrivilegesUnprivileged}, nil
	}

	for _, tree := range opts.OwnedTrees {
		if err := sys.MkdirAll(tree, 0o750); err != nil {
			return PrivilegeResult{}, fmt.Errorf("create %s: %w", tree, err)
		}
	}
	if opts.UID == 0 {
		return PrivilegeResult{Outcome: PrivilegesStayRoot}, nil
	}
	for _, tree := range opts.OwnedTrees {
		if err := chownTreeTo(sys, tree, opts.UID, opts.GID); err != nil {
			return PrivilegeResult{}, fmt.Errorf("chown %s to %d:%d: %w", tree, opts.UID, opts.GID, err)
		}
	}
	result := PrivilegeResult{Outcome: PrivilegesDropped}
	for _, mount := range opts.MountPoints {
		info, err := sys.Lstat(mount)
		if err != nil || !info.IsDir() {
			continue
		}
		if uid, _, ok := ownerOf(info); !ok || uid != 0 {
			continue
		}
		if err := sys.Lchown(mount, opts.UID, opts.GID); err != nil {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%s is owned by root and could not be given to %d:%d (%v); give it to that account on the host", mount, opts.UID, opts.GID, err))
		}
	}

	if err := sys.Setgroups([]int{opts.GID}); err != nil {
		return PrivilegeResult{}, fmt.Errorf("setgroups(%d): %w", opts.GID, err)
	}
	if err := sys.Setgid(opts.GID); err != nil {
		return PrivilegeResult{}, fmt.Errorf("setgid(%d): %w", opts.GID, err)
	}
	if err := sys.Setuid(opts.UID); err != nil {
		return PrivilegeResult{}, fmt.Errorf("setuid(%d): %w", opts.UID, err)
	}
	if sys.Getuid() != opts.UID || sys.Geteuid() != opts.UID || sys.Getgid() != opts.GID {
		return PrivilegeResult{}, fmt.Errorf("privilege drop did not take effect: uid=%d euid=%d gid=%d, want %d:%d",
			sys.Getuid(), sys.Geteuid(), sys.Getgid(), opts.UID, opts.GID)
	}
	return result, nil
}

// chownTreeTo gives every entry of root to uid:gid without following symlinks,
// touching only entries whose ownership differs.
func chownTreeTo(sys PrivilegeSystem, root string, uid, gid int) error {
	return sys.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := sys.Lstat(path)
		if err != nil {
			return err
		}
		if owner, group, ok := ownerOf(info); ok && owner == uid && group == gid {
			return nil
		}
		return sys.Lchown(path, uid, gid)
	})
}

func ownerOf(info fs.FileInfo) (uid, gid int, ok bool) {
	if info == nil {
		return 0, 0, false
	}
	stat, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat || stat == nil {
		return 0, 0, false
	}
	return int(stat.Uid), int(stat.Gid), true
}
