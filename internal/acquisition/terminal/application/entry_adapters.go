package application

import (
	target "bansoko/internal/acquisition/target"
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	protocol "bansoko/internal/acquisition/terminal/domain"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"github.com/google/uuid"
)

// EntryTask contains only the legacy caller-owned task projection. It has no
// entry mode, qualification, native input or proof assembly surface.
type EntryTask struct {
	ReleaseID       string
	ExpectedVersion int64
	// Attempt is the ordinal of this download of the Release (1 = its first
	// download task). A Release accepted again after its download failed
	// derives distinct request and effect keys, so a later acceptance never
	// replays the earlier acquisition. 0 means 1.
	Attempt                   int
	FrozenInput, FrozenPolicy json.RawMessage
	CorrelationID             string
	QbConnectionID            string
	MaxAttempts               int
	ParserEvidence            *target.ParserEvidence
	Resolution                OwnerResolution
	// Replacements holds one approval per present overwrite target.
	Replacements []*ReplacementCapture
	// OverwriteTargetAbsent says the pre-download observation found nothing at
	// an overwrite target, so that target needs no replacement approval.
	OverwriteTargetAbsent bool
}
type entryAcceptor struct {
	service *Service
	mode    string
}
type RSSAutomaticAcceptor struct{ entry *entryAcceptor }

// ManualRetryAcceptor is the explicit retry of one Release through normal
// acceptance. It differs from the automatic entry only in its subscription
// policy (a stopped subscription still accepts an explicit command); the
// admission re-check is the automatic one and bypasses nothing. The database
// protocol names this entry subscription_direct / manual_acquire_v1.
type ManualRetryAcceptor struct{ entry *entryAcceptor }
type PreparedHandle struct {
	owner    *entryAcceptor
	tx       *sql.Tx
	prepared preparedNative
	replay   pendingAcceptance
	found    bool
}
type PendingHandle struct {
	owner   *entryAcceptor
	tx      *sql.Tx
	pending pendingAcceptance
}

func NewRSSAutomaticAcceptor(db *sql.DB, revisions *draft.Service) (*RSSAutomaticAcceptor, error) {
	s, err := NewService(db, revisions, 1<<20, 10000)
	if err != nil {
		return nil, err
	}
	return &RSSAutomaticAcceptor{entry: &entryAcceptor{service: s, mode: "automatic_source"}}, nil
}

// NewManualRetryEntry owns the draft dependency at the terminal boundary.
func NewManualRetryEntry(db *sql.DB, store *workitems.Store) (*ManualRetryAcceptor, error) {
	revisions, err := draft.NewService(db, draft.Dependencies{WorkItems: store})
	if err != nil {
		return nil, err
	}
	return NewManualRetryAcceptor(db, revisions)
}

func NewManualRetryAcceptor(db *sql.DB, revisions *draft.Service) (*ManualRetryAcceptor, error) {
	s, err := NewService(db, revisions, 1<<20, 10000)
	if err != nil {
		return nil, err
	}
	return &ManualRetryAcceptor{entry: &entryAcceptor{service: s, mode: "subscription_direct"}}, nil
}
func (a *RSSAutomaticAcceptor) Prepare(ctx context.Context, scope txscope.Tx, input EntryTask) (PreparedHandle, error) {
	return a.entry.prepare(ctx, scope, input)
}
func (a *ManualRetryAcceptor) Prepare(ctx context.Context, scope txscope.Tx, input EntryTask) (PreparedHandle, error) {
	return a.entry.prepare(ctx, scope, input)
}
func (a *RSSAutomaticAcceptor) Accept(ctx context.Context, scope txscope.Tx, p PreparedHandle) (PendingHandle, error) {
	return a.entry.accept(ctx, scope, p)
}
func (a *ManualRetryAcceptor) Accept(ctx context.Context, scope txscope.Tx, p PreparedHandle) (PendingHandle, error) {
	return a.entry.accept(ctx, scope, p)
}
func (a *RSSAutomaticAcceptor) Committed(p PendingHandle) (CommittedAcceptance, error) {
	return a.entry.committed(p)
}
func (a *ManualRetryAcceptor) Committed(p PendingHandle) (CommittedAcceptance, error) {
	return a.entry.committed(p)
}

// EffectKey is the download task's external effect key (the qB operation tag)
// of the attempt-th download of a Release: the first keeps the historical
// qb:<release>, a later one appends its ordinal.
func EffectKey(releaseID string, attempt int) string {
	if attempt > 1 {
		return "qb:" + releaseID + ":" + strconv.Itoa(attempt)
	}
	return "qb:" + releaseID
}

