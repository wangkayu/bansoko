package rtruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The channel is reltitle's own publishing line, independent of bansoko's app
// channel: one branch per lane, one directory per major.minor line. bansoko
// only ever reads this repository and never names another.
const (
	// ChannelSchema is the document shape this build understands.
	ChannelSchema = "rt-channel-1"

	channelRepositoryRoot = "https://raw.githubusercontent.com/wangkayu/reltitle/"
	channelStableBranch   = "update-channel"
	channelTestBranch     = "update-channel-test"

	// maxChannelBytes bounds the manifest document. A channel manifest is a few
	// kilobytes; anything larger is hostile input.
	maxChannelBytes = 1 << 20

	// channelTimeout bounds one channel request end to end.
	channelTimeout = 30 * time.Second
)

// channelRootOverride replaces the channel repository root in tests. It is a
// variable only so a test can point the reader at a local server; the program
// never assigns it, which is what keeps a deployment on this project's own
// channel.
var channelRootOverride = channelRepositoryRoot

// ErrChannel reports a channel document or download this build refuses:
// malformed, for another line or protocol, or failing its own digest.
var ErrChannel = errors.New("reltitle channel")

// ErrNoNewerBuild reports a channel that answered properly but offers nothing
// newer than the running parser. It is not a failure: the caller reports it.
var ErrNoNewerBuild = errors.New("reltitle channel has no newer build")

// ChannelNotes carries one release's notes in both languages, already joined
// into markdown text. Either side may be empty.
type ChannelNotes struct {
	ZhCN string
	EN   string
}

// ChannelArtifact is one platform's published binary.
type ChannelArtifact struct {
	Path   string
	SHA256 string
	Size   int64
}

// Channel is one decoded channel document.
type Channel struct {
	SchemaVersion string
	Line          string
	Protocol      string
	Version       string
	Sequence      int64
	PublishedAt   time.Time
	Notes         ChannelNotes
	Artifacts     map[string]ChannelArtifact
	ReleaseURL    string
}

// ChannelURL resolves one lane's manifest URL for a major.minor line.
func ChannelURL(lane, line string) (string, error) {
	branch, err := channelBranch(lane)
	if err != nil {
		return "", err
	}
	if _, ok := parseLine(line); !ok {
		return "", fmt.Errorf("%w: %q is not a major.minor line", ErrChannel, line)
	}
	return channelRootOverride + branch + "/lines/" + line + "/channel.json", nil
}

// channelBranch mirrors bansoko's own lane choice onto reltitle's two branches,
// so a test deployment takes test builds of both.
func channelBranch(lane string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(lane)) {
	case "", "stable":
		return channelStableBranch, nil
	case "test":
		return channelTestBranch, nil
	default:
		return "", fmt.Errorf("%w: unknown lane %q", ErrChannel, lane)
	}
}

// LineOf is the major.minor line a version belongs to, e.g. "0.0" for both
// 0.0.3 and 0.0.4-test.9.
func LineOf(version string) string {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	parts := strings.SplitN(version, "-", 2)
	fields := strings.Split(parts[0], ".")
	if len(fields) < 2 || fields[0] == "" || fields[1] == "" {
		return ""
	}
	return fields[0] + "." + fields[1]
}

func parseLine(line string) ([2]int, bool) {
	var out [2]int
	fields := strings.Split(strings.TrimSpace(line), ".")
	if len(fields) != 2 {
		return out, false
	}
	for index, field := range fields {
		value, ok := parseDigits(field)
		if !ok {
			return out, false
		}
		out[index] = value
	}
	return out, true
}

func parseDigits(field string) (int, bool) {
	if field == "" {
		return 0, false
	}
	value := 0
	for _, char := range field {
		if char < '0' || char > '9' {
			return 0, false
		}
		value = value*10 + int(char-'0')
	}
	return value, true
}

