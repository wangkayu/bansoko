package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	target "bansoko/internal/acquisition/target"
	generated "bansoko/internal/acquisition/terminal/adapter/persistence/generated"
	protocol "bansoko/internal/acquisition/terminal/domain"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/persistence/txscope"
	"github.com/google/uuid"
	"github.com/sqlc-dev/pqtype"
)

// CommittedAcceptance is opaque: only the transaction coordinator constructs it
// after a successful Commit. Zero values and pending SQL results expose no receipt.
type CommittedAcceptance struct {
	receipt  string
	replayed bool
}

func (c CommittedAcceptance) Receipt() []byte { return []byte(c.receipt) }
func (c CommittedAcceptance) Replayed() bool  { return c.replayed }

// Service owns capture/preparation and the one accepting transaction. It has no
// HTTP adapter, caller-supplied Prepared JSON, SQL callbacks or commit hooks.
type Service struct {
	db        *sql.DB
	revisions *draft.Service
	limits    witnessLimits
}

func NewService(db *sql.DB, revisions *draft.Service, maxWitnessBytes, maxWitnessRows int) (*Service, error) {
	if db == nil || revisions == nil || maxWitnessBytes <= 0 || maxWitnessRows <= 0 {
		return nil, acceptError(500, "INTERNAL_INVARIANT", "invalid_service_dependencies", nil)
	}
	return &Service{db: db, revisions: revisions, limits: witnessLimits{bytes: maxWitnessBytes, unionRows: maxWitnessRows}}, nil
}

// The intent/access constructors belong to the trusted application adapters in
// B2. The unexported values deliberately cannot be transport-assembled Prepared.
type acceptAccess struct{ scope string }
type requestIdentity struct{ namespace, key, hash, canonical string }
type acquisitionIntent struct {
	capture          qualificationRequest
	entryMode        string
	requiresExisting bool
	existingClaim    *protocol.Value
	// Exact frozen deployment/media values are read by the trusted entry adapter,
	// not supplied as client proof or a complete AcquisitionInput.
	media, delivery, limits protocol.Value
	title                   string
	submit                  *submitProjection
	evidence                *target.ParserEvidence
	connection              string
	production              bool
	replacements            []*ReplacementCapture
}
type preparedNative struct {
	args       generated.AcceptQualifiedQueuedParams
	varsRecord string
}
type pendingAcceptance struct {
	receipt  string
	replayed bool
}

type preparedOrReplay struct {
	prepared preparedNative
	replay   pendingAcceptance
	found    bool
}