func (p PreparedHandle) Replayed() bool { return p.owner != nil && p.found }
func (p PendingHandle) TaskID() string {
	if p.owner == nil {
		return ""
	}
	v, err := protocol.ParseStrict([]byte(p.pending.receipt))
	if err != nil {
		return ""
	}
	return v.Object["download_task_id"].String
}
func (a *entryAcceptor) prepare(ctx context.Context, scope txscope.Tx, input EntryTask) (PreparedHandle, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return PreparedHandle{}, acceptError(500, "INTERNAL_INVARIANT", "invalid_txscope", nil)
	}
	if input.ReleaseID == "" || input.ExpectedVersion < 1 || input.MaxAttempts < 1 || input.MaxAttempts > 100 {
		return PreparedHandle{}, acceptError(400, "INPUT_INVALID", "entry_task_invalid", nil)
	}
	if input.Attempt < 0 {
		return PreparedHandle{}, acceptError(400, "INPUT_INVALID", "entry_task_invalid", nil)
	}
	if err := checkFrozenTaskSizes(input); err != nil {
		return PreparedHandle{}, err
	}
	attempt := input.Attempt
	if attempt == 0 {
		attempt = 1
	}
	suffix := ""
	if attempt > 1 {
		suffix = ":" + strconv.Itoa(attempt)
	}
	namespace, key, trigger := "auto_acquire_v1", "release:"+input.ReleaseID+suffix, "release_evaluate"
	if a.mode == "subscription_direct" {
		namespace, key, trigger = "manual_acquire_v1", "retry:"+input.ReleaseID+":"+strconv.Itoa(attempt), "release_retry"
	}
	effectKey := EffectKey(input.ReleaseID, attempt)
	request, err := valueOf(map[string]any{"release_ref": input.ReleaseID, "expected_version": input.ExpectedVersion, "task_input": json.RawMessage(input.FrozenInput), "task_policy": json.RawMessage(input.FrozenPolicy)})
	if err != nil {
		return PreparedHandle{}, mapAcceptanceError(err)
	}
	// The request hash binds the whole task input and policy exactly as it
	// always has. The stored request body names them by digest instead: both
	// documents are kept whole on the download task, and the body's own bound
	// (64 KiB) must not grow with the release's file inventory.
	stored, err := valueOf(map[string]any{
		"release_ref":        input.ReleaseID,
		"expected_version":   input.ExpectedVersion,
		"task_input_digest":  protocol.DigestValue("acquisition-entry-task-input:v1", request.Object["task_input"]),
		"task_policy_digest": protocol.DigestValue("acquisition-entry-task-policy:v1", request.Object["task_policy"]),
	})
	if err != nil {
		return PreparedHandle{}, mapAcceptanceError(err)
	}
	r := requestIdentity{namespace: namespace, key: key, hash: protocol.Digest("acquisition-entry-request:v1", protocol.Canonical(request)), canonical: string(protocol.Canonical(stored))}
	access := acceptAccess{scope: "deployment_owner_v1"}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, access.scope+"\x1f"+namespace+"\x1f"+key); err != nil {
		return PreparedHandle{}, mapAcceptanceError(err)
	}
	replay, found, err := replayTx(ctx, tx, access, r)
	if err != nil {
		return PreparedHandle{}, mapAcceptanceError(err)
	}
	if found {
		return PreparedHandle{owner: a, tx: tx, replay: replay, found: true}, nil
	}
	if err := input.Resolution.validate(input.FrozenPolicy); err != nil {
		return PreparedHandle{}, acceptError(500, "INTERNAL_INVARIANT", "caller_resolution_mismatch", err)
	}
	c, err := captureQualificationTx(ctx, tx, a.service.revisions, qualificationRequest{entryMode: a.mode, configKind: "owner", catalogKind: "release_input", releaseID: input.ReleaseID, requestHash: r.hash, ownerResolution: &input.Resolution})
	if err != nil {
		return PreparedHandle{}, mapAcceptanceError(err)
	}
	if c.material.release.version != input.ExpectedVersion {
		return PreparedHandle{}, acceptError(409, "INPUT_STALE", "release_version_changed", nil)
	}
	intent, err := productionIntent(ctx, tx, c, a.mode, input.QbConnectionID, input.OverwriteTargetAbsent, input.Replacements...)
	if err != nil {
		return PreparedHandle{}, mapAcceptanceError(err)
	}
	correlation := input.CorrelationID
	if correlation == "" {
		correlation = "corr-" + uuid.NewString()
	}
	intent.submit = &submitProjection{frozenInput: input.FrozenInput, frozenPolicy: input.FrozenPolicy, effectKey: effectKey, correlation: correlation, causationType: "release", causationID: input.ReleaseID, trigger: trigger}
	var parsed []target.ParserEvidence
	if input.ParserEvidence != nil {
		parsed = append(parsed, *input.ParserEvidence)
	}
	e, err := evaluateAdmission(ctx, c, intent, parsed...)
	if err != nil {
		return PreparedHandle{}, mapAcceptanceError(err)
	}
	p, err := prepareAdmission(c, e, intent, access, r, a.service.limits)
	if err != nil {
		return PreparedHandle{}, mapAcceptanceError(err)
	}
	if input.QbConnectionID != "" {
		p.args.QbittorrentConnectionID = sqlString(input.QbConnectionID)
	}
	p.args.MaxAttempts = sqlInt(int64(input.MaxAttempts))
	if a.mode == "automatic_source" {
		p.args.EntryKind = sqlString("automatic")
	} else {
		p.args.EntryKind = sqlString("subscription_add")
	}
	return PreparedHandle{owner: a, tx: tx, prepared: p}, nil
}
func (a *entryAcceptor) accept(ctx context.Context, scope txscope.Tx, p PreparedHandle) (PendingHandle, error) {
	if a == nil || p.owner != a || p.tx == nil || scope.SQLTx() != p.tx {
		return PendingHandle{}, acceptError(500, "INTERNAL_INVARIANT", "prepared_scope_mismatch", nil)
	}
	if p.found {
		return PendingHandle{owner: a, tx: p.tx, pending: p.replay}, nil
	}
	result, err := a.service.AcceptNativeTx(ctx, scope, p.prepared)
	if err != nil {
		return PendingHandle{}, err
	}
	return PendingHandle{owner: a, tx: p.tx, pending: result}, nil
}

// Committed must be called only after the owning coordinator's Commit succeeds.
func (a *entryAcceptor) committed(p PendingHandle) (CommittedAcceptance, error) {
	if a == nil || p.owner != a || p.tx == nil {
		return CommittedAcceptance{}, acceptError(500, "INTERNAL_INVARIANT", "pending_owner_mismatch", nil)
	}
	return committedAcceptance(p.pending), nil
}
