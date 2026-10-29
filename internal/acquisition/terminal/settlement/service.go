package settlement

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"bansoko/internal/acquisition/terminal/domain"
)

// SettlementService is the database half of T1 settlement plus the two
// lifecycle passes that follow a root's end: the retirement disposition and
// the T20 compact on detail expiry.
//
//   - Settle: every download root with available acquisition_settle work has
//     its facts read in one snapshot, run through the pure settler
//     (domain.Decide), and the decision handed to acquisition_settle (or
//     acquisition_settle_defer). The owner-held function re-checks the
//     facts revision and live work under the root lock, so a decision made on
//     stale facts never lands. A download task deleted in the downloader
//     (DOWNLOADER_TASK_REMOVED) or cancelled before import
//     (PROVIDER_GROUP_NOT_ALLOWED, USER_CANCELLED) settles its root
//     cancelled, not failed.
//   - Dispose: a running root whose owner retired settles to cancelled /
//     OWNER_RETIRED once no admitted external effect is still in flight.
//   - Compact: a terminal full root whose detail_until has passed, whose task
//     is not bound to a Release and whose owner is not retired is compacted
//     with trigger detail_expired. Release-bound roots are compacted by the
//     local cleanup or one-shot reclaim that deletes their Release.
//
// Each pass reads only open rows through a partial index: due settle work
// (work_items_claim_kind_idx), running roots (acquisitions_running_idx) and
// unpinned detail-expired roots (acquisitions_compact_due_idx). A root whose
// detail expiry can never compact it (acquisitions.compact_pinned: a Release
// still names its task, or a placement obligation depends on it) leaves the
// compact scan until a Release retry or cleanup re-derives the flag.
//
// No pass talks to qB or the filesystem; nothing here deletes media.
type SettlementService struct {
	DB *sql.DB
	// Now is the evaluation instant of the compact expiry predicate. Nil means
	// the database clock. Rows always record the database clock.
	Now   func() time.Time
	Batch int
}

// SettlementResult counts one pass.
type SettlementResult struct {
	Settled, Deferred, Disposed, Compacted int
}

// NewSettlementService builds the service over the runtime database.
func NewSettlementService(db *sql.DB) (*SettlementService, error) {
	if db == nil {
		return nil, errors.New("acquisition settlement requires a database")
	}
	return &SettlementService{DB: db, Batch: 100}, nil
}

func (s *SettlementService) batch() int {
	if s.Batch <= 0 {
		return 100
	}
	return s.Batch
}

func (s *SettlementService) evaluationNow() sql.NullTime {
	if s.Now == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: s.Now(), Valid: true}
}

// RunPass runs settle, dispose and compact once.
func (s *SettlementService) RunPass(ctx context.Context) (SettlementResult, error) {
	var result SettlementResult
	// A root has at most one live settle item (work_items_live_owner_kind_uq),
	// so no DISTINCT: the scan walks the due settle items in claim order.
	roots, err := s.ids(ctx, `SELECT w.owner_id FROM work_items w JOIN acquisitions r ON r.id=w.owner_id
WHERE w.owner_type='acquisition' AND w.kind='acquisition_settle' AND w.state='available' AND w.available_at<=CURRENT_TIMESTAMP
  AND r.state='running' AND r.record_state='full' AND r.execution_branch='download'
ORDER BY w.available_at, w.id LIMIT $1`, s.batch())
	if err != nil {
		return result, err
	}
	for _, root := range roots {
		status, err := s.SettleRoot(ctx, root)
		if err != nil {
			return result, err
		}
		switch status {
		case "settled":
			result.Settled++
		case "deferred":
			result.Deferred++
		}
	}
	disposed, err := s.DisposeRetired(ctx)
	result.Disposed = disposed
	if err != nil {
		return result, err
	}
	compacted, err := s.CompactExpired(ctx)
	result.Compacted = compacted
	return result, err
}

