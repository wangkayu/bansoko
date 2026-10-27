// Package competition holds the shared vocabulary of the subscription
// competition: the one admission owner of a subscription instance.
//
// Release evaluation stops at the plan: a release that passes the seed
// filter, the torrent, the file rules and the batch plan is recorded as
// awaiting_competition. The instance's single subscription_competition work
// item then decides, under the subscription row lock, which waiting release
// takes which episode — against the instance's own managed files and running
// downloads, never another instance's — and runs the admission of every
// release it decided. The replacement fallback (the best release a settled
// acquisition kept out) is the same owner.
//
// Every Go producer enqueues through EnqueueTx: the subscription row is locked
// before the work item is created or merged (the producer/destructive-delete
// ordering of the work item runtime), so a producer racing an unsubscribe is
// either visible to its cleanup or a no-op. The settle trigger of migration 081
// (enqueue_subscription_competition) is the SQL twin of EnqueueTx without the
// lock: it reads the subscription row unlocked (active state and version)
// while its transaction holds the settled acquisition row. Both merge through
// subscription_competition_merge, which never expires the lease of a running
// round (the round re-decides under the subscription lock instead).
package competition

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/batchplan"
	"bansoko/internal/workitems"
)

// VerdictAwaiting is the release verdict of a release waiting for its
// instance's competition. It never carries a download task.
const VerdictAwaiting = "awaiting_competition"

// MaxAttempts is the competition's claim budget; it matches the worker policy
// of the kind and the SQL twin.
const MaxAttempts = 3

// Trigger reasons recorded in the competition input snapshot.
const (
	// TriggerReleaseEvaluated: a release evaluation of the instance finished
	// (it waits for the competition, or the instance still has a waiting
	// release whose wait the evaluation may have ended).
	TriggerReleaseEvaluated = "release_evaluated"
	// TriggerSourceRefreshed: a refresh of one of the instance's sources wrote
	// its runtime (a first refresh may end the first-round wait).
	TriggerSourceRefreshed = "source_refreshed"
	// TriggerAcquisitionSettled: an instance acquisition left running (the
	// SQL twin in migration 081 writes it).
	TriggerAcquisitionSettled = "acquisition_settled"
	// TriggerSubscriptionResumed and TriggerScanCompleted: the resume and the
	// completed-scan comparison moments.
	TriggerSubscriptionResumed = "subscription_resumed"
	TriggerScanCompleted       = "generation_completed"
	// TriggerSitePolicy: the site-level subgroup policy changed and the
	// instance's selection was recomputed; only waiting releases are re-ranked.
	TriggerSitePolicy = "site_subgroup_policy"
	// TriggerFollowUp: a decided release did not start a download, so the
	// next candidate of its episodes gets its turn.
	TriggerFollowUp = "competition_follow_up"
	// TriggerRoundMoved: the round's inputs moved before its commit (or its
	// commit lost a lock race); a fresh round runs at once.
	TriggerRoundMoved = "competition_round_moved"
	// TriggerRoundRetry: a decided release could not finish its decided stage
	// (a downloader lookup, a naming time limit); the round runs again later.
	TriggerRoundRetry = "competition_round_retry"
	// TriggerGateWait: a round found its gate closed (a live evaluation, a
	// first refresh); it looks again after a delay.
	TriggerGateWait = "competition_gate_wait"
	// TriggerRoundCarried: an interrupted round, or one that failed on its
	// last attempt, carries over to a fresh item.
	TriggerRoundCarried = "competition_round_carried"
)

// IdempotencyKey is the stable key of an instance's competition item.
func IdempotencyKey(managedAnimeID string) string {
	return workitems.KindSubscriptionCompetition + ":" + managedAnimeID
}

// Decision is the competition's conclusion for one release: the per-key
// deduplication verdicts its admission applies instead of the read-only
// coverage verdicts. Admitted is true when at least one key is open or
// replace, or when the release has no keyed video at all. Awaiting tells a
// waiting release (every waiting release gets a decision, admitted or not)
// from a fallback candidate (decided only when admitted). Exhaustive marks a
// verdict table that names every key the competition compared (every mode
// but all): a key missing from it is not open but claimed (fail closed). In
// all mode the table is empty and every key is open.
type Decision struct {
	ReleaseID  string                       `json:"release_id"`
	Version    int64                        `json:"version"`
	Awaiting   bool                         `json:"awaiting"`
	Admitted   bool                         `json:"admitted"`
	Exhaustive bool                         `json:"exhaustive"`
	Verdicts   map[string]batchplan.Verdict `json:"verdicts"`
}

