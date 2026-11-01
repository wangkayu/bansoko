// Package rtruntime runs the reltitle parser as a separate process when a
// binary is installed under the configuration root, and reports why the
// in-process library is in use otherwise.
//
// The binary is the update unit: the online updater and a person replacing the
// file by hand both write one path, and both are accepted by the same chain —
// a regular file, executable, that answers `-version`, speaks rt-serve-1 in
// its hello handshake, and parses one known title. A manually placed build
// (including a self-compiled one with its own dictionary) is accepted at any
// version; the version gate belongs to the updater, not to this probe.
package rtruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// Protocol is the serve protocol this build speaks. A binary that reports
	// another one is a parser bansoko does not know how to prompt.
	Protocol = "rt-serve-1"

	// BinaryName is the one file the container reads and everything writes.
	// It carries no architecture suffix: the architecture is proven by running
	// the file, not by its name.
	BinaryName = "reltitle"

	// DirName is the directory under the configuration root.
	DirName = "rt"

	// MaxBinaryBytes bounds the executable. The shipped binaries are a few
	// megabytes; anything far larger is not this program.
	MaxBinaryBytes = 64 << 20

	// smokeTitle is parsed once before a binary is adopted. It exercises the
	// dictionary and the result encoding without depending on any particular
	// dictionary entry.
	smokeTitle = "(Test) Smoke Title S01E01 [1080p]"
)

// Source names where the running parser came from.
const (
	SourceBuiltin = "builtin"
	SourceManual  = "manual"
	SourceChannel = "channel"
)

// Info describes the parser a deployment is currently using.
type Info struct {
	// Source is SourceBuiltin, SourceManual or SourceChannel.
	Source string
	// Version is the build version the binary reports, or the compiled-in
	// module version for the library.
	Version string
	// Line is the major.minor line the binary reports.
	Line string
	// ConfigDigest is the dictionary digest ("lexicon:sha256:…").
	ConfigDigest string
	// Note explains why an installed binary was not adopted, or that a manual
	// build differs from the shipped one. Empty when there is nothing to say.
	Note string
	// Path is the installed binary this info describes ("" for builtin).
	Path string
}

// BinaryPath is the one path the updater and a person both write.
func BinaryPath(configRoot string) string {
	return filepath.Join(configRoot, DirName, BinaryName)
}

// MetadataPath records how the installed binary arrived. The probe never
// trusts it: a hand-placed file may leave it stale, so the version, digest and
// source always come from running the binary.
func MetadataPath(configRoot string) string {
	return filepath.Join(configRoot, DirName, "installed.json")
}

// Installed records one installed binary.
type Installed struct {
	Version     string    `json:"version"`
	SHA256      string    `json:"sha256"`
	Source      string    `json:"source"`
	InstalledAt time.Time `json:"installed_at"`
}

// ProbeResult is what Start learned about the installed file.
type ProbeResult struct {
	// Info is the adopted parser, or the builtin parser with a Note.
	Info Info
	// Manual records that the installed file was checked and refused, so a
	// caller can surface the reason without re-running the chain.
	Manual bool
}

// Probe runs the adoption chain over the installed file.
//
// The returned client is nil when the builtin parser should be used: the file
// is absent, or it failed the chain and Info.Note says why. Probe itself never
// fails; a broken installation is a state to show, not an error to abort
// startup with.
func Probe(ctx context.Context, configRoot, builtinVersion string) (*Client, Info) {
	path := BinaryPath(configRoot)
	info := Info{Source: SourceBuiltin, Path: path}
	if st, err := os.Lstat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			info.Note = fmt.Sprintf("%s cannot be inspected: %v", path, err)
		}
		return nil, info
	} else if st.Mode()&os.ModeSymlink != 0 {
		info.Note = fmt.Sprintf("%s is a symbolic link; a regular file is required", path)
		return nil, info
	} else if !st.Mode().IsRegular() {
		info.Note = fmt.Sprintf("%s is not a regular file", path)
		return nil, info
	} else if st.Size() > MaxBinaryBytes {
		info.Note = fmt.Sprintf("%s is larger than %d bytes", path, MaxBinaryBytes)
		return nil, info
	} else if st.Mode()&0o111 == 0 {
		info.Note = fmt.Sprintf("%s is not executable", path)
		return nil, info
	}

	client, err := Start(ctx, path)
	if err != nil {
		info.Note = err.Error()
		return nil, info
	}
	reported := client.Info()
	for field, value := range map[string]string{
		"version": reported.Version, "line": reported.Line, "digest": reported.ConfigDigest,
	} {
		if strings.TrimSpace(value) == "" {
			_ = client.Close()
			info.Note = fmt.Sprintf("%s reports an empty %s", path, field)
			return nil, info
		}
	}
	if err := client.smoke(ctx); err != nil {
		_ = client.Close()
		info.Note = fmt.Sprintf("%s failed the parse self-check: %v", path, err)
		return nil, info
	}
	info = reported
	info.Source = SourceManual
	info.Path = path
	info.Note = versionNote(reported.Version, builtinVersion)
	return client, info
}

// versionNote flags a hand-placed build that is older than the compiled-in
// one. It is a note, not a refusal: the person who put the file there decides.
func versionNote(installed, builtin string) string {
	if compareVersions(installed, builtin) >= 0 {
		return ""
	}
	return fmt.Sprintf("installed %s is older than the builtin %s", installed, builtin)
}

// compareVersions orders two versions the way the release channel does. A
// version that does not parse compares as unknown, which never claims to be
// newer than a real one.
func compareVersions(a, b string) int {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	switch {
	case oka && okb:
		for index := 0; index < 3; index++ {
			if pa[index] != pb[index] {
				if pa[index] < pb[index] {
					return -1
				}
				return 1
			}
		}
		return 0
	case oka && !okb:
		return 1
	case !oka && okb:
		return -1
	default:
		return strings.Compare(a, b)
	}
}

func parseVersion(version string) ([3]int, bool) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	var out [3]int
	parts := strings.SplitN(version, "-", 2)
	if len(parts) != 1 {
		return out, false
	}
	fields := strings.Split(parts[0], ".")
	if len(fields) != 3 {
		return out, false
	}
	for index, field := range fields {
		if field == "" {
			return out, false
		}
		value := 0
		for _, char := range field {
			if char < '0' || char > '9' {
				return out, false
			}
			value = value*10 + int(char-'0')
		}
		out[index] = value
	}
	return out, true
}

// ArchSuffix is the platform key the release channel names an artifact by,
// e.g. "linux/amd64". It documents which artifact belongs at BinaryPath.
func ArchSuffix() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}
