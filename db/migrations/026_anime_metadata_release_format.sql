-- Forward-only migration 026: the anime release format.
--
-- `anime_metadata.release_format` is tv | movie | ova | ona | special. The
-- automatic match fills it only while it is NULL, from the Bangumi subject
-- platform of the bound subject (TV, movie, OVA, WEB -> ona); any other
-- platform leaves it NULL. The owner edits it like year and total_episodes.
-- It is never guessed from a title and never derived from the TMDB media
-- kind. Naming does not read it.
--
-- No data migration: every current environment is a test environment, and
-- NULL (unknown) is the correct value for an existing row.

ALTER TABLE anime_metadata
    ADD COLUMN release_format TEXT;

ALTER TABLE anime_metadata
    ADD CONSTRAINT anime_metadata_release_format_check
        CHECK (release_format IS NULL OR release_format IN ('tv', 'movie', 'ova', 'ona', 'special'));
