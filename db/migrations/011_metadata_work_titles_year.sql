-- Forward-only migration 011: metadata_work_titles records the provider's year.
--
-- A work's first air/release year is a provider fact: Bangumi reports it as the
-- subject `date`, TMDB as `first_air_date` (tv) or `release_date` (movie). It
-- is recorded on the same row and in the same upsert as the work's native
-- titles, so an acceptance can freeze the year of a bound work and a naming
-- template can project `anime.year` from frozen evidence instead of guessing
-- from a title, a file name or a search order.
--
-- The column is nullable and never backfilled: every environment is a test
-- environment, and a work the server has not observed (for example one bound
-- by hand) has no year. A value outside 1900..2100 is not a release year; the
-- write path stores NULL for it and this CHECK refuses one that slips through.
--
-- The baseline stays immutable; this file is applied after it by
-- cmd/bansoko-migrate and is checksummed in schema_migrations.

ALTER TABLE metadata_work_titles ADD COLUMN year INTEGER;

ALTER TABLE metadata_work_titles
    ADD CONSTRAINT metadata_work_titles_year_check
        CHECK (year IS NULL OR year BETWEEN 1900 AND 2100);
