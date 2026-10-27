package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	animedomain "bansoko/internal/anime/domain"
	"bansoko/internal/batchplan"
	"bansoko/internal/events"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/runtime/worker"
	"bansoko/internal/subscription/competition"
	"bansoko/internal/workitems"

	"github.com/jackc/pgx/v5/pgconn"
)

// CompetitionDecider decides one competition round of a subscription
// instance inside the caller's transaction (filemaintenance
// ReplacementDecisionService.CompetitionDecisions).
type CompetitionDecider interface {
	CompetitionDecisions(ctx context.Context, tx *sql.Tx, managedAnimeID string) (competition.Round, error)
}

// SetSubscriptionCompetition wires the competition owner. From then on an
// active subscription's release evaluation stops at the plan
// (awaiting_competition) and the competition decides every admission.
func (s *Service) SetSubscriptionCompetition(decider CompetitionDecider) {
	if s != nil {
		s.competition = decider
	}
}

// Competition gate reasons: the round does not run yet.
const (
	competitionWaitingEvaluations  = "waiting_evaluations"
	competitionWaitingFirstRefresh = "waiting_first_refresh"
	competitionInactive            = "subscription_inactive"
)

// errCompetitionActivated: a release evaluated for the direct path (a stopped
// subscription's explicit retry) found its subscription active under the
// owner lock; the release must wait for the competition instead.
var errCompetitionActivated = errors.New("the subscription became active: the release waits for the competition")

// awaitsCompetition reports whether a downloadable release stops at the plan:
// the competition is wired, the subscription is active and this is not the
// competition's own decided stage. A stopped subscription's explicit retry
// keeps the direct path. The owner writer repeats the decision on the locked
// subscription state (competitionOwnsAdmission).
func (s *Service) awaitsCompetition(snapshot releaseSnapshot) bool {
	return s.competitionOwnsAdmission(snapshot, snapshot.subscriptionState)
}

// competitionOwnsAdmission is awaitsCompetition for a given subscription state.
func (s *Service) competitionOwnsAdmission(snapshot releaseSnapshot, subscriptionState string) bool {
	return s != nil && s.competition != nil && snapshot.competitionDecision == nil && subscriptionState == "active"
}

// markAwaitingCompetition records a downloadable release as waiting for its
// instance's competition.
func markAwaitingCompetition(result *animedomain.PreviewReleaseResult) {
	result.Verdict = animedomain.ReleaseVerdict(competition.VerdictAwaiting)
	result.ReasonCode = competition.VerdictAwaiting
	result.ReasonDetail = nil
}

// decidedVerdicts is the deduplication table of the decided stage over the
// keys the evaluation planned.
func decidedVerdicts(decision competition.Decision, keys []string) map[string]batchplan.Verdict {
	verdicts := make(map[string]batchplan.Verdict, len(keys))
	for _, key := range keys {
		verdicts[key] = decision.VerdictFor(key)
	}
	return verdicts
}

// withCompetitionFollowUp makes the end of a release evaluation enqueue the
// instance's competition when a release waits for it: the evaluation just
// written may wait itself, and its end may open the wait of the others. The
// owner writer locks the subscription before the release (the competition
// commit's order); the enqueue runs after the evaluation item left the live
// set, so the competition never sees this evaluation as unfinished.
func (s *Service) withCompetitionFollowUp(outcome worker.Outcome, managedAnimeID string) worker.Outcome {
	if s == nil || s.competition == nil || outcome.Kind != worker.OutcomeFinish || strings.TrimSpace(managedAnimeID) == "" {
		return outcome
	}
	if inner := outcome.WriteOwner; inner != nil {
		outcome.WriteOwner = func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
			tx := scope.SQLTx()
			if tx == nil {
				return 0, errors.New("release owner writer transaction is nil")
			}
			if _, err := tx.ExecContext(ctx, `SELECT 1 FROM subscriptions WHERE managed_anime_id = $1 FOR UPDATE`, managedAnimeID); err != nil {
				return 0, fmt.Errorf("lock subscription for evaluation: %w", err)
			}
			return inner(ctx, scope, result)
		}
	}
	outcome.PostTransition = withCompetitionEnqueue(outcome.PostTransition, managedAnimeID, competition.TriggerReleaseEvaluated)
	return outcome
}

