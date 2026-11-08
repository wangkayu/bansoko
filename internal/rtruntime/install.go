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

// ErrAdoption reports a downloaded binary that failed the execution probe. The
// live file has not been replaced when this error is returned.
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
	// BuiltinVersion lets the default probe note an older-than-builtin binary.
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
			return ProbeBinary(ctx, path, options.BuiltinVersion)
		}
	}
	if err := probe(ctx, temp); err != nil {
		_ = os.Remove(temp)
		return result, fmt.Errorf("%w: %v", ErrAdoption, err)
	}
	target := BinaryPath(options.ConfigRoot)
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
		// The binary is already in place; a stale metadata file costs a probe,
		// which never trusts it anyway.
		result.Installed = true
		result.Version = channel.Version
		result.Path = target
		result.Reason = "installed, but the metadata file could not be written: " + err.Error()
		return result, nil
	}
	result.Installed = true
	result.Version = channel.Version
	result.Path = target
	return result, nil
}
