package application

import (
	"context"
	"database/sql"
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

// errCompetitionMoved: the round decided outside the lock differs from the
// one the subscription lock sees; the claim retries with a fresh round.
var errCompetitionMoved = errors.New("subscription competition round changed before commit")

// awaitsCompetition reports whether a downloadable release stops at the plan:
// the competition is wired, the subscription is active and this is not the
// competition's own decided stage. A stopped subscription's explicit retry
// keeps the direct path.
func (s *Service) awaitsCompetition(snapshot releaseSnapshot) bool {
	return s != nil && s.competition != nil && snapshot.competitionDecision == nil && snapshot.subscriptionState == "active"
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
	previous := outcome.PostTransition
	outcome.PostTransition = func(ctx context.Context, scope txscope.Tx) error {
		if previous != nil {
			if err := previous(ctx, scope); err != nil {
				return err
			}
		}
		tx := scope.SQLTx()
		if tx == nil {
			return errors.New("competition enqueue transaction is nil")
		}
		_, err := competition.EnqueueTx(ctx, tx, s.workItems, managedAnimeID, competition.TriggerReleaseEvaluated, competition.WhenAwaiting)
		return err
	}
	return outcome
}

// withRefreshCompetition makes a refresh write that records the source's
// refresh (success, not modified, failure) enqueue the instance's competition
// when a release waits for it: a first refresh may end the first-round wait.
// The writer locks the subscription first. When the write left the site's
// circuit open, the other instances waiting on a never-refreshed source of
// that site are enqueued after the commit (best effort; the circuit's end
// refreshes those sources anyway).
func (s *Service) withRefreshCompetition(outcome worker.Outcome, sourceID string) worker.Outcome {
	inner := outcome.WriteOwner
	if s == nil || s.competition == nil || inner == nil || strings.TrimSpace(sourceID) == "" {
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
		if _, err := competition.EnqueueTx(ctx, tx, s.workItems, animeID, competition.TriggerSourceRefreshed, competition.WhenAwaiting); err != nil {
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
		if _, err := competition.EnqueueTx(ctx, tx, s.workItems, id, competition.TriggerSourceRefreshed, competition.WhenAwaiting); err != nil {
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

// competitionGate reads, without locks, whether the instance's round may run.
// The first round waits until every source in scope has refreshed once (a
// failed refresh writes last_refresh_at too, and a source whose site circuit
// is open counts as refreshed); every round waits until no release evaluation
// of the instance is live. A torrent fetch failure ends its evaluation as a
// failed verdict, so it never holds the round.
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
       EXISTS (
           SELECT 1 FROM rss_sources src
           LEFT JOIN provider_group_selections selection
             ON selection.subscription_id = src.subscription_id AND selection.provider = src.provider
            AND selection.group_ref = src.provider_group_ref
           LEFT JOIN rss_source_runtime runtime ON runtime.rss_source_id = src.id
           LEFT JOIN rss_provider_circuits circuit ON circuit.provider = src.provider
           WHERE src.subscription_id = sub.id
             AND (src.source_kind <> 'provider_group' OR COALESCE(selection.available AND selection.effective_selected, FALSE))
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

// Handle runs one round of the instance's competition:
//
//  1. the gate (competitionGate) decides whether the round runs at all;
//  2. the round is decided in a read-only transaction;
//  3. every decided release runs the decided stage of the one release
//     evaluation outside any transaction (target probe, naming vars, qB);
//  4. the finish transaction locks the subscription, checks the gate again,
//     decides the round again and refuses one that moved, then writes every
//     release's evaluation in the same transaction.
//
// A release the round admitted that did not start a download lets the next
// candidate of its episodes run in a follow-up round.
func (h competitionHandler) Handle(ctx context.Context, claim workitems.Claim) (worker.Outcome, error) {
	s := h.service
	if s == nil || s.db == nil || s.workItems == nil || s.competition == nil {
		return worker.Outcome{}, errors.New("subscription competition is not fully wired")
	}
	if claim.Kind != workitems.KindSubscriptionCompetition || claim.Owner.Type != workitems.OwnerFileMaintenance || strings.TrimSpace(claim.Owner.ID) == "" {
		return worker.Outcome{}, fmt.Errorf("subscription competition handler received %s/%s", claim.Owner.Type, claim.Kind)
	}
	animeID := claim.Owner.ID
	reason, err := competitionGate(ctx, s.db, animeID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if reason != "" {
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "waiting", ReasonCode: reason}, nil), nil
	}
	round, err := s.decideCompetition(ctx, animeID)
	if err != nil {
		return worker.Outcome{}, err
	}
	if len(round.Decisions) == 0 {
		return worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "idle", ReasonCode: round.Reason}, nil), nil
	}
	evaluations := make([]decidedEvaluation, 0, len(round.Decisions))
	ids := make([]string, 0, len(round.Decisions))
	for _, decision := range round.Decisions {
		snapshot, err := s.loadReleaseSnapshot(ctx, decision.ReleaseID)
		if err != nil {
			return worker.Outcome{}, err
		}
		if snapshot.releaseVersion != decision.Version {
			return worker.Retry("competition_round_changed", "a decided release changed before its evaluation", s.retryAfter, nil), nil
		}
		decided := decision
		snapshot.competitionDecision = &decided
		releaseClaim := workitems.Claim{
			Owner: workitems.OwnerRef{Type: workitems.OwnerRelease, ID: decision.ReleaseID}, Kind: workitems.KindReleaseEvaluate,
			ExpectedOwnerVersion: decision.Version, ManagedAnimeID: animeID,
		}
		outcome, err := s.evaluateRelease(ctx, releaseClaim, snapshot)
		if err != nil {
			return worker.Outcome{}, err
		}
		if outcome.Kind == worker.OutcomeRetry {
			// The decided stage could not finish (naming vars interrupted, a
			// qB lookup failed): nothing of the round is written.
			return worker.Retry(outcome.ReasonCode, outcome.Reason, s.retryAfter, nil), nil
		}
		evaluations = append(evaluations, decidedEvaluation{decision: decision, outcome: outcome})
		ids = append(ids, decision.ReleaseID)
	}
	outcome := worker.Finish(workitems.StateSucceeded, workitems.OwnerResult{State: "decided", ReasonCode: "competition_decided", Reason: "releases: " + strings.Join(ids, ",")}, s.competitionCommitWriter(animeID, round, evaluations))
	outcome.PostTransition = s.competitionFollowUp(animeID, round)
	outcome.AfterCommit = func(ctx context.Context) {
		for _, evaluation := range evaluations {
			if evaluation.outcome.AfterCommit != nil {
				evaluation.outcome.AfterCommit(ctx)
			}
		}
		if s.eventHub != nil {
			s.eventHub.Publish(events.Event{Target: events.TargetRSS, ManagedAnimeID: animeID})
		}
	}
	return outcome, nil
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

// competitionCommitWriter writes a round inside the competition's finish
// transaction. Lock order: subscription, then each release (the release owner
// writers lock their own release row after the subscription they share).
func (s *Service) competitionCommitWriter(animeID string, round competition.Round, evaluations []decidedEvaluation) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("subscription competition transaction is nil")
		}
		var version int64
		err := tx.QueryRowContext(ctx, `SELECT version FROM subscriptions WHERE managed_anime_id = $1 FOR UPDATE`, animeID).Scan(&version)
		if errors.Is(err, sql.ErrNoRows) {
			return 1, nil
		}
		if err != nil {
			return 0, fmt.Errorf("lock subscription for competition: %w", err)
		}
		// A stopped subscription or a release evaluation that started since
		// the round was decided ends the round unwritten; the evaluation's end
		// enqueues the next one.
		reason, err := competitionGate(ctx, tx, animeID)
		if err != nil {
			return 0, err
		}
		if reason != "" {
			return version, nil
		}
		locked, err := s.competition.CompetitionDecisions(ctx, tx, animeID)
		if err != nil {
			return 0, err
		}
		if !locked.Equal(round) {
			return 0, errCompetitionMoved
		}
		for _, evaluation := range evaluations {
			if evaluation.outcome.WriteOwner == nil {
				continue
			}
			if _, err := evaluation.outcome.WriteOwner(ctx, scope, evaluation.outcome.OwnerResult); err != nil {
				return 0, fmt.Errorf("write competition decision for release %s: %w", evaluation.decision.ReleaseID, err)
			}
		}
		return version, nil
	}
}

// competitionFollowUp enqueues the next round when this round's writes let a
// release in that this round did not admit: an admitted release that did not
// start a download frees its episodes for the next candidate. A release the
// next round would admit again is not a reason (the set of candidates only
// shrinks, so the follow-up chain is bounded).
func (s *Service) competitionFollowUp(animeID string, round competition.Round) func(context.Context, txscope.Tx) error {
	admitted := map[string]bool{}
	for _, decision := range round.Decisions {
		if decision.Admitted {
			admitted[decision.ReleaseID] = true
		}
	}
	return func(ctx context.Context, scope txscope.Tx) error {
		tx := scope.SQLTx()
		if tx == nil {
			return errors.New("subscription competition transaction is nil")
		}
		reason, err := competitionGate(ctx, tx, animeID)
		if err != nil || reason != "" {
			return err
		}
		next, err := s.competition.CompetitionDecisions(ctx, tx, animeID)
		if err != nil {
			return err
		}
		for _, decision := range next.Decisions {
			if decision.Admitted && !admitted[decision.ReleaseID] {
				_, err := competition.EnqueueTx(ctx, tx, s.workItems, animeID, competition.TriggerFollowUp, competition.Always)
				return err
			}
		}
		return nil
	}
}