// withCompetitionEnqueue chains a post-transition hook that enqueues the
// instance's competition while one of its releases waits.
func withCompetitionEnqueue(previous func(context.Context, txscope.Tx) error, managedAnimeID, trigger string) func(context.Context, txscope.Tx) error {
	return func(ctx context.Context, scope txscope.Tx) error {
		if previous != nil {
			if err := previous(ctx, scope); err != nil {
				return err
			}
		}
		tx := scope.SQLTx()
		if tx == nil {
			return errors.New("competition enqueue transaction is nil")
		}
		_, err := competition.EnqueueTx(ctx, tx, managedAnimeID, trigger, competition.WhenAwaiting)
		return err
	}
}

// withRefreshCompetition makes the end of a refresh enqueue the instance's
// competition when a release waits for it: a first refresh (or a refresh that
// ended without writing the runtime) may end the first-round wait. A refresh
// that writes its runtime (success, not modified, failure, whether the item
// finishes or retries) locks the subscription first and enqueues in the same
// write: the runtime now has last_refresh_at, so the still-live item holds
// nothing. A finished refresh with no runtime write (an ineligible source, a
// parser policy fault) enqueues after its item left the live set. When the
// write left the site's circuit open, the other instances waiting on a
// never-refreshed source of that site are enqueued after the commit (best
// effort; the circuit's end refreshes those sources anyway).
func (s *Service) withRefreshCompetition(ctx context.Context, outcome worker.Outcome, sourceID string) worker.Outcome {
	if s == nil || s.competition == nil || strings.TrimSpace(sourceID) == "" {
		return outcome
	}
	inner := outcome.WriteOwner
	if inner == nil {
		if outcome.Kind == worker.OutcomeFinish {
			if animeID := s.sourceManagedAnimeID(ctx, sourceID); animeID != "" {
				outcome.PostTransition = withCompetitionEnqueue(outcome.PostTransition, animeID, competition.TriggerSourceRefreshed)
			}
		}
		return outcome
	}
	var (
		animeID, provider string
		paused            bool
	)
	outcome.WriteOwner = func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("RSS refresh owner writer transaction is nil")
		}
		paused = false
		err := tx.QueryRowContext(ctx, `
SELECT sub.managed_anime_id, COALESCE(src.provider, '')
FROM rss_sources src JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE src.id = $1
FOR UPDATE OF sub`, sourceID).Scan(&animeID, &provider)
		if errors.Is(err, sql.ErrNoRows) {
			return inner(ctx, scope, result)
		}
		if err != nil {
			return 0, fmt.Errorf("lock subscription for refresh: %w", err)
		}
		version, err := inner(ctx, scope, result)
		if err != nil {
			return 0, err
		}
		if _, err := competition.EnqueueTx(ctx, tx, animeID, competition.TriggerSourceRefreshed, competition.WhenAwaiting); err != nil {
			return 0, err
		}
		if provider != "" {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT paused_until > CURRENT_TIMESTAMP FROM rss_provider_circuits WHERE provider = $1), FALSE)`, provider).Scan(&paused); err != nil {
				return 0, fmt.Errorf("read RSS provider circuit: %w", err)
			}
		}
		return version, nil
	}
	next := outcome.AfterCommit
	outcome.AfterCommit = func(ctx context.Context) {
		if next != nil {
			next(ctx)
		}
		if paused {
			s.enqueueCircuitWaiters(context.WithoutCancel(ctx), provider, animeID)
		}
	}
	return outcome
}

// sourceManagedAnimeID is the instance of a source ("" when it is gone).
func (s *Service) sourceManagedAnimeID(ctx context.Context, sourceID string) string {
	var id string
	if err := s.db.QueryRowContext(ctx, `
