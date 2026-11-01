package rtruntime

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

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
	// decides the line to read and whether the channel is an advance.
	CurrentVersion string
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
// its size and digest, and a failed pass leaves the previous binary in place.
// Adoption of the new file is the caller's next step: the file being correct is
// not the same as the binary being able to answer.
func Update(ctx context.Context, options UpdateOptions) (UpdateResult, error) {
	line := LineOf(options.CurrentVersion)
	if line == "" {
		return UpdateResult{}, fmt.Errorf("%w: current version %q has no major.minor line", ErrChannel, options.CurrentVersion)
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
