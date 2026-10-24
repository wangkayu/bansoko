-- Forward-only migration 044: the owner-edited naming season.
--
-- `anime_metadata.season` is the season naming reads as anime.season,
-- anime.season_dir, anime.season_path and ep.s00e00, decoupled from the
-- TMDB id. It is an owner value
-- like `year`: NULL means unset (no season folder, ep.s00e00 renders the
-- episode alone), 0 is the specials season. The TMDB tuple
-- (tmdb_work_id, tmdb_media_kind, tmdb_season) stays atomic and unchanged:
-- tmdb_season is the bound TMDB season used for TMDB episode lookups and is
-- never read as the naming season again. Editing either never changes the
-- other; confirming a TMDB match only fills an unset season once.
--
-- Backfill: a TV row bound to a TMDB season keeps the season it named before
-- (naming-render-3 named the bound TV season, 0 as Season 00; movies and rows
-- without a binding named none). Data preservation is not required (every
-- environment is a test environment); this is only the trivial carry-over.
--

ALTER TABLE anime_metadata
    ADD COLUMN season INTEGER;

ALTER TABLE anime_metadata
    ADD CONSTRAINT anime_metadata_season_check
        CHECK (season IS NULL OR season >= 0);

UPDATE anime_metadata
   SET season = tmdb_season
 WHERE tmdb_season IS NOT NULL AND tmdb_season >= 0 AND tmdb_media_kind = 'tv';
