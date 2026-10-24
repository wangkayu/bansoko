-- Forward-only migration 028: drop the editable release format.
--
-- The release format is not an owner-configurable value: it is a read-only
-- fact the provider catalog states for an entry (Mikan homepage section,
-- AniBT format) and is read from provider_catalog_entries.source_hint at
-- read time. anime_metadata no longer stores it; neither the owner nor the
-- automatic metadata match writes it.
--
-- No data preservation: every current environment is a test environment.

ALTER TABLE anime_metadata
    DROP CONSTRAINT IF EXISTS anime_metadata_release_format_check;

ALTER TABLE anime_metadata
    DROP COLUMN IF EXISTS release_format;
