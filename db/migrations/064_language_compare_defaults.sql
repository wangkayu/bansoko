-- Forward-only migration 064: the default version priority compares subtitle
-- languages per source (owner rulings 2026-10-27).
--
-- The built-in default used to rank the built-in rules' raw language tokens
-- one by one (chs, sc, cht, tc, jpn, eng), which split one language into two
-- ranks and left bilingual releases unlisted. It now ranks language
-- combinations in each source's own spelling, AniBT first, then reltitle; a
-- layer a release has no data for ties, so AniBT first does not favour AniBT
-- releases. Only a stored global default that is still the old built-in
-- default is replaced; an edited one, and every per-anime copy (one-time
-- input), stays as it is.

UPDATE global_config_sections
SET value = jsonb_set(value, '{quality_profile}', '{"version":"profile-v1","items":[{"key":"subgroup.score","ranks":[]},{"key":"anibt.language","ranks":[["CHS","CHT","JPN"],["CHS","JPN"],["CHS","CHT"],["CHS"],["CHT","JPN"],["CHT"],["JPN"],["ENG"]]},{"key":"rt.sub_lang","ranks":[["SC","TC","JPN"],["SC","JPN"],["SC","TC"],["SC"],["TC","JPN"],["TC"],["JPN"],["ENG"]]},{"key":"release.resolution","ranks":[["2160p"],["1440p"],["1080p"],["720p"],["480p"]]},{"key":"release.revision","ranks":[]}]}'::jsonb),
    version = version + 1,
    updated_at = CURRENT_TIMESTAMP
WHERE section_name = 'replacement'
  AND value->'quality_profile' = '{"version": "profile-v1", "items": [{"key": "subgroup.score", "ranks": []}, {"key": "release.lang", "ranks": [["chs"], ["sc"], ["cht"], ["tc"], ["jpn"], ["eng"]]}, {"key": "release.resolution", "ranks": [["2160p"], ["1440p"], ["1080p"], ["720p"], ["480p"]]}, {"key": "release.revision", "ranks": []}]}'::jsonb;
