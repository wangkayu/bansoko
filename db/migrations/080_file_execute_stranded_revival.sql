-- Forward-only migration 080: re-open file_execute work stranded by a
-- writer-less retry at its attempt budget.
--
-- Before this release every retry of the typed file lane (a rolled-back
-- finish of a closed proof such as the notification proof mismatch fixed by
-- 070, a transient proof capture or publication fault, a preparation that
-- overran its attempt deadline) was an ordinary retry without an owner writer.
-- At the attempt budget that retry archived the WorkItem as failed and wrote
-- nothing to the operation: the operation stayed pending at the version the
-- work carried, its acquisition stayed running, and no lane could reach it
-- again. The typed lane now quarantines such a claim for the recovery lane
-- instead (RetryUntilResolved); this function moves an already stranded item
-- to that same state.
--
-- A publication proof is bound to its WorkItem id (the finish gates and the
-- convergence read compare publication_work_item_id with the claimed item) and
-- a WorkItem id is used once, so a new item could neither finish a closed
-- proof nor see unclosed evidence: the failed outcome itself is moved back to
-- the live queue under its own id, recovery-blocked with a new fencing token.
-- The recovery observation converges it from the evidence and never starts an
-- effect: a closed proof finishes through the fixed entry (which re-validates
-- every gate), unclosed evidence or no evidence ends the operation visibly.
--
-- Stranded means: the operation is pending/running, its acquisition is
-- running, it has no live work, and its latest work outcome failed while the
-- operation still carries that work's owner version (every failure that wrote
-- its owner advanced the operation version or terminalized it). Every
-- condition is re-checked under the root-first locks; anything else returns a
-- refusal code and writes nothing:
--   operation_missing / operation_terminal / root_not_running / live_work /
--   work_outcome_missing / work_not_failed / owner_version_moved /
--   work_not_proof_bound (the evidence belongs to another work) /
--   one_shot (a one-shot retry at the budget already quarantines instead of
--   failing, so a failed one-shot item has another cause and stays untouched).
CREATE FUNCTION file_execute_revive_stranded_work(p_operation text) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
DECLARE root_id text; root_state text; o public.file_operations; w public.work_item_outcomes;
BEGIN
 SELECT acquisition_id INTO root_id FROM public.file_operations WHERE id=p_operation;
 IF NOT FOUND THEN RETURN 'operation_missing'; END IF;
 SELECT state INTO STRICT root_state FROM public.acquisitions WHERE id=root_id FOR UPDATE;
 SELECT * INTO STRICT o FROM public.file_operations WHERE id=p_operation FOR UPDATE;
 IF o.state NOT IN ('pending','running') THEN RETURN 'operation_terminal'; END IF;
 IF root_state<>'running' THEN RETURN 'root_not_running'; END IF;
 IF EXISTS(SELECT 1 FROM public.work_items WHERE owner_type='file_operation' AND owner_id=o.id AND kind='file_execute') THEN
  RETURN 'live_work'; END IF;
 SELECT * INTO w FROM public.work_item_outcomes
  WHERE owner_type='file_operation' AND owner_id=o.id AND kind='file_execute'
  ORDER BY terminal_at DESC, id DESC LIMIT 1 FOR UPDATE;
 IF NOT FOUND THEN RETURN 'work_outcome_missing'; END IF;
 IF w.state<>'failed' THEN RETURN 'work_not_failed'; END IF;
 IF w.expected_owner_version<>o.version THEN RETURN 'owner_version_moved'; END IF;
 IF o.publication_permit_id IS NOT NULL AND (o.publication_work_item_id IS DISTINCT FROM w.id
  OR o.publication_owner_version IS DISTINCT FROM o.version) THEN
  RETURN 'work_not_proof_bound'; END IF;
 IF w.input_snapshot ? 'one_shot' THEN RETURN 'one_shot'; END IF;
 DELETE FROM public.work_item_outcomes WHERE id=w.id;
 INSERT INTO public.work_items(id,owner_type,owner_id,kind,state,available_at,lease_until,fencing_token,
  idempotency_key,attempt,max_attempts,input_snapshot,expected_owner_version,last_error_code,last_error_reason,
  retention_days,created_at,updated_at,cancel_requested,external_effect_admitted_at,recovery_blocked,
  recovery_reason,acquisition_id)
 VALUES(w.id,w.owner_type,w.owner_id,w.kind,'available',CURRENT_TIMESTAMP,NULL,w.fencing_token+1,
  w.idempotency_key,w.attempt,w.max_attempts,w.input_snapshot,w.expected_owner_version,'FILE_WORK_REVIVED',
  'failed at the attempt budget without an owner result; re-opened for recovery',
  w.retention_days,w.created_at,CURRENT_TIMESTAMP,w.cancel_requested,w.external_effect_admitted_at,TRUE,
  CASE WHEN w.cancel_requested THEN 'cancel_requested' ELSE 'effect_unknown' END,w.acquisition_id);
 RETURN 'revived';
END $$;

REVOKE ALL ON FUNCTION public.file_execute_revive_stranded_work(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.file_execute_revive_stranded_work(text) TO bansoko_runtime;
