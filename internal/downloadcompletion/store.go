package downloadcompletion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"bansoko/internal/batchplan"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/redact"
	"bansoko/internal/workitems"
)

// Row states.
const (
	StatePending   = "pending"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
)

const completionMaxAttempts = 3

var infoHashPattern = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// Row is one download_completions row.
type Row struct {
	ID              string
	AcquisitionID   string
	Action          string
	InfoHash        string
	RemoteRoot      string
	ApplicationRoot string
	SeedLimits      *batchplan.SeedLimits
	State           string
	ResultCode      string
	Version         int64
}

func (r Row) completion() Completion {
	return Completion{ID: r.ID, AcquisitionID: r.AcquisitionID, Action: r.Action, InfoHash: r.InfoHash, RemoteRoot: r.RemoteRoot, ApplicationRoot: r.ApplicationRoot, SeedLimits: r.SeedLimits.Clone()}
}

// Store owns every write of download_completions and the reads the
// completion action needs about other owners.
type Store struct {
	DB   *sql.DB
	Work *workitems.Store
}

// candidate is a succeeded download acquisition without a completion row yet.
type candidate struct {
	AcquisitionID string
	ReleaseID     string
	Correlation   []byte
	FrozenInput   []byte
}

// listCandidates returns succeeded, still-detailed download acquisitions that
// have no completion row. acquisitions.completion_open marks exactly those
// roots (acquisition_write_terminal sets it, the download_completions insert
// clears it in the same transaction), so the scan reads only open roots; the
// full predicate is kept so the flag can only narrow, never widen, the set.
// An acquisition is seen again only if its earlier pass attempt rolled back.
func (s *Store) listCandidates(ctx context.Context, limit int) ([]candidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
SELECT a.id, t.release_id, COALESCE(t.provider_correlation::text, ''), t.frozen_input::text
FROM acquisitions a
JOIN download_tasks t ON t.acquisition_id = a.id
WHERE a.completion_open
  AND a.state = 'succeeded' AND a.execution_branch = 'download' AND a.record_state = 'full'
  AND NOT EXISTS (SELECT 1 FROM download_completions c WHERE c.acquisition_id = a.id)
ORDER BY a.terminal_at, a.id
LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list completion candidates: %w", err)
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		var correlation, frozen string
		if err := rows.Scan(&c.AcquisitionID, &c.ReleaseID, &correlation, &frozen); err != nil {
			return nil, err
		}
		c.Correlation, c.FrozenInput = []byte(correlation), []byte(frozen)
		out = append(out, c)
	}
	return out, rows.Err()
}

// frozenFacts are the qBittorrent facts a completion row freezes.
type frozenFacts struct {
	InfoHash        string
	ProviderRef     string
	RemoteRoot      string
	ApplicationRoot string
}

// factsOf reads the torrent identity and the frozen path roots. The roots come
// from the download task's frozen input (the admission-time snapshot) and only
// fall back to the provider correlation's echo of the same snapshot.
func factsOf(c candidate) frozenFacts {
	var correlation struct {
		ExternalID   string `json:"external_id"`
		InfoHash     string `json:"info_hash"`
		PathSnapshot *struct {
			RemoteRoot      string `json:"remote_root"`
			ApplicationRoot string `json:"application_root"`
		} `json:"path_snapshot"`
	}
	var frozen struct {
		PathSnapshot *struct {
			RemoteRoot      string `json:"remote_root"`
			ApplicationRoot string `json:"application_root"`
		} `json:"path_snapshot"`
	}
	_ = json.Unmarshal(c.Correlation, &correlation)
	_ = json.Unmarshal(c.FrozenInput, &frozen)
	result := frozenFacts{InfoHash: strings.ToLower(strings.TrimSpace(correlation.InfoHash)), ProviderRef: strings.TrimSpace(correlation.ExternalID)}
	if !infoHashPattern.MatchString(result.InfoHash) {
		result.InfoHash = ""
	}
	switch {
	case frozen.PathSnapshot != nil && strings.TrimSpace(frozen.PathSnapshot.RemoteRoot) != "":
		result.RemoteRoot, result.ApplicationRoot = strings.TrimSpace(frozen.PathSnapshot.RemoteRoot), strings.TrimSpace(frozen.PathSnapshot.ApplicationRoot)
	case correlation.PathSnapshot != nil:
		result.RemoteRoot, result.ApplicationRoot = strings.TrimSpace(correlation.PathSnapshot.RemoteRoot), strings.TrimSpace(correlation.PathSnapshot.ApplicationRoot)
	}
	return result
}

