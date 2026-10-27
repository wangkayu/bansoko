-- Forward-only migration 067: the default version priority uses AniBT's own
-- language spelling and ranks resolution per source (owner rulings
-- 2026-10-27).
--
-- Migration 064 ranked AniBT languages as CHS/CHT/JPN/ENG, but AniBT spells
-- Japanese and English JP and EN, so those ranks never matched. The default
-- now uses AniBT's spelling and ranks resolution per source like language:
-- anibt.resolution first, then rt.resolution in reltitle's canonical spelling
-- (its vocabulary has no 1440p), replacing the source-less release.resolution.
-- A layer a release has no data for ties. Only a stored global default that is
-- still the one migration 064 wrote is replaced; an edited one, and every
-- per-anime copy (one-time input), stays as it is.

UPDATE global_config_sections
SET value = jsonb_set(value, '{quality_profile}', '{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"anibt.language","ranks":[["CHS","CHT","JP"],["CHS","JP"],["CHS","CHT"],["CHS"],["CHT","JP"],["CHT"],["JP"],["EN"]]},{"key":"rt.sub_lang","ranks":[["SC","TC","JPN"],["SC","JPN"],["SC","TC"],["SC"],["TC","JPN"],["TC"],["JPN"],["ENG"]]},{"key":"anibt.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"rt.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}'::jsonb),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'replacement'
  AND value->'quality_profile' = '{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"anibt.language","ranks":[["CHS","CHT","JPN"],["CHS","JPN"],["CHS","CHT"],["CHS"],["CHT","JPN"],["CHT"],["JPN"],["ENG"]]},{"key":"rt.sub_lang","ranks":[["SC","TC","JPN"],["SC","JPN"],["SC","TC"],["SC"],["TC","JPN"],["TC"],["JPN"],["ENG"]]},{"key":"release.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}'::jsonb;
