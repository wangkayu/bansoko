package downloadcompletion

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"bansoko/internal/batchplan"
)

// Seed-limit result codes. The row succeeds once the limits are written; from
// then on qBittorrent acts when a limit is reached and the product has let go.
const (
	// ResultSeedLimitsSet: the limits and the task's own limit action are set.
	ResultSeedLimitsSet = "seed_limits_set"
	// ResultSeedLimitsSetGlobalAction: the limits are set, but this
	// qBittorrent has no per-task limit action (WebAPI older than 2.12.0), so
	// qBittorrent's global limit action applies when a limit is reached.
	ResultSeedLimitsSetGlobalAction = "seed_limits_set_global_action"
)

// ShareLimitsRequest is one torrents/setShareLimits write. A limit of -1 means
// no limit, -2 the global limit. Action and Mode are empty when the
// qBittorrent WebAPI does not know them.
type ShareLimitsRequest struct {
	RatioLimit               float64
	SeedingTimeLimit         int64
	InactiveSeedingTimeLimit int64
	// Action is the task's own limit action (Stop, Remove, RemoveWithContent).
	Action string
	// Mode is how the limits combine (MatchAny).
	Mode string
}

// WebAPI versions that changed torrents/setShareLimits, from qBittorrent's
// WebAPI_Changelog.md:
//   - 2.12.0 (#22989): shareLimitAction is required (Default, Stop, Remove,
//     RemoveWithContent, EnableSuperSeeding); older versions have no
//     per-task limit action and apply the global one.
//   - 2.15.3 (#24043): shareLimitsMode is required (Default, MatchAny,
//     MatchAll).
var (
	webAPIShareLimitAction = [3]int{2, 12, 0}
	webAPIShareLimitsMode  = [3]int{2, 15, 3}
)

// shareLimitSupport reports which optional setShareLimits parameters a
// qBittorrent WebAPI version takes. A version that does not parse is treated
// as the oldest (neither), the conservative reading.
func shareLimitSupport(version string) (action, mode bool) {
	parsed, ok := parseWebAPIVersion(version)
	if !ok {
		return false, false
	}
	return versionAtLeast(parsed, webAPIShareLimitAction), versionAtLeast(parsed, webAPIShareLimitsMode)
}

func parseWebAPIVersion(version string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) < 2 || len(parts) > 3 {
		return out, false
	}
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return out, false
		}
		out[i] = value
	}
	return out, true
}

func versionAtLeast(have, want [3]int) bool {
	for i := range have {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return true
}

// qbLimitAction is the qBittorrent spelling of an at_limit value.
func qbLimitAction(atLimit string) (string, bool) {
	switch atLimit {
	case batchplan.SeedLimitStop:
		return "Stop", true
	case batchplan.SeedLimitRemove:
		return "Remove", true
	case batchplan.SeedLimitRemoveWithData:
		return "RemoveWithContent", true
	}
	return "", false
}

// shareLimitsRequest builds the write for one set of limits under a WebAPI
// version. An unused limit is -1 (no limit); the inactive seeding limit is
// always sent as -2 (global), because qBittorrent 4.6 refuses the request
// without it. It reports whether the task's own limit action was included.
func shareLimitsRequest(limits batchplan.SeedLimits, version string) (ShareLimitsRequest, bool, error) {
	if err := limits.Validate(); err != nil {
		return ShareLimitsRequest{}, false, err
	}
	action, ok := qbLimitAction(limits.AtLimit)
	if !ok {
		return ShareLimitsRequest{}, false, errors.New("unknown at_limit")
	}
	request := ShareLimitsRequest{RatioLimit: -1, SeedingTimeLimit: -1, InactiveSeedingTimeLimit: -2}
	if limits.Ratio != nil {
		request.RatioLimit = *limits.Ratio
	}
	if limits.SeedingTimeMinutes != nil {
		request.SeedingTimeLimit = *limits.SeedingTimeMinutes
	}
	withAction, withMode := shareLimitSupport(version)
	if withAction {
		request.Action = action
	}
	if withMode {
		// Either limit ends seeding, as on every older qBittorrent.
		request.Mode = "MatchAny"
	}
	return request, withAction, nil
}

// seedLimits writes the frozen limits on the task and lets go. remove_with_data
// at the limit is checked now, exactly like the remove_with_data action: a
// refusal fails the row and nothing is set in qBittorrent.
func (e Executor) seedLimits(ctx context.Context, c Completion, hash string, torrent Torrent) Outcome {
	limits := *c.SeedLimits
	if limits.Validate() != nil {
		return failure(FailureInvalidAction, "the frozen seed limits are not valid")
	}
	if limits.AtLimit == batchplan.SeedLimitRemoveWithData {
		if outcome, refused := e.checkRemoveWithData(ctx, c, hash, torrent); refused {
			return outcome
		}
	}
	version, err := e.QB.WebAPIVersion(ctx)
	if err != nil {
		return retryable(FailureQBUnavailable, err.Error())
	}
	request, withAction, err := shareLimitsRequest(limits, version)
	if err != nil {
		return failure(FailureInvalidAction, err.Error())
	}
	if err := e.QB.SetShareLimits(ctx, hash, request); err != nil {
		return retryable(FailureQBUnavailable, err.Error())
	}
	if withAction {
		return Outcome{Code: ResultSeedLimitsSet, Success: true}
	}
	return Outcome{Code: ResultSeedLimitsSetGlobalAction, Success: true}
}
