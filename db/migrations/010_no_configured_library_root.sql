-- Forward-only migration 010: no configured library root.
--
-- Migration 009 removed the file manager's root layer. This one removes the
-- configuration that fed it: the seeded `library_root` section, the two CHECK
-- carve-outs that existed only to let it hold a null value, and the last two
-- SQL readers of the section.
--
-- 1. The section row is deleted and its name is removed from the section-name
--    enum, so no section can be stored under it again. The value-presence
--    carve-out collapses into the ordinary "a stored section carries a value"
--    rule now that no section is exempt.
--
-- 2. acquisition_unknown_files relativized a file's absolute target against
--    the configured root to produce the display text a notification may carry
--    (file_notification_text refuses any absolute path). The base for that
--    relativization is now the acquisition's own frozen delivery route root —
--    the work's tree top, frozen at acceptance. Same shape and same fallback
--    (an unrecognized file outside the base stays unreported), one source per
--    acquisition instead of one global setting for all of them.
--
-- 3. unknown_files_notification built a file-manager anchor that named the
--    file. A notification payload is stored frozen and delivered to external
--    adapters verbatim, and the tree top it would need is not part of that
--    payload: an absolute container path cannot go in, and a relative path has
--    no base there. The link therefore opens this acquisition's execution
--    record, which lists its files — the form the multi-file case already used.
--
-- Rows are not migrated: every environment is pre-release, and the notify
-- projectors recompute from the acquisition's frozen input on the transition
-- that admits a notice.
--
-- This is a forward-only change rather than an edit to 001_baseline.sql. The
-- baseline is immutable: an installed database verifies the checksum of every
-- migration it has applied before it applies anything else, so editing the
-- baseline would stop an existing install from starting instead of removing
-- the section. cmd/bansoko-migrate applies this file after the baseline and
-- every earlier forward migration, and its checksum is recorded in
-- schema_migrations.

DELETE FROM global_config_sections WHERE section_name = 'library_root';

ALTER TABLE global_config_sections
    DROP CONSTRAINT global_config_sections_section_name_check,
    DROP CONSTRAINT global_config_sections_value_presence_check,
    ADD CONSTRAINT global_config_sections_section_name_check CHECK (section_name IN ('subgroups', 'parser', 'release_rules', 'file_rules', 'subtitle_rules', 'naming_rules', 'file_effect', 'tag_filters', 'download_completion', 'retention', 'parser_policy', 'file_effect_policy')),
    ADD CONSTRAINT global_config_sections_value_presence_check CHECK (value IS NOT NULL AND value <> 'null'::jsonb);

CREATE OR REPLACE FUNCTION acquisition_unknown_files(p_root text) RETURNS jsonb
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $$
 WITH route AS (
  SELECT rtrim(btrim(a.input->'delivery'->'route'->'root'->>'value'),'/') AS path
  FROM public.acquisitions a
  WHERE a.id=p_root AND a.input->'delivery'->'route'->'root'->>'state'='known'
 ), files AS (
  SELECT CASE WHEN route.path <> '' AND left(fo.target_path,length(route.path)+1)=route.path||'/'
              THEN substring(fo.target_path FROM length(route.path)+2) END AS relative,
         COALESCE(CASE WHEN jsonb_typeof(fo.target_fingerprint->'size_bytes')='number' THEN (fo.target_fingerprint->>'size_bytes')::bigint END,
                  af.size_bytes) AS size
  FROM public.file_operations fo
  LEFT JOIN public.artifact_files af ON af.artifact_id=fo.artifact_id AND af.file_ref=fo.file_ref
  LEFT JOIN route ON TRUE
  WHERE fo.acquisition_id=p_root AND fo.state='completed'
    AND fo.frozen_render_snapshot->'unknown_file'='true'::jsonb
    AND COALESCE(af.media_kind,'video')='video'
 )
 SELECT COALESCE(jsonb_agg(jsonb_build_object('path',public.file_notification_text(relative),'size',size) ORDER BY relative),'[]'::jsonb)
 FROM files
$$;

CREATE OR REPLACE FUNCTION unknown_files_notification(p_root text, p_files jsonb) RETURNS jsonb
    LANGUAGE plpgsql STABLE SECURITY DEFINER
    SET search_path TO 'pg_catalog', 'public'
    AS $_$
DECLARE input jsonb; release_id text; source_facts jsonb; link text;
BEGIN
 SELECT q.input INTO input FROM public.acquisitions q WHERE q.id=p_root;
 SELECT t.release_id INTO release_id FROM public.download_tasks t WHERE t.acquisition_id=p_root AND t.release_id IS NOT NULL LIMIT 1;
 SELECT rel.source_facts INTO source_facts FROM public.releases rel WHERE rel.id=release_id;
 -- A notice payload is frozen and delivered to external adapters as it stands,
 -- so it carries no absolute container path. The tree the files live under is
 -- not part of that payload either, so the link cannot name one of them: it
 -- opens this acquisition's execution record, which lists the files, and the
 -- owner matches them from there. The anchor is an in-app hash.
 link:='#/?record='||public.notification_query_escape(p_root);
 RETURN public.notification_input_facts(COALESCE(input,'{}'::jsonb))
  || public.notification_value_facts(input->'config'->'vars'->'output',source_facts)
  || public.notification_release_facts(COALESCE(input,'{}'::jsonb),release_id)
  || jsonb_build_object(
   'anime.id',COALESCE(to_jsonb(public.acquisition_owner(p_root)),
                       public.notification_known(input->'config'->'identity'->'managed_anime_ref','{}')),
   'anime.title',public.notification_acquisition_title(p_root),
   'out.count',jsonb_array_length(p_files),
   'out.files',p_files,
   'event.link',link);
END $_$;