SELECT sub.managed_anime_id FROM rss_sources src JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE src.id = $1`, sourceID).Scan(&id); err != nil {
		return ""
	}
	return id
}

// enqueueCircuitWaiters enqueues the competition of every other active
// instance that waits for a release and has a never-refreshed source of the
// paused site: the open circuit counts as that source's first refresh.
func (s *Service) enqueueCircuitWaiters(ctx context.Context, provider, except string) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT sub.managed_anime_id
FROM rss_sources src
JOIN subscriptions sub ON sub.id = src.subscription_id
LEFT JOIN rss_source_runtime runtime ON runtime.rss_source_id = src.id
WHERE src.provider = $1 AND sub.state = 'active' AND sub.managed_anime_id <> $2
  AND sub.competition_first_round_at IS NULL
  AND runtime.last_refresh_at IS NULL`, provider, except)
	if err != nil {
		return
	}
	var animeIDs []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			animeIDs = append(animeIDs, id)
		}
	}
	_ = rows.Close()
	for _, id := range animeIDs {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return
		}
		if _, err := competition.EnqueueTx(ctx, tx, id, competition.TriggerSourceRefreshed, competition.WhenAwaiting); err != nil {
			_ = tx.Rollback()
			continue
		}
		_ = tx.Commit()
	}
}

// releaseManagedAnimeID is the instance of a release_evaluate claim ("" when
// the release no longer resolves to one).
func (s *Service) releaseManagedAnimeID(ctx context.Context, claim workitems.Claim) string {
	if id := strings.TrimSpace(claim.ManagedAnimeID); id != "" {
		return id
	}
	var id string
	if err := s.db.QueryRowContext(ctx, `
SELECT sub.managed_anime_id
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
JOIN subscriptions sub ON sub.id = src.subscription_id
WHERE r.id = $1`, claim.Owner.ID).Scan(&id); err != nil {
		return ""
	}
	return id
}

// competitionGate reads whether the instance's round may run (without locks
// in the handler, again under the subscription lock at the commit):
//
//   - every round waits until no release evaluation of the instance is live;
//   - only the instance's first round (no competition_first_round_at yet)
//     also waits for the first refresh of every in-scope source that still
//     has a live refresh item. A failed refresh writes last_refresh_at too,
//     a source whose site circuit is open counts as refreshed, and a source
//     with no runtime row or no live refresh item (a refresh that ended
//     without writing) holds nothing, so the wait always ends.
//
// A torrent fetch failure ends its evaluation as a failed verdict, so it never
// holds the round.
func competitionGate(ctx context.Context, q queryer, managedAnimeID string) (string, error) {
	var (
		state, lifecycle      string
		evaluating, unrefresh bool
	)
	err := q.QueryRowContext(ctx, `
SELECT sub.state, m.lifecycle_state,
       EXISTS (
           SELECT 1 FROM work_items wi
           JOIN releases r ON r.id = wi.owner_id
           JOIN rss_sources src ON src.id = r.rss_source_id
           WHERE wi.owner_type = 'release' AND wi.kind = 'release_evaluate' AND src.subscription_id = sub.id),
       sub.competition_first_round_at IS NULL AND EXISTS (
           SELECT 1 FROM rss_sources src
           JOIN work_items refresh ON refresh.owner_type = 'rss_source_runtime' AND refresh.owner_id = src.id AND refresh.kind = 'rss_refresh'
           LEFT JOIN rss_source_runtime runtime ON runtime.rss_source_id = src.id
           LEFT JOIN rss_provider_circuits circuit ON circuit.provider = src.provider
           WHERE src.subscription_id = sub.id
             -- competition.SourceInScopeSQL, spelled out: the query reads work_items
             AND (src.source_kind <> 'provider_group' OR EXISTS (
                 SELECT 1 FROM provider_group_selections selection
                 WHERE selection.subscription_id = src.subscription_id AND selection.provider = src.provider
                   AND selection.group_ref = src.provider_group_ref AND selection.available AND selection.effective_selected))
             AND runtime.last_refresh_at IS NULL
             AND NOT COALESCE(circuit.paused_until > CURRENT_TIMESTAMP, FALSE))
FROM subscriptions sub
JOIN managed_anime m ON m.id = sub.managed_anime_id
WHERE sub.managed_anime_id = $1`, managedAnimeID).Scan(&state, &lifecycle, &evaluating, &unrefresh)
	if errors.Is(err, sql.ErrNoRows) {
		return competitionInactive, nil
	}
	if err != nil {
		return "", fmt.Errorf("read competition gate: %w", err)
	}
	switch {
	case state != "active" || lifecycle != "active":
		return competitionInactive, nil
	case evaluating:
		return competitionWaitingEvaluations, nil
	case unrefresh:
		return competitionWaitingFirstRefresh, nil
	}
	return "", nil
}

