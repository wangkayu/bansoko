-- Forward-only migration 143: upgrade stored business snapshots to
-- business-snapshot-v2.
--
-- The v2 shape retires the title-attribute program reference: a v1 snapshot
-- carries programs.attrrules next to programs.vars and the
-- 'business-snapshot-v1' schema string. The evaluator freezes only the v2
-- shape, so a v1 owner row fails every automatic admission with
-- INPUT_STALE: capture_material_unavailable while manual one-shot entries
-- (which never read the owner snapshot) keep working. The sections, the
-- parser envelope and the replacement document are unchanged between the two
-- shapes; the upgrade therefore drops the retired program reference, sets the
-- schema string and recomputes the canonical digest.
--
-- Linked (dormant) candidates are history: no admission reads their draft, so
-- they keep their evidence bytes. Working candidates and their revisions stay
-- editable/activatable and are upgraded with the owner rows.

CREATE FUNCTION pg_temp.business_snapshot_v2(document jsonb) RETURNS jsonb
    LANGUAGE sql IMMUTABLE
    AS $$
 SELECT jsonb_set(jsonb_set(document, '{programs}', (document->'programs') - 'attrrules'),
                   '{schema_version}', to_jsonb('business-snapshot-v2'::text))
$$;

CREATE FUNCTION pg_temp.business_snapshot_v2_digest(document jsonb) RETURNS text
    LANGUAGE sql IMMUTABLE
    AS $$
 SELECT 'sha256:' || encode(digest(convert_to(public.anime_config_candidate_canonical_json(document), 'UTF8'), 'sha256'), 'hex')
$$;

UPDATE public.anime_business_configs
SET business_snapshot = pg_temp.business_snapshot_v2(business_snapshot),
    business_digest = pg_temp.business_snapshot_v2_digest(pg_temp.business_snapshot_v2(business_snapshot)),
    business_version = business_version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE business_snapshot IS NOT NULL
  AND business_snapshot->>'schema_version' IS DISTINCT FROM 'business-snapshot-v2';

WITH fixed AS (
 SELECT c.id,
        pg_temp.business_snapshot_v2(c.draft->'business') AS business
 FROM public.anime_config_candidates c
 WHERE c.activated_managed_anime_id IS NULL
   AND c.draft->'business'->>'schema_version' = 'business-snapshot-v1'
), drafts AS (
 SELECT f.id,
        CASE WHEN c.draft ? 'business_digest'
             THEN jsonb_set(jsonb_set(c.draft, '{business}', f.business),
                            '{business_digest}', to_jsonb(pg_temp.business_snapshot_v2_digest(f.business)))
             ELSE jsonb_set(c.draft, '{business}', f.business)
        END AS draft
 FROM fixed f JOIN public.anime_config_candidates c ON c.id = f.id
)
UPDATE public.anime_config_candidates c
SET draft = d.draft,
    draft_digest = pg_temp.business_snapshot_v2_digest(d.draft)
FROM drafts d
WHERE c.id = d.id;

-- Candidate revisions are immutable evidence. A schema upgrade is not a
-- business edit: the revision guard is suspended for this one statement, the
-- same way migration 081 suspends the work item retirement fence.
ALTER TABLE public.anime_config_candidate_revisions DISABLE TRIGGER anime_config_candidate_revisions_guard_trg;
WITH fixed AS (
 SELECT r.candidate_id, r.revision,
        CASE WHEN r.business_snapshot IS NOT NULL THEN pg_temp.business_snapshot_v2(r.business_snapshot) END AS business
 FROM public.anime_config_candidate_revisions r
 JOIN public.anime_config_candidates c ON c.id = r.candidate_id
 WHERE c.activated_managed_anime_id IS NULL
   AND (r.business_snapshot->>'schema_version' = 'business-snapshot-v1'
        OR r.draft->'business'->>'schema_version' = 'business-snapshot-v1')
), drafted AS (
 SELECT f.candidate_id, f.revision, f.business,
        CASE WHEN r.draft->'business'->>'schema_version' = 'business-snapshot-v1'
             THEN jsonb_set(
                 jsonb_set(r.draft, '{business}', pg_temp.business_snapshot_v2(r.draft->'business')),
                 '{business_digest}',
                 CASE WHEN r.draft ? 'business_digest'
                      THEN to_jsonb(pg_temp.business_snapshot_v2_digest(pg_temp.business_snapshot_v2(r.draft->'business')))
                 END)
             ELSE r.draft
        END AS draft
 FROM fixed f
 JOIN public.anime_config_candidate_revisions r ON r.candidate_id = f.candidate_id AND r.revision = f.revision
)
UPDATE public.anime_config_candidate_revisions r
SET draft = d.draft,
    draft_digest = pg_temp.business_snapshot_v2_digest(d.draft),
    business_snapshot = d.business,
    business_digest = CASE WHEN d.business IS NOT NULL THEN pg_temp.business_snapshot_v2_digest(d.business) END
FROM drafted d
WHERE r.candidate_id = d.candidate_id AND r.revision = d.revision;
ALTER TABLE public.anime_config_candidate_revisions ENABLE TRIGGER anime_config_candidate_revisions_guard_trg;

DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM public.anime_business_configs
            WHERE business_snapshot IS NOT NULL
              AND business_snapshot->>'schema_version' IS DISTINCT FROM 'business-snapshot-v2') THEN
  RAISE EXCEPTION 'BUSINESS_SNAPSHOT_V2_INCOMPLETE: an owner business snapshot is not v2';
 END IF;
 IF EXISTS (SELECT 1 FROM public.anime_config_candidates
            WHERE activated_managed_anime_id IS NULL
              AND draft->'business'->>'schema_version' IS DISTINCT FROM 'business-snapshot-v2') THEN
  RAISE EXCEPTION 'BUSINESS_SNAPSHOT_V2_INCOMPLETE: a working candidate draft is not v2';
 END IF;
 IF EXISTS (SELECT 1 FROM public.anime_config_candidate_revisions r
            JOIN public.anime_config_candidates c ON c.id = r.candidate_id
            WHERE c.activated_managed_anime_id IS NULL
              AND r.business_snapshot IS NOT NULL
              AND r.business_snapshot->>'schema_version' IS DISTINCT FROM 'business-snapshot-v2') THEN
  RAISE EXCEPTION 'BUSINESS_SNAPSHOT_V2_INCOMPLETE: a working candidate revision is not v2';
 END IF;
END $$;
