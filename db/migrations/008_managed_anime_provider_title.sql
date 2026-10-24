-- Forward-only migration 008: the managed anime records the provider catalog
-- title observed at subscribe time.
--
-- AniBT/Mikan subscriptions see the work title in `provider_catalog_entries`.
-- Freezing that observed title onto `managed_anime.provider_title` at subscribe
-- time means the title chain can rank the provider title ahead of the metadata
-- titles without re-querying the catalog, and a later snapshot (or an entry that
-- was withdrawn afterwards) cannot change the title the subscriber already saw.
--
-- The column is nullable and never backfilled: a custom/oneshot subscription, or
-- a subscription whose entry the catalog does not carry, has no provider title,
-- and the chain falls through to the metadata titles and the base title. An
-- empty string is not a value; NULL is.
--
-- The baseline stays immutable; this file is applied after it by
-- cmd/bansoko-migrate and is checksummed in schema_migrations.

ALTER TABLE managed_anime ADD COLUMN provider_title TEXT;

ALTER TABLE managed_anime
    ADD CONSTRAINT managed_anime_provider_title_check
        CHECK (provider_title IS NULL OR char_length(btrim(provider_title)) BETWEEN 1 AND 512);