func (s *Service) AcceptNative(ctx context.Context, a acceptAccess, r requestIdentity, i acquisitionIntent) (CommittedAcceptance, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommittedAcceptance{}, mapAcceptanceError(err)
	}
	defer tx.Rollback()
	p, err := s.PrepareNativeTx(ctx, txscope.Wrap(tx), a, r, i)
	if err != nil {
		return CommittedAcceptance{}, err
	}
	pending := p.replay
	if !p.found {
		if _, err = tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id=$1`, i.capture.releaseID); err != nil {
			return CommittedAcceptance{}, mapAcceptanceError(err)
		}
		pending, err = s.AcceptNativeTx(ctx, txscope.Wrap(tx), p.prepared)
		if err != nil {
			return CommittedAcceptance{}, mapAcceptanceError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return CommittedAcceptance{}, acceptError(503, "ACCEPTANCE_UNKNOWN", "commit_not_confirmed", err)
	}
	return committedAcceptance(pending), nil
}
func committedAcceptance(p pendingAcceptance) CommittedAcceptance {
	return CommittedAcceptance{receipt: p.receipt, replayed: p.replayed}
}

func (s *Service) PrepareNativeTx(ctx context.Context, scope txscope.Tx, a acceptAccess, r requestIdentity, i acquisitionIntent) (preparedOrReplay, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return preparedOrReplay{}, acceptError(500, "INTERNAL_INVARIANT", "invalid_txscope", nil)
	}
	if i.requiresExisting || i.existingClaim != nil {
		return preparedOrReplay{}, existingUnsupported()
	}
	if a.scope == "" || r.key == "" || !validMaterialDigest(r.hash) || (r.namespace != "manual_acquire_v1" && r.namespace != "auto_acquire_v1") {
		return preparedOrReplay{}, acceptError(400, "INPUT_INVALID", "request_identity_invalid", nil)
	}
	v, err := protocol.ParseStrict([]byte(r.canonical))
	if err != nil || v.Kind != protocol.KindObject {
		return preparedOrReplay{}, acceptError(400, "INPUT_INVALID", "request_invalid", err)
	}
	if replay, found, err := replayTx(ctx, tx, a, r); err != nil || found {
		return preparedOrReplay{replay: replay, found: found}, mapAcceptanceError(err)
	}
	i.capture.requestHash = r.hash
	i.capture.entryMode = i.entryMode
	c, err := captureQualificationTx(ctx, tx, s.revisions, i.capture)
	if err != nil {
		return preparedOrReplay{}, mapAcceptanceError(err)
	}
	if i.production {
		projection, err := productionIntent(ctx, tx, c, i.entryMode, i.connection, false, i.replacements...)
		if err != nil {
			return preparedOrReplay{}, mapAcceptanceError(err)
		}
		i.media, i.delivery, i.limits, i.title = projection.media, projection.delivery, projection.limits, projection.title
	}
	var evidence []target.ParserEvidence
	if i.evidence != nil {
		evidence = append(evidence, *i.evidence)
	}
	e, err := evaluateAdmission(ctx, c, i, evidence...)
	if err != nil {
		return preparedOrReplay{}, mapAcceptanceError(err)
	}
	p, err := prepareAdmission(c, e, i, a, r, s.limits)
	if err != nil {
		return preparedOrReplay{}, mapAcceptanceError(err)
	}
	return preparedOrReplay{prepared: p}, nil
}
func (s *Service) AcceptNativeTx(ctx context.Context, scope txscope.Tx, p preparedNative) (pendingAcceptance, error) {
	return acceptNativeTx(ctx, scope, p)
}

func replayTx(ctx context.Context, tx *sql.Tx, a acceptAccess, r requestIdentity) (pendingAcceptance, bool, error) {
	var hash string
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT request_hash,admission_receipt FROM acquisitions WHERE access_scope=$1 AND request_namespace=$2 AND request_key=$3`, a.scope, r.namespace, r.key).Scan(&hash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return pendingAcceptance{}, false, nil
	}
	if err != nil {
		return pendingAcceptance{}, false, err
	}
	if hash != r.hash {
		return pendingAcceptance{}, true, acceptError(409, "IDEMPOTENCY_KEY_REUSED", "idempotency_key_reused", nil)
	}
	var consistent bool
	if err := tx.QueryRowContext(ctx, `SELECT vars_evidence_consistent(id) FROM acquisitions WHERE access_scope=$1 AND request_namespace=$2 AND request_key=$3`, a.scope, r.namespace, r.key).Scan(&consistent); err != nil {
		return pendingAcceptance{}, true, err
	}
	if !consistent {
		return pendingAcceptance{}, true, acceptError(500, "INTERNAL_INVARIANT", "VARS_EVALUATION_EVIDENCE_INCONSISTENT", nil)
	}
	v, err := protocol.ParseStrict(raw)
	if err == nil {
		err = protocol.Validate("AcceptReceiptV1", v)
	}
	if err != nil {
		return pendingAcceptance{}, true, acceptError(500, "INTERNAL_INVARIANT", "stored_receipt_invalid", err)
	}
	return pendingAcceptance{receipt: string(protocol.Canonical(v)), replayed: true}, true, nil
}
func (s *Service) commitPrepared(ctx context.Context, p preparedNative) (CommittedAcceptance, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommittedAcceptance{}, mapAcceptanceError(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE releases SET verdict='accepted_pending_download' WHERE id=$1`, p.args.SourceReleaseID.String); err != nil {
		return CommittedAcceptance{}, mapAcceptanceError(err)
	}
	pending, err := acceptNativeTx(ctx, txscope.Wrap(tx), p)
	if err != nil {
		return CommittedAcceptance{}, mapAcceptanceError(err)
	}
	if err := tx.Commit(); err != nil {
		return CommittedAcceptance{}, acceptError(503, "ACCEPTANCE_UNKNOWN", "commit_not_confirmed", err)
	}
	return CommittedAcceptance{receipt: pending.receipt, replayed: pending.replayed}, nil
}
func acceptNativeTx(ctx context.Context, scope txscope.Tx, p preparedNative) (pendingAcceptance, error) {
	return acceptNativeTxWithDefaults(ctx, scope, p, defaultSectionDirectory)
}

func acceptNativeTxWithDefaults(ctx context.Context, scope txscope.Tx, p preparedNative, directory func() (json.RawMessage, error)) (pendingAcceptance, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return pendingAcceptance{}, acceptError(500, "INTERNAL_INVARIANT", "invalid_txscope", nil)
	}
	defaults, err := directory()
	if err != nil {
		return pendingAcceptance{}, acceptError(500, "INTERNAL_INVARIANT", "default_directory_invalid", err)
	}
	p.args.DefaultSections = sqlJSON(string(defaults))
	p.args.VarsEvaluation = sql.NullString{String: p.varsRecord, Valid: p.varsRecord != ""}
	raw, err := generated.New(tx).AcceptQualifiedQueued(ctx, p.args)
	if err != nil {
		return pendingAcceptance{}, mapAcceptanceError(err)
	}
	var result struct {
		Status  string          `json:"status"`
		Receipt json.RawMessage `json:"admission_receipt"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return pendingAcceptance{}, acceptError(500, "INTERNAL_INVARIANT", "sql_accept_result_invalid", err)
	}
	if result.Status != "accepted" && result.Status != "replayed" {
		return pendingAcceptance{}, acceptError(500, "INTERNAL_INVARIANT", "invalid_accept_result", nil)
	}
	receipt, err := protocol.ParseStrict(result.Receipt)
	if err == nil {
		err = protocol.Validate("AcceptReceiptV1", receipt)
	}
	if err != nil {
		return pendingAcceptance{}, acceptError(500, "INTERNAL_INVARIANT", "sql_receipt_invalid", err)
	}
	return pendingAcceptance{receipt: string(protocol.Canonical(receipt)), replayed: result.Status == "replayed"}, nil
}
func sqlString(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
func sqlJSON(v string) pqtype.NullRawMessage {
	return pqtype.NullRawMessage{RawMessage: json.RawMessage(v), Valid: true}
}
func sqlInt(v int64) sql.NullInt32 { return sql.NullInt32{Int32: int32(v), Valid: true} }
func newAcceptIDs() (string, string, string, string) {
	return uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
}
