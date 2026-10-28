package application

import (
	target "bansoko/internal/acquisition/target"
	anime "bansoko/internal/anime/domain"
	draft "bansoko/internal/anime/draft/application"
	"bansoko/internal/filekinds"
	"bansoko/internal/persistence/txscope"
	"bansoko/internal/workitems"
	"context"
	"database/sql"
	"encoding/json"
)

type OneShotTask struct {
	EntryTask
	RequestKey  string
	CandidateID string
	Revision    int64
	Draft       anime.AnimeDraft
	Business    anime.BusinessSnapshot
	RequestHash string
	OneShot     workitems.OneShotRef
	Evidence    target.ParserEvidence
	// FileSelection is the one-shot's explicit manual file selection (nil =
	// the product default); it replaces the first-level gate of the required
	// file check exactly like the frozen batch plan.
	FileSelection *filekinds.Selection
}
type OneShotAcceptor struct{ entry *entryAcceptor }

func NewOneShotAcceptor(db *sql.DB, store *workitems.Store) (*OneShotAcceptor, error) {
	revisions, err := draft.NewService(db, draft.Dependencies{WorkItems: store})
	if err != nil {
		return nil, err
	}
	s, err := NewService(db, revisions, 1<<20, 10000)
	if err != nil {
		return nil, err
	}
	return &OneShotAcceptor{entry: &entryAcceptor{service: s, mode: "one_shot_seed"}}, nil
}
func (a *OneShotAcceptor) Prepare(ctx context.Context, scope txscope.Tx, in OneShotTask) (PreparedHandle, error) {
	tx := scope.SQLTx()
	if tx == nil {
		return PreparedHandle{}, acceptError(500, "INTERNAL_INVARIANT", "invalid_txscope", nil)
	}
	access := acceptAccess{scope: "deployment_owner_v1"}
	raw, err := json.Marshal(map[string]any{"request_hash": in.RequestHash})
	if err != nil {
		return PreparedHandle{}, err
	}
	request := requestIdentity{namespace: "manual_acquire_v1", key: in.RequestKey, hash: in.RequestHash, canonical: string(raw)}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, access.scope+"\x1f"+request.namespace+"\x1f"+request.key); err != nil {
		return PreparedHandle{}, mapAcceptanceError(err)
	}
	req := qualificationRequest{entryMode: "one_shot_seed", configKind: "submitted", catalogKind: "release_input", releaseID: in.ReleaseID, submitted: &in.Business, submittedDraft: &in.Draft}
	if in.CandidateID != "" {
		req.configKind = "saved_revision"
		req.candidateID = in.CandidateID
		req.revision = in.Revision
	}
	intent := acquisitionIntent{entryMode: "one_shot_seed", capture: req, submit: &submitProjection{frozenInput: in.FrozenInput, frozenPolicy: in.FrozenPolicy, effectKey: "qb:" + in.ReleaseID, correlation: in.CorrelationID, causationType: "release", causationID: in.ReleaseID, trigger: "one_shot_acquire", oneShot: &in.OneShot}, evidence: &in.Evidence, connection: in.QbConnectionID, production: true}
	intent.replacements = in.Replacements
	intent.fileSelection = in.FileSelection
	p, err := a.entry.service.PrepareNativeTx(ctx, scope, access, request, intent)
	if err != nil {
		return PreparedHandle{}, err
	}
	if !p.found {
		p.prepared.args.QbittorrentConnectionID = sqlString(in.QbConnectionID)
		p.prepared.args.MaxAttempts = sqlInt(int64(in.MaxAttempts))
	}
	return PreparedHandle{owner: a.entry, tx: tx, prepared: p.prepared, replay: p.replay, found: p.found}, nil
}
func (a *OneShotAcceptor) Accept(ctx context.Context, scope txscope.Tx, p PreparedHandle) (PendingHandle, error) {
	return a.entry.accept(ctx, scope, p)
}
func (a *OneShotAcceptor) Committed(p PendingHandle) (CommittedAcceptance, error) {
	return a.entry.committed(p)
}