// rawChannel mirrors the published document. Unknown fields are ignored on
// purpose: a schema change bumps schema_version, which is refused below, so
// additive fields from a newer publisher cannot break an older reader.
type rawChannel struct {
	SchemaVersion string                 `json:"schema_version"`
	Line          string                 `json:"line"`
	Protocol      string                 `json:"protocol"`
	Version       string                 `json:"version"`
	Sequence      int64                  `json:"sequence"`
	PublishedAt   time.Time              `json:"published_at"`
	Notes         json.RawMessage        `json:"notes"`
	Artifacts     map[string]rawArtifact `json:"artifacts"`
	ReleaseURL    string                 `json:"release_url"`
}

type rawArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// FetchChannel reads and validates one lane's manifest.
func FetchChannel(ctx context.Context, client *http.Client, lane, line string) (*Channel, error) {
	url, err := ChannelURL(lane, line)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, channelTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChannel, err)
	}
	response, err := httpClientOf(client).Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch %s: %v", ErrChannel, url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s answered %s", ErrChannel, url, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxChannelBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrChannel, url, err)
	}
	if len(body) > maxChannelBytes {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrChannel, url, maxChannelBytes)
	}
	return DecodeChannel(body, line)
}

// DecodeChannel validates one manifest document against the line and protocol
// this build speaks.
func DecodeChannel(body []byte, line string) (*Channel, error) {
	var raw rawChannel
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: manifest is not JSON: %v", ErrChannel, err)
	}
	return validateChannel(raw, line)
}

func validateChannel(raw rawChannel, line string) (*Channel, error) {
	if raw.SchemaVersion != ChannelSchema {
		return nil, fmt.Errorf("%w: schema %q, want %q", ErrChannel, raw.SchemaVersion, ChannelSchema)
	}
	if raw.Protocol != Protocol {
		return nil, fmt.Errorf("%w: channel speaks %q, this build speaks %q", ErrChannel, raw.Protocol, Protocol)
	}
	if raw.Line != line {
		return nil, fmt.Errorf("%w: channel is for line %q, not %q", ErrChannel, raw.Line, line)
	}
	if _, ok := parseVersion(raw.Version); !ok {
		return nil, fmt.Errorf("%w: version %q is not a release version", ErrChannel, raw.Version)
	}
	if raw.Sequence <= 0 {
		return nil, fmt.Errorf("%w: sequence %d is not a publication order", ErrChannel, raw.Sequence)
	}
	if len(raw.Artifacts) == 0 {
		return nil, fmt.Errorf("%w: the channel lists no artifacts", ErrChannel)
	}
	artifacts := make(map[string]ChannelArtifact, len(raw.Artifacts))
	for platform, artifact := range raw.Artifacts {
		if strings.TrimSpace(platform) == "" || platform != strings.TrimSpace(platform) {
			return nil, fmt.Errorf("%w: artifact platform %q is not a platform key", ErrChannel, platform)
		}
		if err := validateArtifact(artifact); err != nil {
			return nil, fmt.Errorf("%w: artifact %s: %v", ErrChannel, platform, err)
		}
		artifacts[platform] = ChannelArtifact{Path: artifact.Path, SHA256: strings.ToLower(artifact.SHA256), Size: artifact.Size}
	}
	notes, err := decodeNotes(raw.Notes)
	if err != nil {
		return nil, err
	}
	return &Channel{
		SchemaVersion: raw.SchemaVersion,
		Line:          raw.Line,
		Protocol:      raw.Protocol,
		Version:       strings.TrimPrefix(raw.Version, "v"),
		Sequence:      raw.Sequence,
		PublishedAt:   raw.PublishedAt,
		Notes:         notes,
		Artifacts:     artifacts,
		ReleaseURL:    raw.ReleaseURL,
	}, nil
}

func validateArtifact(artifact rawArtifact) error {
	path := strings.TrimSpace(artifact.Path)
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return fmt.Errorf("path %q is not a relative artifact path", artifact.Path)
	}
	digest := strings.ToLower(strings.TrimSpace(artifact.SHA256))
	if len(digest) != 64 {
		return fmt.Errorf("sha256 %q is not a digest", artifact.SHA256)
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("sha256 %q is not a digest", artifact.SHA256)
	}
	if artifact.Size <= 0 || artifact.Size > MaxBinaryBytes {
		return fmt.Errorf("size %d is outside 1..%d", artifact.Size, MaxBinaryBytes)
	}
	return nil
}

