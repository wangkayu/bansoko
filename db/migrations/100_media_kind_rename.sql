-- Forward-only migration 100: the owner media kind loses its "_override" suffix
-- (lane L7 of the subscription-current-config work).
--
-- anime_metadata.media_kind_override has been the anime's own media kind
-- (tv | movie, NULL is tv) since migration 005; the suffix said "override"
-- of a value nothing reads any more. The column becomes media_kind, and the
-- same word is used in the API, the code and the stored draft JSON. The
-- TMDB binding's tmdb_media_kind column and the binding object's own
-- media_kind key are different values and keep their names.
--
-- This migration changes no behaviour: the column keeps its rows and its
-- CHECK, and no stored function mentions the old name.

ALTER TABLE anime_metadata RENAME COLUMN media_kind_override TO media_kind;
ALTER TABLE anime_metadata RENAME CONSTRAINT anime_metadata_media_kind_override_check TO anime_metadata_media_kind_check;

-- Stored candidate drafts carry the field as metadata.media_kind_override. The
-- typed readback would drop the old key (and so clear the value on the next
-- save), so the key is translated. The candidate guard refuses a linked draft
-- rewrite, so the migration-only fixture switch lets this one shape through;
-- the defaults trigger recomputes draft_digest from the rewritten draft.
SET LOCAL bansoko.candidate_fixture = 'on';
UPDATE anime_config_candidates
SET draft = jsonb_set(draft, '{metadata}',
        (draft->'metadata') - 'media_kind_override'
        || jsonb_build_object('media_kind', draft->'metadata'->'media_kind_override')),
    draft_digest = ''
WHERE draft->'metadata' ? 'media_kind_override';
RESET bansoko.candidate_fixture;

-- Candidate revisions are append-only; their guard is lifted for this one
-- shape rewrite only. metadata is not part of the effective sections, so
-- effective_digest and every current_revision_digest mirror are unchanged. A
-- revision draft digest that was the canonical draft hash follows the
-- rewritten draft; any other recorded digest is left as it was.
ALTER TABLE anime_config_candidate_revisions DISABLE TRIGGER anime_config_candidate_revisions_guard_trg;
WITH rewritten AS (
    SELECT candidate_id, revision, draft_digest AS old_digest,
           'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(draft), 'UTF8'), 'sha256'), 'hex') AS old_canonical,
           jsonb_set(draft, '{metadata}',
               (draft->'metadata') - 'media_kind_override'
               || jsonb_build_object('media_kind', draft->'metadata'->'media_kind_override')) AS new_draft
    FROM anime_config_candidate_revisions
    WHERE draft->'metadata' ? 'media_kind_override'
)
UPDATE anime_config_candidate_revisions r
SET draft = rewritten.new_draft,
    draft_digest = CASE WHEN rewritten.old_digest = rewritten.old_canonical
        THEN 'sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(rewritten.new_draft), 'UTF8'), 'sha256'), 'hex')
        ELSE r.draft_digest END
FROM rewritten
WHERE r.candidate_id = rewritten.candidate_id AND r.revision = rewritten.revision;
ALTER TABLE anime_config_candidate_revisions ENABLE TRIGGER anime_config_candidate_revisions_guard_trg;

-- Replayed subscribe and metadata-match results are read back through the
-- typed metadata shape, which would show the old key as an unset kind.
UPDATE idempotency_records
SET result_snapshot = jsonb_set(result_snapshot, '{metadata}',
        (result_snapshot->'metadata') - 'media_kind_override'
        || jsonb_build_object('media_kind', result_snapshot->'metadata'->'media_kind_override'))
WHERE kind IN ('subscribe', 'metadata_match')
  AND result_snapshot->'metadata' ? 'media_kind_override';
UPDATE idempotency_records
SET result_snapshot = (result_snapshot - 'media_kind_override')
        || jsonb_build_object('media_kind', result_snapshot->'media_kind_override')
WHERE kind IN ('subscribe', 'metadata_match')
  AND result_snapshot ? 'media_kind_override';

-- One-shot input snapshots keep the draft the user submitted for readback; the
-- draft's metadata key is translated and the snapshot's own draft_digest
-- follows the rewritten draft. input_digest and request_hash stay as they
-- were admitted: they are the replay identity, so a retry of the same
-- idempotency key after the upgrade is answered 409 rather than replayed.
UPDATE one_shot_acquisitions
SET input_snapshot = jsonb_set(
        jsonb_set(input_snapshot, '{draft,metadata}',
            (input_snapshot->'draft'->'metadata') - 'media_kind_override'
            || jsonb_build_object('media_kind', input_snapshot->'draft'->'metadata'->'media_kind_override')),
        '{draft_digest}',
        to_jsonb('sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(
            jsonb_set(input_snapshot->'draft', '{metadata}',
                (input_snapshot->'draft'->'metadata') - 'media_kind_override'
                || jsonb_build_object('media_kind', input_snapshot->'draft'->'metadata'->'media_kind_override'))
        ), 'UTF8'), 'sha256'), 'hex')))
WHERE input_snapshot->'draft'->'metadata' ? 'media_kind_override'
  AND input_snapshot ? 'draft_digest';
UPDATE one_shot_acquisitions
SET input_snapshot = jsonb_set(input_snapshot, '{draft,metadata}',
        (input_snapshot->'draft'->'metadata') - 'media_kind_override'
        || jsonb_build_object('media_kind', input_snapshot->'draft'->'metadata'->'media_kind_override'))
WHERE input_snapshot->'draft'->'metadata' ? 'media_kind_override';

UPDATE one_shot_execution_attempts
SET input_snapshot = jsonb_set(
        jsonb_set(input_snapshot, '{draft,metadata}',
            (input_snapshot->'draft'->'metadata') - 'media_kind_override'
            || jsonb_build_object('media_kind', input_snapshot->'draft'->'metadata'->'media_kind_override')),
        '{draft_digest}',
        to_jsonb('sha256:' || encode(digest(convert_to(anime_config_candidate_canonical_json(
            jsonb_set(input_snapshot->'draft', '{metadata}',
                (input_snapshot->'draft'->'metadata') - 'media_kind_override'
                || jsonb_build_object('media_kind', input_snapshot->'draft'->'metadata'->'media_kind_override'))
        ), 'UTF8'), 'sha256'), 'hex')))
WHERE input_snapshot->'draft'->'metadata' ? 'media_kind_override'
  AND input_snapshot ? 'draft_digest';
UPDATE one_shot_execution_attempts
SET input_snapshot = jsonb_set(input_snapshot, '{draft,metadata}',
        (input_snapshot->'draft'->'metadata') - 'media_kind_override'
        || jsonb_build_object('media_kind', input_snapshot->'draft'->'metadata'->'media_kind_override'))
WHERE input_snapshot->'draft'->'metadata' ? 'media_kind_override';

-- Every other frozen document keeps the bytes it was admitted with: acquisition
-- inputs, download-task frozen policy and activity payloads are identity-bearing
-- or never read back through the metadata shape.