// CompetitionHandler is the subscription_competition work item handler.
func (s *Service) CompetitionHandler() worker.Handler {
	return competitionHandler{service: s}
}

type competitionHandler struct{ service *Service }

// decidedEvaluation is the decided-stage outcome of one release of a round.
type decidedEvaluation struct {
	decision competition.Decision
	outcome  worker.Outcome
}

// competitionRun is what the unlocked part of a round hands its commit.
type competitionRun struct {
	gate        string
	round       competition.Round
	evaluations []decidedEvaluation
	// moved: a decided release changed before its decided stage.
	moved bool
	// retryExcluded: a decided release could not finish its decided stage
	// now (a retry outcome or a failed owner write); it stays waiting and the
	// round runs again later.
	retryExcluded bool
	// excluded names every decided release left unwritten.
	excluded []string
}

// Handle runs one round of the instance's competition:
//
//  1. the gate (competitionGate) decides whether the round runs at all;
//  2. the round is decided in a read-only transaction;
//  3. every decided release runs the decided stage of the one release
//     evaluation outside any transaction (target probe, naming vars, qB). A
//     release whose decided stage fails is left out of the round: it is not
//     written, its episodes stay closed this round, and the others commit;
//  4. the commit (commitCompetition) locks the managed anime, the
//     subscription and then the claim (the producers' order), checks the gate
//     again, decides the round again and starts a fresh round when it moved,
//     else writes every release in the same transaction.
//
// Contention never spends the attempt budget: a round whose inputs moved, or
// whose commit lost a lock race, ends and leaves a fresh item that runs at
// once.
func (h competitionHandler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	s := h.service
	if s == nil || s.db == nil || s.workItems == nil || s.competition == nil {
		return worker.Outcome{}, errors.New("subscription competition is not fully wired")
	}
	if claim.Kind != workitems.KindSubscriptionCompetition || claim.Owner.Type != workitems.OwnerFileMaintenance || strings.TrimSpace(claim.Owner.ID) == "" {
		return worker.Outcome{}, fmt.Errorf("subscription competition handler received %s/%s", claim.Owner.Type, claim.Kind)
	}
	animeID := claim.Owner.ID
	run := competitionRun{}
	var err error
	if run.gate, err = competitionGate(ctx, s.db, animeID); err != nil {
		return worker.Outcome{}, err
	}
	if run.gate == "" {
		if run.round, err = s.decideCompetition(ctx, animeID); err != nil {
			return worker.Outcome{}, err
		}
		s.runDecidedStages(ctx, animeID, &run)
	}
	return s.commitCompetition(ctx, claim, run)
}

// runDecidedStages runs the decided stage of every release of the round.
func (s *Service) runDecidedStages(ctx context.Context, animeID string, run *competitionRun) {
	for _, decision := range run.round.Decisions {
		snapshot, err := s.loadReleaseSnapshot(ctx, decision.ReleaseID)
		if err != nil {
			run.excluded = append(run.excluded, decision.ReleaseID)
			continue
		}
		if snapshot.releaseVersion != decision.Version {
			run.moved = true
			return
		}
		decided := decision
		snapshot.competitionDecision = &decided
		releaseClaim := workitems.Claim{
			Owner: workitems.OwnerRef{Type: workitems.OwnerRelease, ID: decision.ReleaseID}, Kind: workitems.KindReleaseEvaluate,
			ExpectedOwnerVersion: decision.Version, ManagedAnimeID: animeID,
		}
		outcome, err := s.evaluateRelease(ctx, releaseClaim, snapshot)
		switch {
		case err != nil:
			run.excluded = append(run.excluded, decision.ReleaseID)
		case outcome.Kind == worker.OutcomeRetry:
			// The decided stage could not finish (naming vars interrupted, a
			// qB lookup failed): the release waits for a later round.
			run.excluded = append(run.excluded, decision.ReleaseID)
			run.retryExcluded = true
		case outcome.Kind != worker.OutcomeFinish || outcome.WriteOwner == nil:
			// Nothing to write (a parser policy fault): the release keeps
			// waiting; it holds no episode of this round.
			run.excluded = append(run.excluded, decision.ReleaseID)
		default:
			run.evaluations = append(run.evaluations, decidedEvaluation{decision: decision, outcome: outcome})
		}
	}
}