func (s *SettlementService) ids(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list acquisition roots: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SettleRoot reads one root's facts, decides and records the decision. It
// returns the database status (settled, deferred, stale, live_work,
// already_terminal, not_running).
func (s *SettlementService) SettleRoot(ctx context.Context, root string) (string, error) {
	facts, revision, err := s.readFacts(ctx, root)
	if err != nil {
		return "", err
	}
	facts.SettledAt = time.Now()
	decision, decideErr := domain.Decide(facts)
	var raw []byte
	switch {
	case errors.Is(decideErr, domain.ErrExecutionGraphIncomplete):
		err = s.DB.QueryRowContext(ctx, `SELECT acquisition_settle_defer($1,$2,'EXECUTION_GRAPH_INCOMPLETE')`, root, revision).Scan(&raw)
	case decideErr != nil:
		return "", decideErr
	case decision.Kind == domain.Continue:
		err = s.DB.QueryRowContext(ctx, `SELECT acquisition_settle_defer($1,$2,NULL)`, root, revision).Scan(&raw)
	default:
		value := decision.Receipt.Value()
		if verr := domain.Validate("TerminalReceiptV1", value); verr != nil {
			return "", fmt.Errorf("settle %s: receipt invalid: %w", root, verr)
		}
		synthesized := make([]map[string]string, 0, len(decision.Synthesized))
		for _, item := range decision.Synthesized {
			synthesized = append(synthesized, map[string]string{"file_ref": string(item.FileRef), "reason_code": string(item.ReasonCode)})
		}
		synthesizedJSON, merr := json.Marshal(synthesized)
		if merr != nil {
			return "", merr
		}
		err = s.DB.QueryRowContext(ctx, `SELECT acquisition_settle($1,$2,$3::jsonb,$4::jsonb)`,
			root, revision, string(domain.Canonical(value)), string(synthesizedJSON)).Scan(&raw)
	}
	if err != nil {
		return "", fmt.Errorf("settle acquisition %s: %w", root, err)
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	return out.Status, nil
}

// readFacts reads the settler's facts of one root in one REPEATABLE READ
// snapshot.
func (s *SettlementService) readFacts(ctx context.Context, root string) (domain.SettlementFacts, int64, error) {
	var facts domain.SettlementFacts
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return facts, 0, fmt.Errorf("begin settlement facts: %w", err)
	}
	defer tx.Rollback()
	var branch string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT execution_branch,facts_revision FROM acquisitions WHERE id=$1`, root).
		Scan(&branch, &revision); err != nil {
		return facts, 0, fmt.Errorf("read settlement root %s: %w", root, err)
	}
	facts.Branch = domain.Branch(branch)
	if err := tx.QueryRowContext(ctx, `SELECT acquisition_live_work($1)`, root).Scan(&facts.LiveWork); err != nil {
		return facts, 0, fmt.Errorf("read live work %s: %w", root, err)
	}

	var reqDigest sql.NullString
	var itemCount, requiredCount sql.NullInt64
	var sealed bool
	var manifestDigest sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT requirements_digest,requirements_item_count,requirements_required_count,requirements_sealed_at IS NOT NULL,manifest_digest FROM acquisitions WHERE id=$1`, root).
		Scan(&reqDigest, &itemCount, &requiredCount, &sealed, &manifestDigest)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		facts.Requirements = domain.RequirementsNotReached
	case err != nil:
		return facts, 0, fmt.Errorf("read requirements %s: %w", root, err)
	case !sealed:
		facts.Requirements = domain.RequirementsIncomplete
		if !manifestDigest.Valid {
			facts.Requirements = domain.RequirementsNotReached
		}
	default:
		facts.Requirements = domain.RequirementsSealed
		digest := domain.DigestText(reqDigest.String)
		facts.RequirementsDigest = &digest
		facts.HeaderItemCount, facts.HeaderRequired = itemCount.Int64, requiredCount.Int64
		var target sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT target_digest FROM acquisitions WHERE id=$1`, root).Scan(&target); err != nil {
			return facts, 0, err
		}
		if target.Valid {
			t := domain.DigestText(target.String)
			facts.TargetDigest = &t
		}
		rows, err := tx.QueryContext(ctx, `SELECT f.file_ref, f.requirement='required', f.outcome, f.effect_class, f.delivery_reason_code
FROM acquisition_files f
WHERE f.acquisition_id=$1 AND f.requirement IS NOT NULL ORDER BY f.file_ref COLLATE "C"`, root)
		if err != nil {
			return facts, 0, fmt.Errorf("read requirement files %s: %w", root, err)
		}
		for rows.Next() {
			var ref string
			var required bool
			var outcome, effect, reason sql.NullString
			if err := rows.Scan(&ref, &required, &outcome, &effect, &reason); err != nil {
				rows.Close()
				return facts, 0, err
			}
			file := domain.FileFact{Ref: domain.ID(ref), Required: required}
			if outcome.Valid {
				file.Result = &domain.FileResult{Outcome: domain.Outcome(outcome.String), Effect: domain.EffectClass(effect.String)}
				// File operations record their own failure codes (target_parent_invalid,
				// source_facts_invalid…); map them onto the receipt grammar like the
				// upstream codes, or a failed file would block the root forever.
				if reason.Valid && reason.String != "" {
					file.Result.ReasonCode = safeReason(reason.String)
				}
			}
			facts.Files = append(facts.Files, file)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return facts, 0, err
		}
	}
	upstream, err := readUpstreamFailure(ctx, tx, root)
	if err != nil {
		return facts, 0, err
	}
	facts.Upstream = upstream
	return facts, revision, nil
}

// readUpstreamFailure is the first definite failure upstream of the files:
// the download task, then inspection (no verified artifact and a failed one),
// then planning (the latest plan failed and none succeeded).
func readUpstreamFailure(ctx context.Context, tx *sql.Tx, root string) (*domain.Failure, error) {
	var taskState string
	var taskCode sql.NullString
	var waited bool
	err := tx.QueryRowContext(ctx, `SELECT state,failure_code,wait_started_at IS NOT NULL FROM download_tasks WHERE acquisition_id=$1`, root).Scan(&taskState, &taskCode, &waited)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read settlement task %s: %w", root, err)
	}
	if err == nil && taskState == "failed" {
		stage := domain.StageSubmit
		if waited {
			stage = domain.StageDownloadWait
		}
		return &domain.Failure{Stage: stage, ReasonCode: safeReason(taskCode.String)}, nil
	}
	var artifactCode sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT failure_code FROM artifacts WHERE acquisition_id=$1 AND state='failed'
  AND NOT EXISTS(SELECT 1 FROM artifacts v WHERE v.acquisition_id=$1 AND v.state='verified') ORDER BY created_at DESC, id LIMIT 1`, root).Scan(&artifactCode)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read settlement artifact %s: %w", root, err)
	}
	if err == nil {
		return &domain.Failure{Stage: domain.StageInspect, ReasonCode: safeReason(artifactCode.String)}, nil
	}
	var planCode sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT outcome_code FROM postprocessing_plans WHERE acquisition_id=$1 AND state='failed'
  AND NOT EXISTS(SELECT 1 FROM postprocessing_plans s WHERE s.acquisition_id=$1 AND s.state IN ('queued','waiting_config','succeeded'))
