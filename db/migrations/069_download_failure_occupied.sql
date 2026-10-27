-- Forward-only migration 069: the occupied download failure category (owner
-- ruling 2026-10-27).
--
-- qBittorrent keeps one task per info hash. A submission whose torrent qB
-- already holds under another task (no matching operation tag) is no longer
-- sent, or, when the add was sent and refused, is attributed to that task; the
-- download task fails with failure_code downloader_hash_occupied. The download
-- failed notification's result.category maps that code to the new value
-- occupied so the default templates can say why; every other code keeps its
-- category. CREATE OR REPLACE keeps the function's privileges.

CREATE OR REPLACE FUNCTION download_failure_category(p_code text) RETURNS text
    LANGUAGE sql IMMUTABLE
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 SELECT CASE
  WHEN p_code IN ('DOWNLOAD_STALLED','DOWNLOAD_PROGRESS_UNVERIFIABLE') THEN 'stalled'
  WHEN p_code IN ('SYNC_BUDGET_EXHAUSTED','SUBMIT_BUDGET_EXHAUSTED') THEN 'timeout'
  WHEN p_code IN ('qb_provider_failed','qb_sync_failed') THEN 'client'
  WHEN p_code IN ('SYNC_ERROR_BUDGET_EXHAUSTED','DOWNLOAD_OBSERVATION_UNAVAILABLE') THEN 'unobservable'
  WHEN p_code = 'downloader_hash_occupied' THEN 'occupied'
  ELSE 'submit' END
$$;