// ObserveRecovery: an interrupted round wrote nothing (its writes commit with
// the finish), so the claim ends failed with no effect; the next trigger runs
// the round again.
func (h competitionHandler) ObserveRecovery(ctx context.Context, c workitems.Claim) (worker.Outcome, error) {
	if h.service == nil || c.Kind != workitems.KindSubscriptionCompetition || c.Owner.Type != workitems.OwnerFileMaintenance {
		return worker.Outcome{}, workitems.ErrInvalidSpec
	}
	o := worker.Finish(workitems.StateFailed, workitems.OwnerResult{State: workitems.StateFailed, ReasonCode: "WORKER_EXECUTION_STOPPED", Reason: "worker execution ended without an external effect"}, nil)
	o.Disposition = workitems.DispositionNoExternalEffect
	return o, nil
}

func (s *Service) decideCompetition(ctx context.Context, animeID string) (competition.Round, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return competition.Round{}, err
	}
	defer func() { _ = tx.Rollback() }()
	return s.competition.CompetitionDecisions(ctx, tx, animeID)
}

// competitionNext is the item a commit leaves behind.
type competitionNext struct {
	trigger string
	retry   int
	delayed bool
}

// commitCompetition commits a round in the handler's own transaction and
// reports it as a committed outcome. Lock order: managed anime (key share),
// subscription, the claimed item, then each release (the release owner
// writers lock their own release row after the subscription). Every producer
// locks the subscription before it merges into the item, so none can deadlock
// the commit. A commit that still loses a lock race (or meets a serialization
// failure) ends the claim in a fresh transaction and leaves a fresh item.
func (s *Service) commitCompetition(ctx context.Context, claim workitems.Claim, run competitionRun) (worker.Outcome, error) {
	animeID := claim.Owner.ID
	result, written, err := s.commitCompetitionTx(ctx, claim, run)
	if err != nil {
		if !competitionContention(err) {
			return worker.Outcome{}, err
		}
		if err := s.restartCompetition(ctx, claim); err != nil {
			return worker.Outcome{}, err
		}
		result, written = workitems.OwnerResult{State: "moved", ReasonCode: competition.TriggerRoundMoved, Reason: safeReason(err.Error())}, nil
	}
	outcome := worker.Committed(claim, result)
	outcome.AfterCommit = func(ctx context.Context) {
		for _, evaluation := range written {
			if evaluation.outcome.AfterCommit != nil {
				evaluation.outcome.AfterCommit(ctx)
			}
		}
		if len(written) > 0 && s.eventHub != nil {
			s.eventHub.Publish(events.Event{Target: events.TargetRSS, ManagedAnimeID: animeID})
		}
	}
	return outcome, nil
}

