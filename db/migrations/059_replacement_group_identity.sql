-- Forward-only migration 059: replacement group identity and scan-managed
-- files.
--
-- 1. A custom RSS source is its own group identity. per_group pools by
--    coverage key plus the stable group identity: the frozen
--    compare_values.subgroup (provider, group_ref) that subgroup.score also
--    reads, never the bracketed title group. A provider-group source already
--    froze (provider, provider_group_ref); a custom RSS source froze no
--    subgroup. Acceptance now freezes (custom, <rss_source_id>) for it, so a
--    custom source pools on its own and ranks lowest on subgroup.score (it
--    never has a provider-group score). Candidate facts frozen before this
--    migration are backfilled in place; rows without a compare-values-v1
--    document stay as they are (they are re-projected lazily).
--
-- 2. A scan-managed file is a managed file. The library scan admits a file it
--    finds as an active placement (source evidence 'reconciliation'); such a
--    placement now takes part in replacement like every managed placement:
--    with no group and no comparison values until a release claims it, so any
--    candidate beats it, and as a loser it is deleted like any managed loser.
--    Existing scan-managed placements become replacement-eligible.

UPDATE release_candidate_facts f
SET compare_values = jsonb_set(
        f.compare_values,
        '{subgroup}',
        jsonb_build_object('provider', 'custom', 'group_ref', src.id))
FROM releases r
JOIN rss_sources src ON src.id = r.rss_source_id
WHERE r.id = f.release_id
  AND src.source_kind = 'custom_rss'
  AND f.compare_values->>'version' = 'compare-values-v1'
  AND NOT (f.compare_values ? 'subgroup');

UPDATE placements
SET replacement_eligible = true, version = version + 1, updated_at = CURRENT_TIMESTAMP
WHERE source_evidence_type = 'reconciliation' AND NOT replacement_eligible;
