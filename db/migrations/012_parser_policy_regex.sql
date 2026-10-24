-- Forward-only migration 012: the parser policy rows carry the regex kind.
--
-- The baseline is immutable. 001_baseline.sql is checksummed into
-- schema_migrations the instant a database installs it, and every later run
-- re-hashes the file it finds on disk against that record: an installed
-- database that sees different baseline bytes is refused with a checksum
-- mismatch rather than patched. The parser kind identity was renamed from
-- "traditional" to "regex" after databases had already applied the baseline,
-- so the baseline keeps the name it was born with and this file carries the
-- rename.
--
-- 1. The rename is an identity rename, not an alias: exactly one kind is
--    registered and no compatibility mapping is kept, so every stored value
--    that selects a parser has to move. The singleton section's `version` is
--    the CAS owner the parser policy service reads, and it is bumped with the
--    value: a save that arrives holding an older expected version loses its
--    race instead of overwriting the renamed kind with a stale one.
--
-- 2. The per-instance axis in anime_business_configs is loaded and validated
--    on every refresh, evaluation and one-shot: parserpolicy.LoadInstanceRequired
--    refuses a kind the registry does not know, and the rename kept no alias.
--    An installed database can already hold works accepted under the old
--    name, and a work's parser axis is frozen at accept and
--    never re-derived from the creation template, so an unrewritten row would
--    turn every later refresh of that work into INSTANCE_PARSER_POLICY_REQUIRED.
--    Rewriting it is faithful rather than a reinterpretation: an identity
--    rename selects the same parser. The provenance column
--    (parser_source_default_version) stays as recorded, because it names the
--    template version the work froze, not the spelling of the kind.
--
-- 3. Historical evidence is left as recorded. Candidate revisions freeze a
--    parser-policy envelope together with its digest, and activation re-resolves
--    the current default rather than trusting the saved envelope; rewriting one
--    without the other would break the digest for no runtime gain.
--    media_fact_snapshots.parser_kind is evidence of what parsed a release and
--    is never validated against the registry. Both keep the name they were
--    written with.
--
-- Rows are not backfilled for the same reason 011 leaves `year` NULL: this
-- file only has to hold on databases that already installed the baseline, and
-- those are pre-release.
--
-- The baseline stays immutable; this file is applied after it by
-- cmd/bansoko-migrate and is checksummed in schema_migrations. Version
-- discovery is the migration directory sorted by file name, so 012 is applied
-- on top of an installed 001..011 prefix without any manifest to register it
-- in.

UPDATE global_config_sections
SET value = jsonb_set(value, '{parser_kind}', '"regex"'::jsonb),
    version = version + 1
WHERE section_name = 'parser_policy';

UPDATE anime_business_configs
SET parser_kind = 'regex'
WHERE parser_kind = 'traditional';
