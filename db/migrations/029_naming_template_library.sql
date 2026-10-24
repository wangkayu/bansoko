-- Forward-only migration 029: the naming template library.
--
-- Naming presets become a user-managed template list edited only in the global
-- settings. One template is the default, and the global naming is that
-- template: the default template's body IS the `naming_rules` row of
-- global_config_sections (or the code default while that row is unsaved), so
-- every existing global naming reader keeps reading one value and the two can
-- never disagree. This table holds the list itself: order, keys, names, the
-- default key, and the bodies of the non-default templates. The default entry
-- carries no body here.
--
-- Shape of `value`:
--   {"default_key": "<key>",
--    "templates": [{"key": "<key>", "name": "<name>"[, "rules": {...naming rules}]}]}
-- Exactly the non-default entries carry `rules`; the application validates the
-- naming bodies with the naming validator before it writes. The database
-- checks the structural invariants: at least one template, and the default key
-- names one of them.
--
-- A template is a quick fill for an anime override: picking one copies its
-- body into the anime's own naming values. Nothing links back, so no other
-- table references a template.
--
-- The library ships with one template, the current default naming. There is
-- no data-preservation obligation; the seed is the only row.

CREATE TABLE naming_template_library (
    singleton  BOOLEAN PRIMARY KEY DEFAULT TRUE,
    value      JSONB NOT NULL,
    version    BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT naming_template_library_singleton_check CHECK (singleton),
    CONSTRAINT naming_template_library_version_check CHECK (version >= 1),
    CONSTRAINT naming_template_library_value_check CHECK (
        jsonb_typeof(value) = 'object'
        AND pg_column_size(value) <= 1048576
        AND jsonb_typeof(value->'default_key') = 'string'
        AND jsonb_typeof(value->'templates') = 'array'
        AND jsonb_array_length(value->'templates') BETWEEN 1 AND 50
        AND jsonb_path_exists(value, '$.templates[*] ? (@.key == $.default_key)')
    )
);

INSERT INTO naming_template_library (singleton, value, version)
VALUES (TRUE, '{"default_key":"default","templates":[{"key":"default","name":"Default"}]}'::jsonb, 1);

GRANT SELECT, INSERT, UPDATE ON TABLE naming_template_library TO bansoko_runtime;
-- Schema default privileges also grant DELETE; the library is never deleted.
REVOKE DELETE ON TABLE naming_template_library FROM bansoko_runtime;