// create writes the completion row of one acquisition and, for an action that
// has work to do, its WorkItem, in one transaction. It reports whether the
// row was created (false: another pass already did).
func (s *Store) create(ctx context.Context, acquisitionID, action string, limits *batchplan.SeedLimits, f frozenFacts) (created bool, err error) {
	if action != ActionSeed {
		limits = nil
	}
	var limitsJSON sql.NullString
	if limits != nil {
		raw, err := json.Marshal(limits)
		if err != nil {
			return false, fmt.Errorf("encode seed limits: %w", err)
		}
		limitsJSON = sql.NullString{String: string(raw), Valid: true}
	}
	err = s.Work.WithTx(ctx, func(tx *sql.Tx) error {
		id := uuid.NewString()
		state, result := StatePending, sql.NullString{}
		reason := sql.NullString{}
		switch {
		case action == ActionSeed && limits == nil:
			state, result = StateSucceeded, sql.NullString{String: ResultSeedKept, Valid: true}
		case f.InfoHash == "":
			// Nothing to act on: the download task never recorded which
			// qBittorrent task it was. Visible, with a typed reason.
			state, result = StateFailed, sql.NullString{String: FailureTaskUnknown, Valid: true}
			reason = sql.NullString{String: "the download task has no recorded torrent identity", Valid: true}
		}
		res, err := tx.ExecContext(ctx, `
INSERT INTO download_completions(id, acquisition_id, action, info_hash, provider_ref, remote_root, application_root, state, result_code, reason, completed_at, seed_limits)
VALUES ($1, $2, $3, NULLIF($4::text, ''), NULLIF($5::text, ''), NULLIF($6::text, ''), NULLIF($7::text, ''), $8::text, $9::text, $10::text, CASE WHEN $8::text = 'pending' THEN NULL ELSE CURRENT_TIMESTAMP END, $11::jsonb)
ON CONFLICT (acquisition_id) DO NOTHING`,
			id, acquisitionID, action, f.InfoHash, f.ProviderRef, f.RemoteRoot, f.ApplicationRoot, state, result, reason, limitsJSON)
		if err != nil {
			return fmt.Errorf("insert download completion: %w", err)
		}
		if affected, err := res.RowsAffected(); err != nil {
			return err
		} else if affected == 0 {
			return nil
		}
		created = true
		if state != StatePending {
			return nil
		}
		return s.enqueue(ctx, tx, id, 1)
	})
	return created, err
}

func (s *Store) enqueue(ctx context.Context, tx *sql.Tx, id string, version int64) error {
	input, _ := json.Marshal(map[string]any{"completion_id": id})
	_, err := s.Work.Create(ctx, tx, workitems.WorkItemSpec{
		Owner:                workitems.OwnerRef{Type: workitems.OwnerDownloadCompletion, ID: id},
		Kind:                 workitems.KindCompletionExecute,
		IdempotencyKey:       fmt.Sprintf("completion_execute:%s:%d", id, version),
		InputSnapshot:        input,
		MaxAttempts:          completionMaxAttempts,
		ExpectedOwnerVersion: version,
	})
	if err != nil {
		return fmt.Errorf("enqueue completion work item: %w", err)
	}
	return nil
}

// loadRow reads one completion by id; nil when it does not exist.
func (s *Store) loadRow(ctx context.Context, where string, arg any) (*Row, error) {
	var r Row
	var hash, remote, app, result, limits sql.NullString
	err := s.DB.QueryRowContext(ctx, `
SELECT id, acquisition_id, action, info_hash, remote_root, application_root, state, result_code, version, seed_limits::text
FROM download_completions WHERE `+where, arg).Scan(&r.ID, &r.AcquisitionID, &r.Action, &hash, &remote, &app, &r.State, &result, &r.Version, &limits)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load download completion: %w", err)
	}
	r.InfoHash, r.RemoteRoot, r.ApplicationRoot, r.ResultCode = hash.String, remote.String, app.String, result.String
	if limits.Valid {
		var value batchplan.SeedLimits
		if err := json.Unmarshal([]byte(limits.String), &value); err != nil {
			return nil, fmt.Errorf("decode download completion seed limits: %w", err)
		}
		r.SeedLimits = &value
	}
	return &r, nil
}

// ByID loads a completion by its id.
func (s *Store) ByID(ctx context.Context, id string) (*Row, error) {
	return s.loadRow(ctx, "id = $1", id)
}

// ByAcquisition loads the completion of one acquisition.
func (s *Store) ByAcquisition(ctx context.Context, acquisitionID string) (*Row, error) {
	return s.loadRow(ctx, "acquisition_id = $1", acquisitionID)
}

// boundedReason keeps a stored reason short and free of paths, URLs and
// credentials.
func boundedReason(detail string) sql.NullString {
	if strings.TrimSpace(detail) == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: redact.Text(detail, 512), Valid: true}
}