// VerdictFor is the deduplication verdict of one key of the decided release.
func (d Decision) VerdictFor(key string) batchplan.Verdict {
	if verdict, ok := d.Verdicts[key]; ok {
		return verdict
	}
	if d.Exhaustive {
		return batchplan.VerdictInFlight
	}
	return batchplan.VerdictOpen
}

// Round is one decision round of an instance. Reason names why it decided
// nothing (no_candidate, subscription_inactive, ...).
type Round struct {
	Decisions []Decision
	Reason    string
}

// Equal reports whether two rounds decide the same releases at the same
// versions with the same verdicts: the commit re-decides under the
// subscription lock and refuses a round that moved since the unlocked one.
func (r Round) Equal(other Round) bool {
	if len(r.Decisions) != len(other.Decisions) {
		return false
	}
	for index, decision := range r.Decisions {
		next := other.Decisions[index]
		if decision.ReleaseID != next.ReleaseID || decision.Version != next.Version || decision.Awaiting != next.Awaiting || decision.Admitted != next.Admitted || decision.Exhaustive != next.Exhaustive || len(decision.Verdicts) != len(next.Verdicts) {
			return false
		}
		for key, verdict := range decision.Verdicts {
			if next.Verdicts[key] != verdict {
				return false
			}
		}
	}
	return true
}

// EnqueueOption narrows when EnqueueTx creates or merges the item.
type EnqueueOption int

const (
	// Always enqueues for an active subscription.
	Always EnqueueOption = iota
	// WhenAwaiting enqueues only while the instance has a waiting release.
	WhenAwaiting
)

// SourceInScopeSQL is the predicate "the source aliased src is in the
// subscription's scope": a custom source always is, a provider group source
// only while its group is available and selected. The competition's
// candidates, its gate and AwaitingSQL read the same scope.
const SourceInScopeSQL = `(src.source_kind <> 'provider_group' OR EXISTS (
    SELECT 1 FROM provider_group_selections selection
    WHERE selection.subscription_id = src.subscription_id AND selection.provider = src.provider
      AND selection.group_ref = src.provider_group_ref AND selection.available AND selection.effective_selected))`

// AwaitingSQL is the predicate "the instance (managed anime $1) has an
// in-scope release waiting for its competition".
const AwaitingSQL = `EXISTS (
    SELECT 1 FROM releases r
    JOIN rss_sources src ON src.id = r.rss_source_id
    JOIN subscriptions sub ON sub.id = src.subscription_id
    WHERE sub.managed_anime_id = $1 AND r.verdict = 'awaiting_competition' AND r.download_task_id IS NULL
      AND ` + SourceInScopeSQL + `)`

// EnqueueTx creates or merges the instance's competition item inside the
// caller's transaction. It locks the subscription row first; a missing or
// non-active subscription (or a retired instance) enqueues nothing.
func EnqueueTx(ctx context.Context, tx *sql.Tx, managedAnimeID, trigger string, option EnqueueOption) (bool, error) {
	if tx == nil || strings.TrimSpace(managedAnimeID) == "" {
		return false, errors.New("subscription competition enqueue is not wired")
	}
	var (
		state   string
		version int64
	)
	err := tx.QueryRowContext(ctx, `
SELECT s.state, s.version
FROM subscriptions s
JOIN managed_anime m ON m.id = s.managed_anime_id
WHERE s.managed_anime_id = $1 AND m.lifecycle_state = 'active'
FOR UPDATE OF s`, managedAnimeID).Scan(&state, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock subscription for competition: %w", err)
	}
	if state != "active" {
		return false, nil
	}
	if option == WhenAwaiting {
		var awaiting bool
		if err := tx.QueryRowContext(ctx, `SELECT `+AwaitingSQL, managedAnimeID).Scan(&awaiting); err != nil {
			return false, fmt.Errorf("read waiting releases: %w", err)
		}
		if !awaiting {
			return false, nil
		}
	}
	if err := MergeTx(ctx, tx, managedAnimeID, version, map[string]any{"trigger": trigger}, 0); err != nil {
		return false, err
	}
	return true, nil
}

// MergeTx creates or merges the instance's competition item through
// subscription_competition_merge. The caller holds the subscription row lock.
// A merge into an available item moves it earlier (never later); a merge into
// a running round leaves its lease alone.
func MergeTx(ctx context.Context, tx *sql.Tx, managedAnimeID string, subscriptionVersion int64, input map[string]any, delay time.Duration) error {
	snapshot, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if delay < 0 {
		delay = 0
	}
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT subscription_competition_merge($1, $2::jsonb, $3, $4)`, managedAnimeID, string(snapshot), subscriptionVersion, delay.Seconds()).Scan(&id); err != nil {
		return fmt.Errorf("enqueue subscription competition: %w", err)
	}
	return nil
}
