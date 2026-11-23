-- The admin scope gates irreversible high-risk operations (destructive deletes,
-- the API token lifecycle, update admission). Existing tokens keep their scope
-- sets; the check only widens to the canonical sets that include admin.
ALTER TABLE public.api_tokens
    DROP CONSTRAINT api_tokens_scopes_check,
    ADD CONSTRAINT api_tokens_scopes_check CHECK (scopes IN (
        'read',
        'read,write',
        'read,admin',
        'read,secrets',
        'read,write,admin',
        'read,write,secrets',
        'read,admin,secrets',
        'read,write,admin,secrets'
    ));

COMMENT ON COLUMN api_tokens.scopes IS 'Canonical comma-joined scope set (read always included; admin gates irreversible high-risk operations). Immutable after creation; rotation keeps it.';
