package downloadcompletion

import (
	"context"
	"errors"
	"strings"
	"time"

	"bansoko/internal/batchplan"
)

// Result codes written to the completion row. The success codes are what the
// acquisition detail shows; the failure codes are typed reasons.
const (
	ResultSeedKept      = "seed_kept"
	ResultPaused        = "paused"
	ResultRemoved       = "removed"
	ResultRemovedData   = "removed_with_data"
	ResultTaskAbsent    = "task_absent"
	ResultTorrentShared = "skipped_torrent_shared"

	FailureTaskUnknown      = "qb_task_unknown"
	FailureQBUnavailable    = "qb_unavailable"
	FailureVerifyFailed     = "qb_verify_failed"
	FailureTaskChanged      = "qb_task_changed"
	FailureStoreUnavailable = "completion_store_unavailable"
	FailureInvalidAction    = "completion_action_invalid"
)

// Action names (equal to the download_completion section values).
const (
	ActionSeed           = "seed"
	ActionPause          = "pause"
	ActionRemove         = "remove"
	ActionRemoveWithData = "remove_with_data"
)

// Completion is the frozen content of one completion row the executor acts on.
type Completion struct {
	ID              string
	AcquisitionID   string
	Action          string
	InfoHash        string
	RemoteRoot      string
	ApplicationRoot string
	// SeedLimits are the frozen share limits of the seed action; nil seeds
	// without a limit and takes no action.
	SeedLimits *batchplan.SeedLimits
}

// QB is the qBittorrent surface the action uses. Every call addresses the task
// by infohash; nothing is addressed by path, tag or name.
type QB interface {
	// Torrent returns the task and its complete file list, or found=false when
	// qBittorrent no longer has it.
	Torrent(ctx context.Context, hash string) (Torrent, bool, error)
	// Others lists every other task (all but hash). A task whose content path
	// equals its save path carries its complete file list; the list failing to
	// read is an error, never an empty list.
	Others(ctx context.Context, hash string) ([]Torrent, error)
	Pause(ctx context.Context, hash string) error
	Delete(ctx context.Context, hash string, deleteFiles bool) error
	// WebAPIVersion reads app/webapiVersion.
	WebAPIVersion(ctx context.Context) (string, error)
	// SetShareLimits writes the task's share limits.
	SetShareLimits(ctx context.Context, hash string, limits ShareLimitsRequest) error
}

// Library answers the filesystem-side facts the data-deleting action checks.
type Library interface {
	// EachLibraryPath yields every path a library file may live at.
	EachLibraryPath(ctx context.Context, visit func(path string) error) error
}

// Sharing tells whether another running acquisition still uses the task.
type Sharing interface {
	SharedByOther(ctx context.Context, hash, acquisitionID string) (bool, error)
}

// Outcome is the executor's decision: a settled result (success or a terminal
// typed failure) or a retryable failure that the WorkItem budget retries.
type Outcome struct {
	Code string
	// Success is true for a settled success result.
	Success bool
	// Retryable marks a transient failure; the WorkItem retries and settles
	// the row failed only when its attempt budget is spent.
	Retryable bool
	Detail    string
}

// Executor runs one completion action. It performs no filesystem write and no
// filesystem delete; qBittorrent does the deleting, on its own task only.
type Executor struct {
	QB      QB
	Library Library
	Sharing Sharing
	// VerifyWindow bounds how long a result is re-read before it counts as not
	// applied (qBittorrent applies stop and delete asynchronously). Zero means
	// five seconds; a negative value reads once.
	VerifyWindow time.Duration
	// Sleep waits between two reads; nil sleeps for real.
	Sleep func(ctx context.Context, d time.Duration)
}

const verifyInterval = 250 * time.Millisecond

// settled re-reads the task until done reports true or the window is over. It
// returns the last read; a read error ends it at once.
func (e Executor) settled(ctx context.Context, hash string, done func(t Torrent, found bool) bool) (Torrent, bool, error) {
	window := e.VerifyWindow
	if window == 0 {
		window = 5 * time.Second
	}
	sleep := e.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		}
	}
	for waited := time.Duration(0); ; waited += verifyInterval {
		torrent, found, err := e.QB.Torrent(ctx, hash)
		if err != nil || done(torrent, found) || window < 0 || waited >= window || ctx.Err() != nil {
			return torrent, found, err
		}
		sleep(ctx, verifyInterval)
	}
}

