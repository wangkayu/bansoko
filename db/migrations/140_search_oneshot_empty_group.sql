-- A search-window one-shot records no source group name: a keyword search
-- result window is not a source group, and the release title's own marker is
-- a parse fact, not a recorded name. The placeholder rss_sources row such a
-- one-shot mounts therefore carries an empty group_name; every subscription
-- path still records the observed display name.
ALTER TABLE rss_sources DROP CONSTRAINT rss_sources_group_name_check;
ALTER TABLE rss_sources ADD CONSTRAINT rss_sources_group_name_check
 CHECK (char_length(btrim(group_name)) BETWEEN 0 AND 256);
