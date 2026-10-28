-- Forward-only migration 096: instance rows carry the seasonless episode key.
--
-- Episode identity is the instance plus the episode number (095). Instance
-- rows written before the change still carry the v1 key tv:s<S>:e<N>; this
-- migration rewrites them to tv:e<N> once, so a new file and a stored file of
-- the same episode compare equal. Sealed rows keep their v1 keys: acquisition
-- inputs (acquisitions.input and its canonical bytes), file facts (checked
-- against the input by filefact_source_relation_valid) and replacement_deletions;
-- digests and idempotency keys are not recomputed. No library file is moved or deleted and no subscription setting
-- changes.
--
-- 1. Keep and exclude. Under v1 an instance could hold active files of more
--    than one season (the TMDB season was rebound between them). Rewriting
--    them all would make two seasons' episode N one episode and let
--    replacement delete one of them. One reference season is kept per
--    instance: the bound TMDB season as v1 keyed it (season 0 keyed s1), or,
--    for an unbound instance, the season with the most active placements (a
--    tie goes to the larger season). An active placement with a key of any
--    other season keeps its v1 keys and becomes not replacement-eligible: it
--    stays on disk and listed, but is never pooled, never deleted by
--    replacement and never counts as coverage, and nothing makes it eligible
--    again. Its version moves, so a decision that captured it goes stale; a
--    live offset migration of it follows the new placement version and keeps
--    its v1 keys. One activity entry per affected subscription records the
--    count.
--
-- 2. Rewrite. Every other v1 key in instance rows becomes tv:e<N>: placement
--    coverage, candidate facts (the unread facts.season is dropped), media
--    fact snapshots and their files, scan observations, replacement decisions
--    and offset migration plans. Rows of an excluded placement keep their v1
--    keys.
--
-- A fresh database has no v1 keys and no active placement, so every statement
-- matches nothing. A second run finds no eligible placement with a v1 key and
-- no row to rewrite.

WITH placement_seasons AS (
    SELECT DISTINCT p.id AS placement_id, p.managed_anime_id, p.version,
           substring(c.observation_key FROM '^tv:s([0-9]+):e')::integer AS season
    FROM placements p
    JOIN placement_coverage c ON c.placement_id = p.id
    WHERE p.state = 'active'
      AND p.verification_state = 'verified'
      AND p.replacement_eligible
      AND c.observation_key ~ '^tv:s[0-9]+:e'
),
season_counts AS (
    SELECT managed_anime_id, season, count(DISTINCT placement_id) AS placements
    FROM placement_seasons
    GROUP BY managed_anime_id, season
),
reference AS (
    SELECT DISTINCT ON (sc.managed_anime_id) sc.managed_anime_id,
           CASE WHEN m.tmdb_work_id IS NOT NULL THEN GREATEST(m.tmdb_season, 1) ELSE sc.season END AS season
    FROM season_counts sc
    LEFT JOIN anime_metadata m ON m.managed_anime_id = sc.managed_anime_id
    ORDER BY sc.managed_anime_id, sc.placements DESC, sc.season DESC
),
excluded AS (
    SELECT DISTINCT s.placement_id, s.managed_anime_id, s.version, r.season AS reference_season
    FROM placement_seasons s
    JOIN reference r ON r.managed_anime_id = s.managed_anime_id
    WHERE s.season <> r.season
),
followed_plans AS (
    UPDATE offset_migration_plans op
    SET placement_version = op.placement_version + 1
    FROM excluded e
    WHERE op.placement_id = e.placement_id
      AND op.placement_version = e.version
      AND op.state IN ('planned', 'ready', 'running', 'waiting_retry', 'effect_unknown')
    RETURNING op.id
),
excluded_placements AS (
    UPDATE placements p
    SET replacement_eligible = false, version = p.version + 1, updated_at = CURRENT_TIMESTAMP
    FROM excluded e
    WHERE p.id = e.placement_id
    RETURNING p.id, p.managed_anime_id
),
affected AS (
    SELECT x.managed_anime_id, count(*) AS placements, min(e.reference_season) AS reference_season
    FROM excluded_placements x
    JOIN excluded e ON e.placement_id = x.id
    GROUP BY x.managed_anime_id
)
INSERT INTO subscription_log_entries(
    id, subscription_id, managed_anime_id, kind, outcome, message_code, message_params,
    details, dedupe_key, reason_code, subject_type, subject_id, subject_version)
