// Package rtruntime runs the reltitle parser as a separate process when a
// channel-installed binary sits under the configuration root, and reports why
// the in-process library is in use otherwise.
//
// The binary is the update unit and only the official channel may write one:
// a file at the install path is executed only after installed.json proves it
// arrived through the channel (source, a release version, and the file's
// sha256), and only while that version sits on the builtin parser's
// major.minor line and strictly ahead of it. A manually placed or
// self-compiled build is never executed; upgrading bansoko quietly supersedes
// a cached channel binary the builtin parser has caught up with.
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
	SourceChannel = "channel"
)

// Info describes the parser a deployment is currently using.
type Info struct {
	// Source is SourceBuiltin or SourceChannel.
	Source string
	// Version is the build version the binary reports, or the compiled-in
	// module version for the library.
	Version string
	// Line is the major.minor line the binary reports.
	Line string
	// ConfigDigest is the dictionary digest ("lexicon:sha256:…").
	ConfigDigest string
	// Note explains why an installed binary was not adopted. Empty when there
	// is nothing to say — in particular when an upgrade quietly supersedes a
	// cached channel binary the builtin parser has caught up with.
	Note string
	// Path is the installed binary this info describes ("" for builtin).
	Path string
}

// BinaryPath is the one path the channel updater writes.
func BinaryPath(configRoot string) string {
	return filepath.Join(configRoot, DirName, BinaryName)
}

// MetadataPath records how the installed binary arrived. The probe executes
// the binary only while this record proves a channel install: the channel
// source, a release version, and the file's sha256.
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
// is absent, or it failed the chain and Info.Note says why. A superseded
// channel cache (an older, equal or cross-line build) falls back with an
// empty Note. Probe itself never fails; a broken installation is a state to
// show, not an error to abort startup with.
func Probe(ctx context.Context, configRoot, builtinVersion string) (*Client, Info) {
	return probe(ctx, configRoot, BinaryPath(configRoot), builtinVersion)
}

// ProbeBinary validates a freshly downloaded candidate before it replaces the
// live binary: the file checks, `-version`, the rt-serve-1 handshake with
// non-empty fields, an exact version and line match against the channel
// manifest, and one smoke parse. It needs no installed.json — the record is
// written only after the candidate passes and lands at BinaryPath.
func ProbeBinary(ctx context.Context, path, expectedVersion, expectedLine string) error {
	problem, missing := binaryFileProblem(path)
	if missing {
		return fmt.Errorf("%s does not exist", path)
	}
	if problem != "" {
		return errors.New(problem)
	}
	if _, err := VersionOf(ctx, path); err != nil {
		return err
	}
	client, err := Start(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	reported := client.Info()
	for field, value := range map[string]string{
		"version": reported.Version, "line": reported.Line, "digest": reported.ConfigDigest,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s reports an empty %s", path, field)
		}
	}
	if !sameVersion(reported.Version, expectedVersion) {
		return fmt.Errorf("%s reports version %s, but the channel manifest offers %s", path, reported.Version, expectedVersion)
	}
	if reported.Line != expectedLine {
		return fmt.Errorf("%s reports line %s, but the channel manifest is for line %s", path, reported.Line, expectedLine)
	}
	if err := client.smoke(ctx); err != nil {
		return fmt.Errorf("%s failed the parse self-check: %v", path, err)
	}
	return nil
}

func probe(ctx context.Context, configRoot, path, builtinVersion string) (*Client, Info) {
	info := Info{Source: SourceBuiltin, Path: path}
	problem, missing := binaryFileProblem(path)
	if missing {
		return nil, info
	}
	if problem != "" {
		info.Note = problem
		return nil, info
	}

	// Nothing executes before installed.json proves a channel install: a file
	// without a corroborating record is a manually placed or leftover binary,
	// and this build never runs one of those.
	record, note := verifyInstalled(configRoot, path)
	if note != "" {
		info.Note = note
		return nil, info
	}

	// A cached channel binary the builtin parser has caught up with — or one
	// left on another major.minor line after an upgrade — is superseded, not
	// broken: fall back quietly, without executing it.
	builtinLine := LineOf(builtinVersion)
	if LineOf(record.Version) != builtinLine || compareVersions(record.Version, builtinVersion) <= 0 {
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

	// The handshake must corroborate the record that allowed execution and
	// still lead the builtin parser: a binary answering anything else is not
	// the install the record describes.
	if !sameVersion(reported.Version, record.Version) {
		_ = client.Close()
		info.Note = fmt.Sprintf("%s reports version %s, but installed.json records %s", path, reported.Version, record.Version)
		return nil, info
	}
	if reported.Line != builtinLine {
		_ = client.Close()
		info.Note = fmt.Sprintf("%s reports line %s, but the builtin parser is on line %s", path, reported.Line, builtinLine)
		return nil, info
	}
	if compareVersions(reported.Version, builtinVersion) <= 0 {
		_ = client.Close()
		info.Note = fmt.Sprintf("%s reports %s, which is not newer than the builtin %s", path, reported.Version, builtinVersion)
		return nil, info
	}
	if err := client.smoke(ctx); err != nil {
		_ = client.Close()
		info.Note = fmt.Sprintf("%s failed the parse self-check: %v", path, err)
		return nil, info
	}
	client.info.Source = SourceChannel
	info = client.Info()
	return client, info
}

// binaryFileProblem applies the file safety checks: the install path must be
// a regular, executable file of a plausible size. missing reports the
// ordinary first start, which is no problem at all.
func binaryFileProblem(path string) (problem string, missing bool) {
	st, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", true
		}
		return fmt.Sprintf("%s cannot be inspected: %v", path, err), false
	}
	switch {
	case st.Mode()&os.ModeSymlink != 0:
		return fmt.Sprintf("%s is a symbolic link; a regular file is required", path), false
	case !st.Mode().IsRegular():
		return fmt.Sprintf("%s is not a regular file", path), false
	case st.Size() > MaxBinaryBytes:
		return fmt.Sprintf("%s is larger than %d bytes", path, MaxBinaryBytes), false
	case st.Mode()&0o111 == 0:
		return fmt.Sprintf("%s is not executable", path), false
	}
	return "", false
}

// verifyInstalled reads installed.json and proves it describes the file on
// disk. The record is what lets a binary execute at all, so every failure is
// a refusal with an explanation, never a reason to run the file anyway.
func verifyInstalled(configRoot, path string) (Installed, string) {
	refuse := func(reason string) (Installed, string) {
		return Installed{}, fmt.Sprintf("%s is not executed: %s; only a binary installed through the official update channel is adopted", path, reason)
	}
	record, err := readInstalled(configRoot)
	if err != nil {
		return refuse("installed.json is missing or malformed")
	}
	if record.Source != SourceChannel {
		return refuse(fmt.Sprintf("installed.json records source %q, not %q", record.Source, SourceChannel))
	}
	if _, ok := parseVersion(record.Version); !ok {
		return refuse(fmt.Sprintf("installed.json version %q is not a release version", record.Version))
	}
	digest, err := fileDigest(path)
	if err != nil {
		return refuse(fmt.Sprintf("the file cannot be read: %v", err))
	}
	if !strings.EqualFold(digest, record.SHA256) {
		return refuse("the file's sha256 does not match installed.json")
	}
	return record, ""
}

// sameVersion compares two versions ignoring an optional leading "v".
func sameVersion(a, b string) bool {
	return strings.EqualFold(strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v"))
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
