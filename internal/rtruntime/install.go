package rtruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// ErrAdoption reports a downloaded binary that could not be adopted: it
// failed the candidate probe, or the install could not be made durable. The
// previous live binary has been kept or restored when this error is returned.
var ErrAdoption = errors.New("reltitle binary adoption")

// UpdateOptions describes one channel pass.
type UpdateOptions struct {
	// ConfigRoot is the deployment's configuration root ("/config").
	ConfigRoot string
	// Lane mirrors bansoko's own update lane: stable or test.
	Lane string
	// Client is the deployment's outbound HTTP client (proxy-aware); nil uses
	// a plain one.
	Client *http.Client
	// CurrentVersion is the version the deployment is actually running, which
	// decides whether the channel is an advance.
	CurrentVersion string
	// ChannelLine is the major.minor line to read. The caller sets it from the
	// reltitle version this bansoko build was compiled against: that version is
	// the compatibility claim, so a binary left on a retired line still reads
	// this build's line and can be advanced. Empty reads the line of
	// CurrentVersion.
	ChannelLine string
	// BuiltinVersion is the compiled-in parser's version. A channel build that
	// is not strictly newer than it is skipped: installing it would leave a
	// cache the next startup probe refuses to execute.
	BuiltinVersion string
	// Probe runs the adoption chain over the temporary download. Nil selects
	// ProbeBinary; tests replace it to avoid executing a fixture binary.
	Probe func(ctx context.Context, path string) error
	// Now stamps installed.json; tests replace it.
	Now func() time.Time
}

// UpdateResult reports one channel pass. Installed is false either because the
// channel offers nothing newer (Reason says so) or because the caller stopped
// before installing.
type UpdateResult struct {
	Channel   Channel
	Previous  string
	Installed bool
	Version   string
	Path      string
	Reason    string
}

// Update reads the channel and, when it offers a strictly newer build, installs
// it at BinaryPath. Nothing touches the live file until the download has proven
// its size and digest and the temporary file has passed the execution probe.
// Adoption into the running process is the caller's next step; a failed pass
// leaves the previous binary in place either way.
func Update(ctx context.Context, options UpdateOptions) (UpdateResult, error) {
	line := options.ChannelLine
	if line == "" {
		line = LineOf(options.CurrentVersion)
	}
	if _, ok := parseLine(line); !ok {
		return UpdateResult{}, fmt.Errorf("%w: channel line %q has no major.minor form", ErrChannel, line)
	}
	channel, err := FetchChannel(ctx, options.Client, options.Lane, line)
	if err != nil {
		return UpdateResult{}, err
	}
	result := UpdateResult{Channel: *channel, Previous: options.CurrentVersion, Version: options.CurrentVersion}
	if _, ok := parseVersion(options.CurrentVersion); ok && compareVersions(channel.Version, options.CurrentVersion) <= 0 {
		result.Reason = fmt.Sprintf("the channel offers %s, which is not newer than %s", channel.Version, options.CurrentVersion)
		return result, nil
	}
	// A build the compiled-in parser already matches or exceeds would install a
	// cache the startup probe then refuses to execute; skip it up front.
	if _, ok := parseVersion(options.BuiltinVersion); ok && compareVersions(channel.Version, options.BuiltinVersion) <= 0 {
		result.Reason = fmt.Sprintf("the channel offers %s, which is not newer than the builtin %s", channel.Version, options.BuiltinVersion)
		return result, nil
	}
	artifact, err := channel.ArtifactFor(ArchSuffix())
	if err != nil {
		return result, err
	}
	url, err := channel.ArtifactURL(options.Lane, artifact)
	if err != nil {
		return result, err
	}
	dir := filepath.Dir(BinaryPath(options.ConfigRoot))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return result, fmt.Errorf("%w: %v", ErrChannel, err)
	}
	temp, err := downloadToTemp(ctx, options.Client, url, artifact, dir)
	if err != nil {
		return result, err
	}
	if err := os.Chmod(temp, 0o755); err != nil {
		_ = os.Remove(temp)
		return result, fmt.Errorf("%w: make the downloaded binary executable: %v", ErrAdoption, err)
	}
	probe := options.Probe
	if probe == nil {
		probe = func(ctx context.Context, path string) error {
			return ProbeBinary(ctx, path, channel.Version, channel.Line)
		}
	}
	if err := probe(ctx, temp); err != nil {
		_ = os.Remove(temp)
		return result, fmt.Errorf("%w: %v", ErrAdoption, err)
	}
	target := BinaryPath(options.ConfigRoot)
	backup, err := backupLiveBinary(target)
	if err != nil {
		_ = os.Remove(temp)
		return result, err
	}
	if err := replaceBinary(temp, target); err != nil {
		return result, err
	}
	now := time.Now
	if options.Now != nil {
		now = options.Now
	}
	if err := writeInstalled(options.ConfigRoot, Installed{
		Version:     channel.Version,
		SHA256:      artifact.SHA256,
		Source:      SourceChannel,
		InstalledAt: now().UTC(),
	}); err != nil {
		// The probe executes the binary only with a corroborating record, so a
		// replace whose record cannot be written must not leave the new file
		// live: put the previous binary back (or remove the new one) and fail.
		restoreLiveBinary(target, backup)
		_ = syncDir(filepath.Dir(target))
		return result, fmt.Errorf("%w: the metadata file could not be written: %v", ErrAdoption, err)
	}
	if backup != "" {
		_ = os.Remove(backup)
	}
	result.Installed = true
	result.Version = channel.Version
	result.Path = target
	return result, nil
}

// backupLiveBinary hard-links the current live binary aside so a failed
// install can put it back. It returns "" when nothing is installed yet.
func backupLiveBinary(target string) (string, error) {
	if _, err := os.Lstat(target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("%w: inspect the live binary: %v", ErrChannel, err)
	}
	backup := target + ".prev"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: remove a stale backup: %v", ErrChannel, err)
	}
	if err := os.Link(target, backup); err == nil {
		return backup, nil
	}
	// A filesystem without hard links gets a plain copy instead.
	data, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("%w: read the live binary: %v", ErrChannel, err)
	}
	if err := os.WriteFile(backup, data, 0o755); err != nil {
		return "", fmt.Errorf("%w: copy the live binary aside: %v", ErrChannel, err)
	}
	return backup, nil
}

// restoreLiveBinary undoes a replace: the backup takes the live path back,
// or the new file is removed when nothing was installed before.
func restoreLiveBinary(target, backup string) {
	if backup == "" {
		_ = os.Remove(target)
		return
	}
	_ = os.Rename(backup, target)
}
