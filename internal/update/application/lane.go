package application

import (
	"fmt"
	"strings"
)

// Update lanes. A deployment does not name an update source anymore: it names
// the lane it belongs to, and the source is this project's own published
// channel. The two lanes are produced by CI from one repository — `main` pushes
// publish the test channel, `v*` tags publish the stable one — so a deployment
// that wants main builds says so with one word instead of carrying three URLs.
const (
	// EnvLane selects the lane: stable (default) or test.
	EnvLane = "BANSOKO_UPDATE_LANE"
	// EnvChannelBase replaces the repository root for development and test
	// fixtures only: it lets a local server play the channel so the update
	// chain can be exercised end to end without publishing. Production refuses
	// it outright, so a shipped deployment still reads this project's own
	// channel and nothing else.
	EnvChannelBase = "BANSOKO_UPDATE_CHANNEL_BASE"
	// LaneStable receives tagged releases.
	LaneStable = "stable"
	// LaneTest receives builds of main.
	LaneTest = "test"
)

const (
	// channelRepositoryRoot is the raw root every lane lives under. It is a
	// code constant on purpose: the update client only ever reads this
	// project's own channel branches, so a misconfigured deployment cannot
	// point it at someone else's code.
	channelRepositoryRoot = "https://raw.githubusercontent.com/wangkayu/bansoko/"
	stableChannelBranch   = "update-channel"
	testChannelBranch     = "update-channel-test"
)

// laneBranch resolves the channel branch of one lane.
func laneBranch(lane string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(lane)) {
	case "", LaneStable:
		return stableChannelBranch, nil
	case LaneTest:
		return testChannelBranch, nil
	default:
		return "", fmt.Errorf("%w: %s must be %q or %q (got %q)", ErrUpdateSourceURL, EnvLane, LaneStable, LaneTest, lane)
	}
}

// LaneSource resolves one lane's channel document URL and package root. An
// empty lane means stable; anything else must be a declared lane. base
// overrides the repository root (development and test fixtures only; the
// caller is responsible for refusing it in production).
func LaneSource(lane, base string) (documentURL, packageRoot string, err error) {
	branch, err := laneBranch(lane)
	if err != nil {
		return "", "", err
	}
	root := strings.TrimSpace(base)
	if root == "" {
		root = channelRepositoryRoot
	}
	if !strings.HasSuffix(root, "/") {
		root += "/"
	}
	packageRoot = root + branch + "/"
	return packageRoot + "channel-v1.json", packageRoot, nil
}

// retiredSourceEnv names the deployment inputs the lane replaced. A deployment
// that still sets one of them is refused at startup rather than left looking
// configured while the client silently reads this project's own channel.
var retiredSourceEnv = []string{
	"BANSOKO_UPDATE_MANIFEST_URL",
	"BANSOKO_UPDATE_CHANNEL_URL",
	"BANSOKO_UPDATE_PACKAGE_ROOT",
	"BANSOKO_UPDATE_PUBLIC_KEY_PATH",
}

// CheckRetiredSourceEnv refuses any retired update environment still present in
// the deployment. getenv is injected so the check is the same everywhere it
// runs (server startup and tests) without reading process state twice.
func CheckRetiredSourceEnv(getenv func(string) string) error {
	for _, name := range retiredSourceEnv {
		if strings.TrimSpace(getenv(name)) != "" {
			return fmt.Errorf("%w: %s is retired; set %s=%s or %s=%s instead",
				ErrUpdateSourceURL, name, EnvLane, LaneStable, EnvLane, LaneTest)
		}
	}
	return nil
}

// NormalizeLane returns the lane a deployment is on, defaulting to stable.
func NormalizeLane(lane string) (string, error) {
	branch, err := laneBranch(lane)
	if err != nil {
		return "", err
	}
	if branch == testChannelBranch {
		return LaneTest, nil
	}
	return LaneStable, nil
}
