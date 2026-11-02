-- Forward-only migration 117: the RSS refresh fingerprints the facts it
-- observed instead of comparing the columns it writes.
--
-- The evaluation stage rewrites parsed_facts and internal_facts with its own
-- decision-time spelling: a batch plan that places nothing clears the parsed
-- episode, and target planning adds release files. A poll that compares the
-- stored columns against what the feed produced therefore sees a permanent
-- difference on every release the evaluation has touched, and re-enqueues the
-- whole chain — evaluation and competition included — every cycle.
--
-- feed_facts_digest is the refresh's own sha256 over the four encoded fact
-- blobs it observed, written only by the refresh. A poll changes the Release
-- when that digest, the stored feed verdict (migration 109), or the evaluation
-- version differs from what it just observed; the columns themselves are still
-- rewritten so the row keeps the latest feed facts.
--
-- No data changes: the column starts empty, so the first poll after the
-- upgrade re-evaluates each decided release once and stays quiet afterwards.

ALTER TABLE releases ADD COLUMN feed_facts_digest TEXT;
ALTER TABLE releases ADD CONSTRAINT releases_feed_facts_digest_check
    CHECK (feed_facts_digest IS NULL OR feed_facts_digest ~ '^[0-9a-f]{64}$');
COMMENT ON COLUMN releases.feed_facts_digest IS
    'The RSS refresh''s own fingerprint of the four fact blobs it observed; NULL before migration 117 backfills it on the next poll. Never written by the evaluation stage.';
