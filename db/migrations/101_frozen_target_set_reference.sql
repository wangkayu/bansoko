-- Forward-only migration 101: frozen target facts name the stored target set
-- instead of carrying a copy of it.
--
-- Every target-planned admission stores its target set once, one row per
-- target in acquisition_targets (the planner, the file operation binding and
-- the plan capture read those rows; nothing reads a frozen copy). The frozen
-- carriers (one_shot_acquisitions.input_snapshot,
-- one_shot_execution_attempts.input_snapshot and download_tasks.frozen_input)
-- also embedded every target spec and observation under target_facts, so they
-- grew with the number of targets and a one-shot batch of a couple of dozen
-- episodes went over the 256 KiB snapshot bound. target_facts now keeps the
-- admission outcome, its reason, the set digest it always carried and the
-- number of targets; the targets and observations arrays are dropped.
--
-- Rows keep everything else as admitted. input_digest, request_hash and the
-- acquisition canonical_request are the replay identity and are not touched:
-- none of them is computed over target_facts, so replaying an idempotency key
-- answers exactly as before. download_tasks.version is not bumped: no worker
-- holds a frozen input across the upgrade (a worker reads it from the row it
-- claims, and download_freeze_path_snapshot compares that input with the
-- stored row), so in-flight tasks continue from the rewritten row. No trigger
-- is disabled: the one-shot tables carry none, and the download_tasks guards
-- (budget, progress, submission slot) check columns this rewrite leaves alone
-- on rows whose acquisition is full (compaction deletes a root's tasks). Every
-- rewritten document only shrinks, so its size bound holds.
--
-- download_tasks.frozen_input also kept a second copy of the release's
-- internal facts under selected_release.internal_facts, the parsed file list
-- included, next to the release_files the task reads. Nothing read that copy:
-- selected_release names the release by id and version, and its facts stay on
-- the releases row. It is dropped, so the frozen input carries the file list
-- once.
--
-- media_fact_snapshots.release_facts holds the release's batch plan, one entry
-- per file of the torrent, which the import reads to place each file. Its
-- 64 KiB bound refused a box set of a few hundred files at admission, as an
-- internal error. The bound becomes 2 MiB, the bound of the releases row whose
-- inventory the plan classifies; admission refuses a plan over it with the
-- typed torrent_too_large reason before the row is written.
--
-- Target render snapshots are not rewritten. Their vars member is no longer
-- written (the release-level naming vars are frozen once, in the task's frozen
-- policy) and was never read, while their bytes are bound by spec_digest and
-- the target set digest.

UPDATE one_shot_acquisitions
SET input_snapshot = jsonb_set(input_snapshot, '{target_facts}',
        ((input_snapshot->'target_facts') - 'targets' - 'observations')
        || jsonb_build_object('target_count',
            CASE WHEN jsonb_typeof(input_snapshot->'target_facts'->'targets') = 'array'
                THEN jsonb_array_length(input_snapshot->'target_facts'->'targets') ELSE 0 END))
WHERE jsonb_typeof(input_snapshot->'target_facts') = 'object'
  AND (input_snapshot->'target_facts' ? 'targets' OR input_snapshot->'target_facts' ? 'observations');

UPDATE one_shot_execution_attempts
SET input_snapshot = jsonb_set(input_snapshot, '{target_facts}',
        ((input_snapshot->'target_facts') - 'targets' - 'observations')
        || jsonb_build_object('target_count',
            CASE WHEN jsonb_typeof(input_snapshot->'target_facts'->'targets') = 'array'
                THEN jsonb_array_length(input_snapshot->'target_facts'->'targets') ELSE 0 END))
WHERE jsonb_typeof(input_snapshot->'target_facts') = 'object'
  AND (input_snapshot->'target_facts' ? 'targets' OR input_snapshot->'target_facts' ? 'observations');

UPDATE download_tasks
SET frozen_input = jsonb_set(frozen_input, '{target_facts}',
        ((frozen_input->'target_facts') - 'targets' - 'observations')
        || jsonb_build_object('target_count',
            CASE WHEN jsonb_typeof(frozen_input->'target_facts'->'targets') = 'array'
                THEN jsonb_array_length(frozen_input->'target_facts'->'targets') ELSE 0 END))
WHERE jsonb_typeof(frozen_input->'target_facts') = 'object'
  AND (frozen_input->'target_facts' ? 'targets' OR frozen_input->'target_facts' ? 'observations');

UPDATE download_tasks
SET frozen_input = frozen_input #- '{selected_release,internal_facts}'
WHERE jsonb_typeof(frozen_input->'selected_release') = 'object'
  AND frozen_input->'selected_release' ? 'internal_facts';

ALTER TABLE media_fact_snapshots
    DROP CONSTRAINT media_fact_snapshots_release_facts_check,
    ADD CONSTRAINT media_fact_snapshots_release_facts_check CHECK (pg_column_size(release_facts) <= 2097152);
