-- Forward-only migration 036: the managed anime title is a fixed value.
--
-- `managed_anime.title_override` holds the anime's fixed title. It is written when the anime is created (the draft's
-- title override, else its title, which the draft prefills with the provider
-- title) and changes only by an owner edit; the metadata PATCH keeps it when
-- the title is null or omitted and never clears it. Naming's anime.title
-- therefore never falls through to a provider or metadata title for an anime
-- created by the application.
--
-- Existing active rows are backfilled with the title the UI showed for them
-- (the override, else the base title). Retired rows are frozen by the
-- lifecycle guard and stay as they are; their read-only display already
-- falls back to the base title. No CHECK makes the column NOT NULL: rows
-- written directly (fixtures, a retired owner) keep the fallback.

UPDATE managed_anime
SET title_override = btrim(title)
WHERE lifecycle_state = 'active'
  AND (title_override IS NULL OR btrim(title_override) = '');