func (s *Service) commitCompetitionTx(ctx context.Context, claim workitems.Claim, run competitionRun) (workitems.OwnerResult, []decidedEvaluation, error) {
	animeID := claim.Owner.ID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workitems.OwnerResult{}, nil, fmt.Errorf("begin competition commit: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	version, found, err := lockCompetitionOwnerTx(ctx, tx, s.workItems, claim)
	if err != nil {
		return workitems.OwnerResult{}, nil, err
	}
	finish := func(result workitems.OwnerResult, written []decidedEvaluation, next *competitionNext) (workitems.OwnerResult, []decidedEvaluation, error) {
		if err := s.workItems.FinishLockedTx(ctx, tx, claim, workitems.StateSucceeded, version); err != nil {
			return workitems.OwnerResult{}, nil, err
		}
		if next != nil && found {
			delay := s.retryAfter
			if !next.delayed {
				delay = 0
			}
			input := map[string]any{"trigger": next.trigger}
			if next.retry > 0 {
				input["retry"] = next.retry
			}
			if err := competition.MergeTx(ctx, tx, animeID, version, input, delay); err != nil {
				return workitems.OwnerResult{}, nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return workitems.OwnerResult{}, nil, fmt.Errorf("commit competition: %w", err)
		}
		return result, written, nil
	}
	if !found {
		return finish(workitems.OwnerResult{State: "idle", ReasonCode: competitionInactive}, nil, nil)
	}
	gate, err := competitionGate(ctx, tx, animeID)
	if err != nil {
		return workitems.OwnerResult{}, nil, err
	}
	switch {
	case gate != "":
		// Whatever keeps the gate closed (a live evaluation, a first refresh)
		// enqueues the competition when it ends; a stopped subscription's
		// resume does.
		return finish(workitems.OwnerResult{State: "waiting", ReasonCode: gate}, nil, nil)
	case run.gate != "" || run.moved:
		// The gate opened after the unlocked read, or a decided release moved:
		// decide again in a fresh round.
		return finish(workitems.OwnerResult{State: "moved", ReasonCode: competition.TriggerRoundMoved}, nil, &competitionNext{trigger: competition.TriggerRoundMoved})
	}
	locked, err := s.competition.CompetitionDecisions(ctx, tx, animeID)
	if err != nil {
		return workitems.OwnerResult{}, nil, err
	}
	if !locked.Equal(run.round) {
		return finish(workitems.OwnerResult{State: "moved", ReasonCode: competition.TriggerRoundMoved}, nil, &competitionNext{trigger: competition.TriggerRoundMoved})
	}
	if _, err := tx.ExecContext(ctx, `UPDATE subscriptions SET competition_first_round_at = CURRENT_TIMESTAMP WHERE managed_anime_id = $1 AND competition_first_round_at IS NULL`, animeID); err != nil {
		return workitems.OwnerResult{}, nil, fmt.Errorf("record the first competition round: %w", err)
	}
	written := make([]decidedEvaluation, 0, len(run.evaluations))
	excluded := append([]string(nil), run.excluded...)
	retryExcluded := run.retryExcluded
	for _, evaluation := range run.evaluations {
		ok, err := writeCompetitionDecisionTx(ctx, tx, evaluation)
		if err != nil {
			if competitionContention(err) {
				return workitems.OwnerResult{}, nil, err
			}
			excluded = append(excluded, evaluation.decision.ReleaseID)
			retryExcluded = true
			continue
		}
		if ok {
			written = append(written, evaluation)
		}
	}
	next, err := s.competitionFollowUp(ctx, tx, claim, run.round, retryExcluded)
	if err != nil {
		return workitems.OwnerResult{}, nil, err
	}
	ids := make([]string, 0, len(written))
	for _, evaluation := range written {
		ids = append(ids, evaluation.decision.ReleaseID)
	}
	reason := "releases: " + strings.Join(ids, ",")
	if len(excluded) > 0 {
		reason += "; left waiting: " + strings.Join(excluded, ",")
	}
	result := workitems.OwnerResult{State: "decided", ReasonCode: "competition_decided", Reason: safeReason(reason)}
	if len(run.round.Decisions) == 0 {
		result = workitems.OwnerResult{State: "idle", ReasonCode: run.round.Reason}
	}
	return finish(result, written, next)
}

// lockCompetitionOwnerTx takes the commit's locks in the producers' order:
// the managed anime (key share), the subscription row, then the claim. found
// is false when the instance has no subscription any more.
func lockCompetitionOwnerTx(ctx context.Context, tx *sql.Tx, store *workitems.Store, claim workitems.Claim) (int64, bool, error) {
	animeID := claim.Owner.ID
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM managed_anime WHERE id = $1 FOR KEY SHARE`, animeID); err != nil {
		return 0, false, fmt.Errorf("lock managed anime for competition: %w", err)
	}
	var version int64
	found := true
	err := tx.QueryRowContext(ctx, `SELECT version FROM subscriptions WHERE managed_anime_id = $1 FOR UPDATE`, animeID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		found, version = false, claim.ExpectedOwnerVersion
	} else if err != nil {
		return 0, false, fmt.Errorf("lock subscription for competition: %w", err)
	}
	if err := store.LockClaimTx(ctx, tx, claim); err != nil {
		return 0, false, err
	}
	return version, found, nil
}

// writeCompetitionDecisionTx writes one decided release behind a savepoint:
// a release whose owner write fails (its inputs changed under the lock, an
// acceptance refusal) is rolled back alone and keeps waiting. ok is false
// when the release was left unwritten.
func writeCompetitionDecisionTx(ctx context.Context, tx *sql.Tx, evaluation decidedEvaluation) (bool, error) {
	if _, err := tx.ExecContext(ctx, `SAVEPOINT competition_release`); err != nil {
		return false, fmt.Errorf("open competition release savepoint: %w", err)
	}
	if _, err := evaluation.outcome.WriteOwner(ctx, txscope.Wrap(tx), evaluation.outcome.OwnerResult); err != nil {
		if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT competition_release`); rollbackErr != nil {
			return false, fmt.Errorf("roll back competition release %s: %w", evaluation.decision.ReleaseID, rollbackErr)
		}
		return false, fmt.Errorf("write competition decision for release %s: %w", evaluation.decision.ReleaseID, err)
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT competition_release`); err != nil {
		return false, fmt.Errorf("release competition release savepoint: %w", err)
	}
	return true, nil
}

// competitionFollowUp names the item the commit leaves behind. The next round
// runs at once when this round's writes let in a release this round did not
// admit (an admitted release that did not start a download frees its
// episodes for the next candidate; a release admitted again is not a reason,
// so the chain is bounded). A round that left a release waiting after a
// failed decided stage runs again after the retry delay, at most MaxAttempts
// times in a row.
func (s *Service) competitionFollowUp(ctx context.Context, tx *sql.Tx, claim workitems.Claim, round competition.Round, retryExcluded bool) (*competitionNext, error) {
	animeID := claim.Owner.ID
	admitted := map[string]bool{}
	for _, decision := range round.Decisions {
		if decision.Admitted {
			admitted[decision.ReleaseID] = true
		}
	}
	gate, err := competitionGate(ctx, tx, animeID)
	if err != nil || gate != "" {
		return nil, err
	}
	next, err := s.competition.CompetitionDecisions(ctx, tx, animeID)
	if err != nil {
		return nil, err
	}
	for _, decision := range next.Decisions {
		if decision.Admitted && !admitted[decision.ReleaseID] {
			return &competitionNext{trigger: competition.TriggerFollowUp}, nil
		}
	}
	if retryExcluded {
		if retry := competitionRetryCount(claim.InputSnapshot) + 1; retry <= competition.MaxAttempts {
			return &competitionNext{trigger: competition.TriggerRoundRetry, retry: retry, delayed: true}, nil
		}
	}
	return nil, nil
}

// competitionRetryCount reads how many delayed retries in a row led to this
// round.
func competitionRetryCount(input json.RawMessage) int {
	var snapshot struct {
		Trigger string `json:"trigger"`
		Retry   int    `json:"retry"`
	}
	if json.Unmarshal(input, &snapshot) != nil || snapshot.Trigger != competition.TriggerRoundRetry {
		return 0
	}
	return snapshot.Retry
}

// restartCompetition ends a claim whose commit lost a lock race and leaves a
// fresh item that runs at once, so contention spends no attempt.
func (s *Service) restartCompetition(ctx context.Context, claim workitems.Claim) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin competition restart: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	version, found, err := lockCompetitionOwnerTx(ctx, tx, s.workItems, claim)
	if err != nil {
		return err
	}
	if err := s.workItems.FinishLockedTx(ctx, tx, claim, workitems.StateSucceeded, version); err != nil {
		return err
	}
	if found {
		if err := competition.MergeTx(ctx, tx, claim.Owner.ID, version, map[string]any{"trigger": competition.TriggerRoundMoved}, 0); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit competition restart: %w", err)
	}
	return nil
}

// competitionContention reports a lock race or serialization failure of the
// commit: deadlock_detected, serialization_failure, lock_not_available.
func competitionContention(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "40P01", "40001", "55P03":
		return true
	}
	return false
}
