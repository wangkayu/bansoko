-- Forward-only migration 006: the subgroup dictionary's user layer.
--
-- The subgroup dictionary is the collected group snapshot published with the
-- release — embedded in the binary and read-only — union the groups a user
-- retrieved from a provider and added. This migration adds the user-layer
-- relation only. The collected layer is deliberately not a table, so no row
-- here can shadow the release's own snapshot; `source` still names both layers
-- so a row's provenance is always explicit and a future collected import cannot
-- be confused with a user's own addition. Only 'user' rows are ever written.
--
-- This is a forward-only addition rather than an edit to 001_baseline.sql. The
-- baseline is immutable: an installed database verifies the checksum of every
-- migration it has applied before it applies anything else, so editing the
-- baseline would stop an existing install from starting instead of giving it a
-- table. The relation is new in every environment — no stored row expressed a
-- dictionary entry before it existed — so it is created empty, with no data
-- migration. cmd/bansoko-migrate applies this file after the baseline and every
-- later forward migration, and its checksum is recorded in schema_migrations.

CREATE TABLE provider_group_dictionary (
    id TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_group_ref TEXT NOT NULL,
    display_name TEXT NOT NULL,
    available BOOLEAN NOT NULL DEFAULT TRUE,
    source TEXT NOT NULL DEFAULT 'user',
    added_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    version BIGINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT provider_group_dictionary_pkey PRIMARY KEY (id),
    CONSTRAINT provider_group_dictionary_provider_provider_group_ref_key UNIQUE (provider, provider_group_ref),
    CONSTRAINT provider_group_dictionary_id_check CHECK (char_length(btrim(id)) BETWEEN 1 AND 128),
    CONSTRAINT provider_group_dictionary_provider_check CHECK (provider IN ('anibt', 'mikan')),
    CONSTRAINT provider_group_dictionary_provider_group_ref_check CHECK (char_length(btrim(provider_group_ref)) BETWEEN 1 AND 128),
    CONSTRAINT provider_group_dictionary_display_name_check CHECK (char_length(display_name) <= 256),
    CONSTRAINT provider_group_dictionary_source_check CHECK (source IN ('collected', 'user')),
    CONSTRAINT provider_group_dictionary_version_check CHECK (version >= 1)
);

CREATE INDEX provider_group_dictionary_provider_idx ON provider_group_dictionary (provider, added_at DESC);

GRANT SELECT, INSERT, DELETE, UPDATE ON TABLE provider_group_dictionary TO bansoko_runtime;
