-- Forward-only migration 068: the default AniBT resolution ranking uses
-- AniBT's own resolution values (owner ruling 2026-10-27).
--
-- AniBT's resolution filter offers 4K, 2160p, 1080p, 720p, 480p and 360p; it
-- has no 1440p, and it spells some UHD releases 4K rather than 2160p. The
-- default anibt.resolution ranking now lists exactly those values, 4K and
-- 2160p at the top. Only a stored global default that is still the one
-- migration 067 wrote is replaced; an edited one, and every per-anime copy
-- (one-time input), stays as it is.

UPDATE global_config_sections
SET value = jsonb_set(value, '{quality_profile}', '{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"anibt.language","ranks":[["CHS","CHT","JP"],["CHS","JP"],["CHS","CHT"],["CHS"],["CHT","JP"],["CHT"],["JP"],["EN"]]},{"key":"rt.sub_lang","ranks":[["SC","TC","JPN"],["SC","JPN"],["SC","TC"],["SC"],["TC","JPN"],["TC"],["JPN"],["ENG"]]},{"key":"anibt.resolution","ranks":[["4K"],["2160p"],["1080p"],["720p"],["480p"],["360p"]]},{"key":"rt.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}'::jsonb),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'replacement'
  AND value->'quality_profile' = '{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"anibt.language","ranks":[["CHS","CHT","JP"],["CHS","JP"],["CHS","CHT"],["CHS"],["CHT","JP"],["CHT"],["JP"],["EN"]]},{"key":"rt.sub_lang","ranks":[["SC","TC","JPN"],["SC","JPN"],["SC","TC"],["SC"],["TC","JPN"],["TC"],["JPN"],["ENG"]]},{"key":"anibt.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"rt.resolution","ranks":[["2160p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}'::jsonb;
