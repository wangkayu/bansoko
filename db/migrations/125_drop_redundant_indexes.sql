-- Forward-only migration 125: six redundant indexes go.
--
-- Five duplicate a unique constraint's btree exactly (a fixed ASC prefix with
-- a DESC tail also scans the same btree backwards), and release_candidate_facts
-- duplicates the primary key itself:
--   provider_catalog_snapshots_latest_idx      <= UNIQUE (provider, season, version)
--   provider_group_snapshots_latest_idx        <= UNIQUE (provider, catalog_entry_ref, version)
--   provider_feed_snapshots_latest_idx         <= UNIQUE (provider, provider_entry_ref, provider_group_ref, version)
--   release_candidate_facts_release_idx        <= PRIMARY KEY (release_id, release_version)
--   reconciliation_files_gen_idx               <= PRIMARY KEY prefix (managed_anime_id, generation)
-- The sixth keys a single-value column: hash_schema is CHECK-pinned to one
-- value, no query filters on it, and feed retention keeps its own
-- (retention_until, id) index. Dropping them removes write amplification with
-- no query-plan cost.

DROP INDEX public.provider_catalog_snapshots_latest_idx;
DROP INDEX public.provider_group_snapshots_latest_idx;
DROP INDEX public.provider_feed_snapshots_latest_idx;
DROP INDEX public.provider_feed_snapshots_hash_schema_idx;
DROP INDEX public.release_candidate_facts_release_idx;
DROP INDEX public.reconciliation_files_gen_idx;
