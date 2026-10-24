-- Forward-only migration 005: add the owner media-kind override to the
-- anime metadata owner.
--
-- `media_kind_override` pins the naming branch selector one release identity
-- and one naming render resolve with. It is a
-- nullable text column with exactly the TMDB media-kind vocabulary; NULL means
-- unset and the frozen media kind then falls back to the TMDB binding's
-- `tmdb_media_kind`, then to the unbound TV default. The override is a
-- separate owner field and never rewrites the TMDB tuple (tmdb_work_id,
-- tmdb_media_kind, tmdb_season stay the binding the matcher wrote).
--
-- The column is added without a data migration: every current environment is a
-- test environment, and NULL is the correct value for an existing row because
-- no stored row expressed an explicit media-kind pin before this column
-- existed. The baseline and later forward migrations stay immutable; this file
-- is applied after them by cmd/bansoko-migrate and is checksummed in
-- schema_migrations.

ALTER TABLE anime_metadata
    ADD COLUMN media_kind_override TEXT;

ALTER TABLE anime_metadata
    ADD CONSTRAINT anime_metadata_media_kind_override_check
        CHECK (media_kind_override IS NULL OR media_kind_override IN ('tv', 'movie'));