SELECT gen_random_uuid()::text, s.id, a.managed_anime_id, 'placement_changed', 'info',
       'placements_excluded', jsonb_build_object('count', a.placements),
       jsonb_build_object('reference_season', a.reference_season),
       'placement_changed:subscription:' || s.id || ':v' || s.version || ':seasonless_key_rewrite',
       'placements_excluded', 'subscription', s.id, s.version
FROM affected a
JOIN subscriptions s ON s.managed_anime_id = a.managed_anime_id
ON CONFLICT (subscription_id, dedupe_key) DO NOTHING;

-- An excluded placement is active, verified and not replacement-eligible;
-- the only other ineligible placements (unknown files) are released when
-- written. Every rewrite below skips the rows of an excluded placement.

UPDATE placement_coverage c
SET observation_key = regexp_replace(c.observation_key, '^tv:s[0-9]+:e', 'tv:e')
FROM placements p
WHERE p.id = c.placement_id
  AND c.observation_key ~ '^tv:s[0-9]+:e'
  AND NOT (p.state = 'active' AND p.verification_state = 'verified' AND NOT p.replacement_eligible);

UPDATE offset_migration_plans op
SET old_coverage_key = regexp_replace(op.old_coverage_key, '^tv:s[0-9]+:e', 'tv:e'),
    new_coverage_key = regexp_replace(op.new_coverage_key, '^tv:s[0-9]+:e', 'tv:e'),
    old_identity = regexp_replace(op.old_identity::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb,
    new_identity = regexp_replace(op.new_identity::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb,
    target_contract = regexp_replace(op.target_contract::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb
WHERE (op.old_coverage_key ~ '^tv:s[0-9]+:e'
       OR op.new_coverage_key ~ '^tv:s[0-9]+:e'
       OR op.old_identity::text ~ '"tv:s[0-9]+:e'
       OR op.new_identity::text ~ '"tv:s[0-9]+:e'
       OR op.target_contract::text ~ '"tv:s[0-9]+:e')
  AND NOT EXISTS (
      SELECT 1 FROM placements p
      WHERE p.id = op.placement_id
        AND p.state = 'active' AND p.verification_state = 'verified' AND NOT p.replacement_eligible);

UPDATE reconciliation_files r
SET coverage = regexp_replace(r.coverage::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb,
    facts = regexp_replace(r.facts::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb
WHERE (r.coverage::text ~ '"tv:s[0-9]+:e' OR r.facts::text ~ '"tv:s[0-9]+:e')
  AND NOT EXISTS (
      SELECT 1 FROM placements p
      WHERE p.id = r.facts->>'placement_id'
        AND p.state = 'active' AND p.verification_state = 'verified' AND NOT p.replacement_eligible);

UPDATE replacement_decisions
SET coverage_key = regexp_replace(coverage_key, '^tv:s[0-9]+:e', 'tv:e'),
    coverage_keys = regexp_replace(coverage_keys::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb,
    pools = regexp_replace(pools::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb
WHERE coverage_key ~ '^tv:s[0-9]+:e'
   OR coverage_keys::text ~ '"tv:s[0-9]+:e'
   OR pools::text ~ '"tv:s[0-9]+:e';

UPDATE release_candidate_facts
SET coverage_keys = regexp_replace(coverage_keys::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb,
    facts = facts - 'season'
WHERE coverage_keys::text ~ '"tv:s[0-9]+:e' OR facts ? 'season';

UPDATE media_fact_snapshots
SET release_identity = regexp_replace(release_identity::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb,
    release_facts = regexp_replace(release_facts::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb
WHERE release_identity::text ~ '"tv:s[0-9]+:e' OR release_facts::text ~ '"tv:s[0-9]+:e';

UPDATE media_fact_files
SET effective_facts = regexp_replace(effective_facts::text, '"tv:s[0-9]+:e', '"tv:e', 'g')::jsonb
WHERE effective_facts::text ~ '"tv:s[0-9]+:e';
