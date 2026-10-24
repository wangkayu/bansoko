-- Forward-only migration 023: the owner-edited naming year.
--
-- `anime_metadata.year` is the year naming reads as anime.year. It is an owner value like `media_kind_override` and
-- `total_episodes`: NULL means unset and anime.year renders empty; naming
-- never falls back to a metadata provider's year. Matching TMDB in the UI only
-- fills the edit form with the work's year; nothing is stored until the owner
-- saves.
--
-- No data migration: every current environment is a test environment, and
-- NULL is the correct value for an existing row because no stored row
-- expressed an owner year before this column existed.
--

ALTER TABLE anime_metadata
    ADD COLUMN year INTEGER;

ALTER TABLE anime_metadata
    ADD CONSTRAINT anime_metadata_year_check
        CHECK (year IS NULL OR year BETWEEN 1900 AND 2100);
