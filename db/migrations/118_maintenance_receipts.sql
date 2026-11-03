-- Forward-only migration 118: maintenance receipts move off the library
-- filesystem into the database.
--
-- A maintenance invocation (a placement move, an overwrite exchange, a
-- deletion capture or purge) used to leave up to three entries next to the
-- operation's target: a start proof marker, an immutable final receipt, and a
-- lock inode that was never unlinked. All three were permanent residue in the
-- owner's library. The same facts now live in this table, and the mutual
-- exclusion moves to a PostgreSQL advisory lock on the invocation token.
--
-- The accepted trade: two processes sharing one library but pointing at
-- different databases no longer exclude each other; the deployment contract
-- is one database per instance.
--
-- started holds the start proof envelope (invocation, frozen source
-- observation, frozen parent identities); final holds the exact applied
-- receipt and is written once, never updated. A row with final IS NULL is an
-- interrupted invocation whose effect recovery must prove from the
-- filesystem. Refused invocations (their rename or unlink provably never
-- ran) delete their row again, so a retried stage leaves nothing behind.

CREATE TABLE maintenance_receipts (
    token TEXT NOT NULL,
    started JSONB NOT NULL,
    final JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finalized_at TIMESTAMPTZ,
    CONSTRAINT maintenance_receipts_pkey PRIMARY KEY (token),
    CONSTRAINT maintenance_receipts_token_check CHECK (token LIKE 'maintenance-%'),
    CONSTRAINT maintenance_receipts_final_check CHECK ((final IS NULL) = (finalized_at IS NULL))
);
COMMENT ON TABLE maintenance_receipts IS 'One maintenance invocation: start proof at admission, exact final receipt after its effect; replaces the on-disk marker files.';

GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE maintenance_receipts TO bansoko_runtime;
