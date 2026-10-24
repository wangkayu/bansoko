-- Forward-only migration 020: the live global parser-policy row accepts only
-- the catalog's parser kinds (internal/parser.Kinds). The baseline CHECK
-- bounds the length only. Frozen evidence (media_fact_snapshots.parser_kind,
-- candidate revision envelopes) deliberately keeps the length-only guard:
-- history is never re-validated against the current catalog.
--
-- The per-instance axis (anime_business_configs.parser_kind) gets no value
-- CHECK: its kind is validated against the registry on every load
-- (parserpolicy.LoadInstanceRequired fails closed on an unknown kind), and
-- composition tests register throwaway kinds there.
--
-- Parallel lanes may also add 017+; this number may be renumbered at merge.
-- Adding a kind means one more value in the list below and in the catalog;
-- internal/parser/kinds_test.go fails when they drift.

ALTER TABLE global_config_sections
    ADD CONSTRAINT global_config_sections_parser_kind_value_check
    CHECK (section_name <> 'parser_policy' OR (value->>'parser_kind') IN ('regex', 'reltitle'));
