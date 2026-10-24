package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	protocol "bansoko/internal/acquisition/terminal/domain"
	"bansoko/internal/library/domain"
	"bansoko/internal/workitems"
)

// publicationReceiptBase reads the original identity independently of today's
// claim. Persisted steps are copied only to retain unchanged authorization facts.
func (s *Service) publicationReceiptBase(ctx context.Context, permit string) (map[string]any, protocol.Value, string, error) {
	var raw, objects []byte
	var dependency string
	err := s.db.QueryRowContext(ctx, `SELECT jsonb_build_object('identity',jsonb_build_object('schema_version',1,'permit_id',publication_permit_id,'root_id',acquisition_id,'original_work_id',publication_work_item_id,'fencing_token',publication_fencing_token,'executor_session',publication_executor_session,'spec_digest',publication_spec_digest),'emitter',jsonb_build_object('mode','dispatch_session','session',publication_executor_session),'payload',jsonb_build_object('checkpoint',publication_step_checkpoint,'closure',jsonb_build_object('state','unavailable','value',NULL,'reason_code','PROOF_UNAVAILABLE'))),publication_frozen_objects_canonical,publication_dependency_digest FROM public.file_operations WHERE publication_permit_id=$1`, permit).Scan(&raw, &objects, &dependency)
	if err != nil {
		return nil, protocol.Value{}, "", err
	}
	var base map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&base); err != nil {
		return nil, protocol.Value{}, "", err
	}
	payload := base["payload"].(map[string]any)
	cpRaw, err := json.Marshal(payload["checkpoint"])
	if err != nil {
		return nil, protocol.Value{}, "", err
	}
	cpRaw, err = normalizeCheckpointTimes(cpRaw)
	if err != nil {
		return nil, protocol.Value{}, "", err
	}
	var checkpoint map[string]any
	decoder = json.NewDecoder(bytes.NewReader(cpRaw))
	decoder.UseNumber()
	if err := decoder.Decode(&checkpoint); err != nil {
		return nil, protocol.Value{}, "", err
	}
	payload["checkpoint"] = checkpoint
	v, err := protocol.ParseStrict(objects)
	if err != nil {
		return nil, protocol.Value{}, "", err
	}
	return base, v, dependency, nil
}

func publicationEnvelope(base map[string]any) (protocol.Value, error) {
	raw, err := json.Marshal(base)
	if err != nil {
		return protocol.Value{}, err
	}
	v, err := protocol.ParseStrict(raw)
	if err != nil {
		return protocol.Value{}, err
	}
	if err := protocol.Validate("FileReceiptEnvelopeV1", v); err != nil {
		return protocol.Value{}, err
	}
	return v, nil
}

// typedPublicationCallbacks is intentionally internal. It does not expose a
// user supplied receipt route, and does not perform Finish from an OS callback.
func (s *Service) typedPublicationCallbacks(c workitems.Claim, permit string, op domain.FileOperation) domain.TypedPublicationCallbacks {
	type committedDeadline struct {
		at      time.Time
		session domain.PublicationSession
	}
	deadlines := map[string]committedDeadline{}
	grant := func(ctx context.Context, session domain.PublicationSession, step string) (bool, error) {
		ok, deadline, err := s.filePublicationGrantDeadline(ctx, c, permit, session, step)
		if ok && err == nil {
			deadlines[step] = committedDeadline{deadline, session}
		}
		return ok, err
	}
	return domain.TypedPublicationCallbacks{
		StartDispatch: func(ctx context.Context, session domain.PublicationSession) (bool, error) {
			return grant(ctx, session, "")
		},
		StartStep: func(ctx context.Context, session domain.PublicationSession, step string) (bool, error) {
			return grant(ctx, session, step)
		},
		CheckMutationDeadline: func(ctx context.Context, session domain.PublicationSession, step string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			deadline, ok := deadlines[step]
			if !ok || session != deadline.session || session.ExecutorSession != session.BootNonce+":"+session.CallNonce || session.BootNonce == "" || session.CallNonce == "" || !time.Now().Before(deadline.at) {
				return fmt.Errorf("FILE_GRANT_DEADLINE_UNAVAILABLE")
			}
			return nil
		},
		RecordStep: func(ctx context.Context, session domain.PublicationSession, r domain.PublicationStepReceipt) error {
			base, objects, dependency, err := s.publicationReceiptBase(ctx, permit)
			if err != nil {
				return err
			}
			if base["identity"].(map[string]any)["executor_session"] != session.ExecutorSession {
				return errors.New("receipt session differs from original invocation")
			}
			at := observationTime(r.ObservedAt)
			slot := map[string]any{"state": "unknown", "proof": nil, "observed_at": at, "reason_code": "FILE_EXECUTION_UNCERTAIN"}
			if r.Err == nil {
				proof, err := publicationStepProof(base, objects, dependency, op, r)
				if err != nil {
					return err
				}
				slot = map[string]any{"state": "confirmed", "proof": proof, "observed_at": at, "reason_code": nil}
			}
			base["observed_at"] = at
			base["payload"].(map[string]any)["checkpoint"].(map[string]any)[r.Step] = slot
			envelope, err := publicationEnvelope(base)
			if err != nil {
				return err
			}
			return s.recordFileReceipt(ctx, permit, envelope)
		},
		Joined: func(ctx context.Context, j domain.PublicationJoin) error {
			if len(j.CoveredSteps) == 0 {
				return nil
			} // no local mutation channel was issued
			base, _, _, err := s.publicationReceiptBase(ctx, permit)
			if err != nil {
				return err
			}
			if base["identity"].(map[string]any)["executor_session"] != j.Session.ExecutorSession {
				return errors.New("join session differs from original invocation")
			}
			at := observationTime(j.JoinedAt)
			base["observed_at"] = at
			base["payload"].(map[string]any)["closure"] = frozenKnown(map[string]any{"kind": "local_join", "executor_session": j.Session.ExecutorSession, "boot_nonce": j.Session.BootNonce, "call_nonce": j.Session.CallNonce, "covered_steps": j.CoveredSteps, "joined_at": at, "observer_version": j.ObserverVersion})
			envelope, err := publicationEnvelope(base)
			if err != nil {
				return err
			}
			return s.recordFileReceipt(ctx, permit, envelope)
		},
	}
}