// settleWriter settles the row in the WorkItem's finish transaction.
func settleWriter(row *Row, state, code, detail string) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("download completion finish transaction is nil")
		}
		res, err := tx.ExecContext(ctx, `
UPDATE download_completions
SET state = $2, result_code = $3, reason = $4, completed_at = CURRENT_TIMESTAMP, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $5 AND state = 'pending'`, row.ID, state, code, boundedReason(detail), row.Version)
		if err != nil {
			return 0, err
		}
		if affected, err := res.RowsAffected(); err != nil || affected != 1 {
			return 0, fmt.Errorf("download completion %s changed concurrently", row.ID)
		}
		return row.Version + 1, nil
	}
}

// retryWriter records one retry in the WorkItem's retry transaction: the row
// stays pending while the budget lasts and settles failed with the last code
// when the budget is spent.
func retryWriter(row *Row, code, detail string) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, result workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("download completion retry transaction is nil")
		}
		var res sql.Result
		var err error
		if result.State == workitems.StateFailed {
			res, err = tx.ExecContext(ctx, `
UPDATE download_completions
SET state = 'failed', result_code = $2, reason = $3, completed_at = CURRENT_TIMESTAMP, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $4 AND state = 'pending'`, row.ID, code, boundedReason(detail), row.Version)
		} else {
			res, err = tx.ExecContext(ctx, `
UPDATE download_completions
SET reason = $2, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE id = $1 AND version = $3 AND state = 'pending'`, row.ID, boundedReason(detail), row.Version)
		}
		if err != nil {
			return 0, err
		}
		if affected, err := res.RowsAffected(); err != nil || affected != 1 {
			return 0, fmt.Errorf("download completion %s changed concurrently", row.ID)
		}
		return row.Version + 1, nil
	}
}

// settledWriter finishes a WorkItem against an owner that is already terminal:
// it proves the row is still in that state at that version and writes nothing.
func settledWriter(row *Row) workitems.OwnerWriter {
	return func(ctx context.Context, scope txscope.Tx, _ workitems.OwnerResult) (int64, error) {
		tx := scope.SQLTx()
		if tx == nil {
			return 0, errors.New("download completion finish transaction is nil")
		}
		var state string
		var version int64
		err := tx.QueryRowContext(ctx, `SELECT state, version FROM download_completions WHERE id = $1 FOR UPDATE NOWAIT`, row.ID).Scan(&state, &version)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (state != row.State || version != row.Version) {
			return 0, workitems.ErrStaleClaim
		}
		if err != nil {
			return 0, err
		}
		return row.Version, nil
	}
}

// SharedByOther reports whether another running acquisition uses the same
// qBittorrent task (the same infohash, adopted by a second acquisition).
func (s *Store) SharedByOther(ctx context.Context, hash, acquisitionID string) (bool, error) {
	var shared bool
	err := s.DB.QueryRowContext(ctx, `
SELECT EXISTS(
  SELECT 1 FROM download_tasks t JOIN acquisitions a ON a.id = t.acquisition_id
  WHERE a.state = 'running' AND a.id <> $2 AND lower(t.provider_correlation->>'info_hash') = $1)`, strings.ToLower(hash), acquisitionID).Scan(&shared)
	if err != nil {
		return false, fmt.Errorf("check shared torrent: %w", err)
	}
	return shared, nil
}

// EachLibraryPath yields every path a user or library file may live at, over-
// including on purpose (an extra path can only add a refusal):
//   - the physical path of every placement in any state (a released or
//     superseded file is still the user's file, and a released row keeps the
//     path it was released at);
//   - the current path of every placement companion (subtitle);
//   - the source, target and result path of every file-manager user operation
//     (a detached file that was moved or renamed lives at its result path, which
//     is recorded only there);
//   - the target of every file operation that wrote or may write a file.
func (s *Store) EachLibraryPath(ctx context.Context, visit func(path string) error) error {
	rows, err := s.DB.QueryContext(ctx, `
SELECT physical_path FROM placements
UNION
SELECT current_path FROM placement_companions
UNION
SELECT source_path FROM file_manager_operations
UNION
SELECT target_path FROM file_manager_operations WHERE target_path IS NOT NULL
UNION
SELECT result_path FROM file_manager_operations WHERE result_path IS NOT NULL
UNION
SELECT target_path FROM file_operations WHERE state IN ('pending', 'running', 'completed', 'waiting_retry')`)
	if err != nil {
		return fmt.Errorf("list library paths: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		if err := visit(path); err != nil {
			return err
		}
	}
	return rows.Err()
}
