-- Forward-only migration 109: the RSS refresh keeps its own pre-admission
-- verdict apart from the Release's decided verdict.
--
-- The refresh evaluates every feed item to decide whether anything changed
-- since the last poll. Its result (filtered / waiting_metadata /
-- accepted_pending_download / parse_failed) is what the feed alone can know:
-- it has no download task, no target plan, and no competition round. The
-- verdict column carries the decision the evaluation stage reached instead
-- (awaiting competition, filtered by the batch plan, submitted, adopted).
-- Writing the feed's own value over it made every poll look like a change,
-- which re-ran the evaluation and the competition every cycle for every
-- decided release.
--
-- The stored feed verdict is what a poll's change check compares against.
-- accepted_pending_download is a pending state, never an answer: the refresh
-- still leaves verdict alone for it and lets the evaluation decide.
--
-- No data changes: the column is added empty and the next poll of each source
-- fills it. A Release whose stored feed verdict is empty therefore re-evaluates
-- once after the upgrade and stays quiet afterwards; terminal releases are
-- skipped by the existing guard, so only the decided-but-not-adopted ones take
-- part.

ALTER TABLE releases ADD COLUMN feed_verdict TEXT;
ALTER TABLE releases ADD CONSTRAINT releases_feed_verdict_check
    CHECK (feed_verdict IS NULL OR feed_verdict IN ('filtered', 'waiting_metadata', 'accepted_pending_download', 'parse_failed'));
COMMENT ON COLUMN releases.feed_verdict IS
    'The RSS refresh''s own pre-admission evaluation of the feed item; NULL before migration 109 backfills it on the next poll. Never the Release decision (see verdict).';
