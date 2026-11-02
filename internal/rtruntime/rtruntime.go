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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// Probe runs the adoption chain over the installed file.
//
// The returned client is nil when the builtin parser should be used: the file
// is absent, or it failed the chain and Info.Note says why. Probe itself never
// fails; a broken installation is a state to show, not an error to abort
// startup with.
func Probe(ctx context.Context, configRoot, builtinVersion string) (*Client, Info) {
	return probe(ctx, configRoot, BinaryPath(configRoot), builtinVersion)
}

// ProbeBinary runs the same adoption chain over an arbitrary file without
// making it the process-wide parser. The updater uses it on the temporary
// download, before anything can replace the live binary.
func ProbeBinary(ctx context.Context, path, builtinVersion string) error {
	client, info := probe(ctx, "", path, builtinVersion)
	if client == nil {
		if info.Note == "" {
			return fmt.Errorf("%s was not adopted", path)
		}
		return errors.New(info.Note)
	}
	if err := client.Close(); err != nil {
		return fmt.Errorf("close probed binary %s: %w", path, err)
	}
	return nil
}

func probe(ctx context.Context, configRoot, path, builtinVersion string) (*Client, Info) {
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

	if _, err := VersionOf(ctx, path); err != nil {
		info.Note = err.Error()
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
	if configRoot != "" {
		info.Source = installedSource(configRoot, path, reported)
	}
	info.Path = path
	info.Note = versionNote(reported.Version, builtinVersion)
	return client, info
}

// installedSource names where the installed file came from. The probe never
// trusts installed.json for what the binary is: version, line and digest come
// from running it. A channel record that matches the file exactly is the one
// fact the binary cannot report, so only that match labels the file a channel
// install; anything else is the running manual build.
func installedSource(configRoot, path string, reported Info) string {
	record, err := readInstalled(configRoot)
	if err != nil || record.Source != SourceChannel {
		return SourceManual
	}
	if !strings.EqualFold(strings.TrimPrefix(record.Version, "v"), strings.TrimPrefix(reported.Version, "v")) {
		return SourceManual
	}
	digest, err := fileDigest(path)
	if err != nil || !strings.EqualFold(digest, record.SHA256) {
		return SourceManual
	}
	return SourceChannel
}

// readInstalled reads the record the updater writes. A missing or malformed
// record is not an error: it just means nothing is known about the file.
func readInstalled(configRoot string) (Installed, error) {
	payload, err := os.ReadFile(MetadataPath(configRoot))
	if err != nil {
		return Installed{}, err
	}
	var record Installed
	if err := json.Unmarshal(payload, &record); err != nil {
		return Installed{}, err
	}
	return record, nil
}

// fileDigest is the sha256 of the installed file, the value a channel record
// must match before it may label the install.
func fileDigest(path string) (string, error) {
	handle, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, handle); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// versionNote flags a hand-placed build that is older than the compiled-in
// one. It is a note, not a refusal: the person who put the file there decides.
func versionNote(installed, builtin string) string {
	if compareVersions(installed, builtin) >= 0 {
		return ""
	}
	return fmt.Sprintf("installed %s is older than the builtin %s", installed, builtin)
}

// Newer reports whether a is strictly newer than b under the same ordering the
// release channel uses. It is the read model's comparison; the updater keeps
// its own call.
func Newer(a, b string) bool { return compareVersions(a, b) > 0 }

// compareVersions orders two versions the way the release channel does: three
// numeric fields, then a prerelease suffix ordered by semantic versioning
// ("0.0.4-test.9" is newer than "0.0.3", older than "0.0.4"). A version that
// does not parse compares as unknown, which never claims to be newer than a
// real one.
func compareVersions(a, b string) int {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	switch {
	case oka && okb:
		for index := 0; index < 3; index++ {
			if pa.core[index] != pb.core[index] {
				if pa.core[index] < pb.core[index] {
					return -1
				}
				return 1
			}
		}
		return comparePrerelease(pa.pre, pb.pre)
	case oka && !okb:
		return 1
	case !oka && okb:
		return -1
	default:
		return strings.Compare(a, b)
	}
}

// comparePrerelease orders the suffix after the dash: no suffix outranks any
// suffix, numeric identifiers compare numerically and rank below alphanumeric
// ones, and a shorter identifier list ranks first when it is a prefix.
func comparePrerelease(a, b []prereleaseID) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for index := 0; index < len(a) && index < len(b); index++ {
		left, right := a[index], b[index]
		if left.numeric && right.numeric {
			if left.value != right.value {
				if left.value < right.value {
					return -1
				}
				return 1
			}
			continue
		}
		if left.numeric != right.numeric {
			if left.numeric {
				return -1
			}
			return 1
		}
		if left.text != right.text {
			return strings.Compare(left.text, right.text)
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}

type prereleaseID struct {
	text    string
	value   int
	numeric bool
}

type versionToken struct {
	core [3]int
	pre  []prereleaseID
}

func parseVersion(version string) (versionToken, bool) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	core := version
	var suffix string
	if index := strings.IndexByte(version, '-'); index >= 0 {
		core, suffix = version[:index], version[index+1:]
		if suffix == "" {
			return versionToken{}, false
		}
	}
	fields := strings.Split(core, ".")
	if len(fields) != 3 {
		return versionToken{}, false
	}
	var token versionToken
	for index, field := range fields {
		value, ok := parseDigits(field)
		if !ok {
			return versionToken{}, false
		}
		token.core[index] = value
	}
	if suffix == "" {
		return token, true
	}
	for _, part := range strings.Split(suffix, ".") {
		if part == "" {
			return versionToken{}, false
		}
		if value, ok := parseDigits(part); ok {
			token.pre = append(token.pre, prereleaseID{text: part, value: value, numeric: true})
			continue
		}
		token.pre = append(token.pre, prereleaseID{text: part})
	}
	return token, true
}

// ArchSuffix is the platform key the release channel names an artifact by,
// e.g. "linux/amd64". It documents which artifact belongs at BinaryPath.
func ArchSuffix() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}