ORDER BY created_at DESC, id LIMIT 1`, root).Scan(&planCode)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read settlement plan %s: %w", root, err)
	}
	if err == nil {
		return &domain.Failure{Stage: domain.StagePlan, ReasonCode: safeReason(planCode.String)}, nil
	}
	return nil, nil
}

// safeReason maps a recorded stage failure code onto the receipt's safe
// reason grammar. A code that is already safe is kept; anything else is
// upper-cased with unsafe characters replaced, never dropped into free text.
func safeReason(code string) domain.Reason {
	if domain.ValidReason(code) {
		return domain.Reason(code)
	}
	var b strings.Builder
	for _, c := range strings.ToUpper(code) {
		if c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteByte('_')
		}
	}
	mapped := strings.Trim(b.String(), "_")
	if len(mapped) > 128 {
		mapped = mapped[:128]
	}
	if !domain.ValidReason(mapped) {
		return "UPSTREAM_FAILED"
	}
	return domain.Reason(mapped)
}

// DisposeRetired settles running roots of retired owners to cancelled.
func (s *SettlementService) DisposeRetired(ctx context.Context) (int, error) {
	roots, err := s.ids(ctx, `SELECT r.id FROM acquisitions r
JOIN managed_anime l ON l.id=acquisition_owner(r.id)
WHERE r.state='running' AND r.record_state='full' AND l.lifecycle_state='retired' ORDER BY r.id LIMIT $1`, s.batch())
	if err != nil {
		return 0, err
	}
	disposed := 0
	for _, root := range roots {
		status, err := s.DisposeRoot(ctx, root)
		if err != nil {
			return disposed, err
		}
		if status == "cancelled" {
			disposed++
		}
	}
	return disposed, nil
}

// DisposeRoot runs the retirement disposition of one root: cancelled,
// not_running, owner_active or effect_unresolved.
func (s *SettlementService) DisposeRoot(ctx context.Context, root string) (string, error) {
	var status string
	if err := s.DB.QueryRowContext(ctx, `SELECT acquisition_dispose_retired($1)`, root).Scan(&status); err != nil {
		return "", fmt.Errorf("dispose acquisition %s: %w", root, err)
	}
	return status, nil
}

// CompactExpired compacts detail-expired roots that no Release pins and whose
// owner is still active (a retired owner's roots are compacted by its local
// cleanup). Pinned roots (compact_pinned) are exactly the release-bound ones
// plus those a placement obligation keeps (acquisition_compact would answer
// dependents_present), so the scan never reads them.
func (s *SettlementService) CompactExpired(ctx context.Context) (int, error) {
	now := s.evaluationNow()
	roots, err := s.ids(ctx, `SELECT r.id FROM acquisitions r
WHERE r.detail_until IS NOT NULL AND r.record_state='full' AND NOT r.compact_pinned
  AND r.state<>'running' AND r.detail_until<=COALESCE($2::timestamptz,CURRENT_TIMESTAMP)
  AND NOT EXISTS(SELECT 1 FROM download_tasks t JOIN releases rel ON rel.download_task_id=t.id WHERE t.acquisition_id=r.id)
  AND NOT EXISTS(SELECT 1 FROM managed_anime l WHERE l.id=acquisition_owner(r.id) AND l.lifecycle_state<>'active')
ORDER BY r.detail_until, r.id LIMIT $1`, s.batch(), now)
	if err != nil {
		return 0, err
	}
	compacted := 0
	for _, root := range roots {
		var raw []byte
		if err := s.DB.QueryRowContext(ctx, `SELECT acquisition_compact($1,'detail_expired',$2)`, root, now).Scan(&raw); err != nil {
			return compacted, fmt.Errorf("compact acquisition %s: %w", root, err)
		}
		var out struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return compacted, err
		}
		if out.Status == "compacted" {
			compacted++
		}
	}
	return compacted, nil
}
