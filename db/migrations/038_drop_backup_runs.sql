-- Forward-only migration 038: backups are no longer stored by the app. A backup
-- is an export the owner downloads and a restore is an upload, so the scheduled
-- and manual run records (and their 'scheduled' trigger) have no owner left.
-- The episodes of the retired backup diagnostics go with them.

DROP TABLE backup_runs;

DELETE FROM diagnostic_episodes WHERE component = 'backup';
