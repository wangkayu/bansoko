-- Forward-only migration 014: the seed torrent cache keeps least privilege.
--
-- The baseline's ALTER DEFAULT PRIVILEGES grants SELECT, INSERT, UPDATE and
-- DELETE to bansoko_runtime on every relation a later migration creates, so
-- 013's narrower GRANT left DELETE in place through that default. Nothing in
-- the application deletes a cache row: the table is content-keyed, and a
-- re-fetch after a removal replaces the same locator. The runtime ACL
-- inventory registers exactly INSERT and UPDATE for this table, and a grant no
-- code path can use is a privilege the runtime should not hold.
--
-- Eviction is out of scope for lazy expansion. When it arrives it belongs to a
-- deliberate maintenance path with its own grant, not to a leftover default.

REVOKE DELETE ON TABLE seed_torrent_cache FROM bansoko_runtime;
