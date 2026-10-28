-- Forward-only migration 094: the default rt.sub_lang ranking uses reltitle's
-- combined Chinese+Japanese subtitle values (owner ruling 2026-10-28).
--
-- Migration 064 copied AniBT's decomposed layout (SC+TC+JPN, SC+JPN, ...) into
-- the rt.sub_lang ranking, but reltitle reports Chinese plus Japanese
-- subtitles as one combined value (JPSC, JPTC) and drops SC/TC/JPN when it
-- does, so the most preferred releases (简繁日, 简日) matched no rank. The
-- default now mirrors the AniBT order in reltitle's spelling: JPSC+JPTC,
-- JPSC, SC+TC, SC, JPTC, TC, JPN, ENG. Only a stored global default that is
-- still the one migration 068 wrote is replaced; an edited one, and every
-- per-anime copy (one-time input), stays as it is.

UPDATE global_config_sections
SET value = jsonb_set(value, '{quality_profile}', '{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"anibt.language","ranks":[["CHS","CHT","JP"],["CHS","JP"],["CHS","CHT"],["CHS"],["CHT","JP"],["CHT"],["JP"],["EN"]]},{"key":"rt.sub_lang","ranks":[["JPSC","JPTC"],["JPSC"],["SC","TC"],["SC"],["JPTC"],["TC"],["JPN"],["ENG"]]},{"key":"anibt.resolution","ranks":[["4K"],["2160p"],["1080p"],["720p"],["480p"],["360p"]]},{"key":"rt.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}'::jsonb),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'replacement'
  AND value->'quality_profile' = '{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"anibt.language","ranks":[["CHS","CHT","JP"],["CHS","JP"],["CHS","CHT"],["CHS"],["CHT","JP"],["CHT"],["JP"],["EN"]]},{"key":"rt.sub_lang","ranks":[["SC","TC","JPN"],["SC","JPN"],["SC","TC"],["SC"],["TC","JPN"],["TC"],["JPN"],["ENG"]]},{"key":"anibt.resolution","ranks":[["4K"],["2160p"],["1080p"],["720p"],["480p"],["360p"]]},{"key":"rt.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}'::jsonb;