// decodeNotes reads the aligned notes shape reltitle publishes from v0.0.4 on
// ({"zh-CN": "<markdown>", "en": "<markdown>"}). Releases published before that
// contract carry a different shape; they are read as no notes rather than as an
// error, because a manifest older than the contract still describes an
// installable build.
func decodeNotes(raw json.RawMessage) (ChannelNotes, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return ChannelNotes{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ChannelNotes{}, nil
	}
	return ChannelNotes{ZhCN: noteText(fields["zh-CN"]), EN: noteText(fields["en"])}, nil
}

// noteText keeps a string note and drops anything else, so a shape this build
// does not know costs the reader a note and nothing more.
func noteText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

// ArtifactFor returns this platform's artifact, or an error naming the gap.
func (c *Channel) ArtifactFor(platform string) (ChannelArtifact, error) {
	artifact, ok := c.Artifacts[platform]
	if !ok {
		return ChannelArtifact{}, fmt.Errorf("%w: the channel publishes no %s artifact", ErrChannel, platform)
	}
	return artifact, nil
}

// ArtifactURL is where one artifact's bytes live: the manifest's sibling
// directory.
func (c *Channel) ArtifactURL(lane string, artifact ChannelArtifact) (string, error) {
	branch, err := channelBranch(lane)
	if err != nil {
		return "", err
	}
	return channelRootOverride + branch + "/lines/" + c.Line + "/" + artifact.Path, nil
}

func httpClientOf(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: channelTimeout}
}

// InstalledBinary describes the file that arrived through the channel.
func writeInstalled(configRoot string, installed Installed) error {
	payload, err := json.MarshalIndent(installed, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	path := MetadataPath(configRoot)
	temp := path + ".tmp"
	if err := os.WriteFile(temp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

// downloadToTemp streams one artifact into a temporary file beside the target
// and proves size and digest before anything replaces the live binary.
func downloadToTemp(ctx context.Context, client *http.Client, url string, expected ChannelArtifact, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, channelTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrChannel, err)
	}
	response, err := httpClientOf(client).Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: download %s: %v", ErrChannel, url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: %s answered %s", ErrChannel, url, response.Status)
	}
	temp, err := os.CreateTemp(dir, ".reltitle-download-*")
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrChannel, err)
	}
	name := temp.Name()
	cleanup := func() {
		temp.Close()
		os.Remove(name)
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(response.Body, expected.Size+1))
	if err != nil {
		cleanup()
		return "", fmt.Errorf("%w: download %s: %v", ErrChannel, url, err)
	}
	if written > expected.Size {
		cleanup()
		return "", fmt.Errorf("%w: artifact is larger than the manifest's %d bytes", ErrChannel, expected.Size)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("%w: %v", ErrChannel, err)
	}
	if err := temp.Close(); err != nil {
		os.Remove(name)
		return "", fmt.Errorf("%w: %v", ErrChannel, err)
	}
	if written != expected.Size {
		_ = os.Remove(name)
		return "", fmt.Errorf("%w: artifact is %d bytes, the manifest says %d", ErrChannel, written, expected.Size)
	}
	if sum := hex.EncodeToString(hasher.Sum(nil)); sum != expected.SHA256 {
		_ = os.Remove(name)
		return "", fmt.Errorf("%w: artifact digest %s does not match the manifest's %s", ErrChannel, sum, expected.SHA256)
	}
	return name, nil
}

// replaceBinary moves a verified file onto the live path. The rename is
// atomic within the directory, so a reader sees either the old file or the
// new one, never a partial write.
func replaceBinary(temp, target string) error {
	if err := os.Chmod(temp, 0o755); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("%w: %v", ErrChannel, err)
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("%w: %v", ErrChannel, err)
	}
	return syncDir(filepath.Dir(target))
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer handle.Close()
	_ = handle.Sync()
	return nil
}