// Run executes the action of one completion. It is idempotent: a replay after
// a lost lease sees the task already stopped or gone and settles the same
// result.
func (e Executor) Run(ctx context.Context, c Completion) Outcome {
	switch c.Action {
	case ActionPause, ActionRemove, ActionRemoveWithData:
	case ActionSeed:
		if c.SeedLimits == nil {
			return Outcome{Code: ResultSeedKept, Success: true}
		}
	default:
		return failure(FailureInvalidAction, "the frozen completion action is not known")
	}
	hash := strings.ToLower(strings.TrimSpace(c.InfoHash))
	if hash == "" {
		return failure(FailureTaskUnknown, "the download task has no recorded torrent identity")
	}
	if e.QB == nil || e.Sharing == nil || (deletesData(c) && e.Library == nil) {
		return retryable(FailureQBUnavailable, "the completion action is not wired")
	}

	shared, err := e.Sharing.SharedByOther(ctx, hash, c.AcquisitionID)
	if err != nil {
		return retryable(FailureStoreUnavailable, err.Error())
	}
	if shared {
		// Another running acquisition still depends on this task; it acts when
		// it ends, under its own frozen action.
		return Outcome{Code: ResultTorrentShared, Success: true}
	}

	torrent, found, err := e.QB.Torrent(ctx, hash)
	if err != nil {
		return retryable(FailureQBUnavailable, err.Error())
	}
	if !found {
		return Outcome{Code: ResultTaskAbsent, Success: true}
	}
	if !strings.EqualFold(strings.TrimSpace(torrent.Hash), hash) {
		return failure(CodeQBTaskMismatch, "the qBittorrent task is not the recorded one")
	}

	switch c.Action {
	case ActionSeed:
		return e.seedLimits(ctx, c, hash, torrent)
	case ActionPause:
		if err := e.QB.Pause(ctx, hash); err != nil {
			return retryable(FailureQBUnavailable, err.Error())
		}
		after, found, err := e.settled(ctx, hash, func(t Torrent, found bool) bool { return !found || notRunning(t.State) })
		if err != nil {
			return retryable(FailureQBUnavailable, err.Error())
		}
		if found && !notRunning(after.State) {
			return retryable(FailureVerifyFailed, "the task is still running after pause (state "+after.State+")")
		}
		return Outcome{Code: ResultPaused, Success: true}
	case ActionRemove:
		return e.remove(ctx, hash, false, ResultRemoved)
	default:
		return e.removeWithData(ctx, c, hash, torrent)
	}
}

// deletesData reports whether the completion may make qBittorrent delete the
// task's files: the remove_with_data action, or seed limits whose at-limit
// action is remove_with_data.
func deletesData(c Completion) bool {
	return c.Action == ActionRemoveWithData || c.Action == ActionSeed && c.SeedLimits != nil && c.SeedLimits.AtLimit == batchplan.SeedLimitRemoveWithData
}

// checkRemoveWithData runs CheckRemoveWithData for the task; refused=true
// carries the outcome (a typed refusal, or a retryable read failure).
func (e Executor) checkRemoveWithData(ctx context.Context, c Completion, hash string, torrent Torrent) (Outcome, bool) {
	check := SafetyInput{
		Hash: hash, Torrent: torrent, RemoteRoot: c.RemoteRoot, ApplicationRoot: c.ApplicationRoot,
		OtherTasks:      func() ([]Torrent, error) { return e.QB.Others(ctx, hash) },
		EachLibraryPath: func(visit func(string) error) error { return e.Library.EachLibraryPath(ctx, visit) },
	}
	if err := CheckRemoveWithData(check); err != nil {
		var safety *SafetyError
		if errors.As(err, &safety) {
			return failure(safety.Code, safety.Detail), true
		}
		return retryable(FailureStoreUnavailable, err.Error()), true
	}
	return Outcome{}, false
}

func (e Executor) removeWithData(ctx context.Context, c Completion, hash string, torrent Torrent) Outcome {
	if outcome, refused := e.checkRemoveWithData(ctx, c, hash, torrent); refused {
		return outcome
	}
	// The task may have changed between the check and the delete (a location
	// move, a file list change). The delete only goes ahead against exactly
	// the task that was checked.
	again, found, err := e.QB.Torrent(ctx, hash)
	if err != nil {
		return retryable(FailureQBUnavailable, err.Error())
	}
	if !found {
		return Outcome{Code: ResultTaskAbsent, Success: true}
	}
	if !sameTorrent(torrent, again) {
		return retryable(FailureTaskChanged, "the task changed while it was being checked")
	}
	return e.remove(ctx, hash, true, ResultRemovedData)
}

func (e Executor) remove(ctx context.Context, hash string, withData bool, result string) Outcome {
	if err := e.QB.Delete(ctx, hash, withData); err != nil {
		return retryable(FailureQBUnavailable, err.Error())
	}
	_, found, err := e.settled(ctx, hash, func(_ Torrent, found bool) bool { return !found })
	if err != nil {
		return retryable(FailureQBUnavailable, err.Error())
	}
	if found {
		return retryable(FailureVerifyFailed, "the task is still in qBittorrent after delete")
	}
	return Outcome{Code: result, Success: true}
}

func sameTorrent(a, b Torrent) bool {
	if !strings.EqualFold(a.Hash, b.Hash) || a.SavePath != b.SavePath || a.ContentPath != b.ContentPath || len(a.FileNames) != len(b.FileNames) {
		return false
	}
	for i := range a.FileNames {
		if a.FileNames[i] != b.FileNames[i] {
			return false
		}
	}
	return true
}

// notRunning reports a qBittorrent state in which the task transfers nothing:
// paused/stopped (4.x and 5.x spellings), or a state qBittorrent forces
// itself (missing files, error).
func notRunning(state string) bool {
	state = strings.ToLower(strings.TrimSpace(state))
	return strings.HasPrefix(state, "paused") || strings.HasPrefix(state, "stopped") || state == "missingfiles" || state == "error"
}

func failure(code, detail string) Outcome {
	return Outcome{Code: code, Detail: detail}
}

func retryable(code, detail string) Outcome {
	return Outcome{Code: code, Detail: detail, Retryable: true}
}
