## Improved

- Custom naming Vars is retired: naming and notification templates no longer support vars.*, plugin webhook envelopes no longer include vars.*, and the /api/v1/naming-vars* API and variable-program runtime have been removed.
- Business snapshots now use business-snapshot-v3. Active subscriptions, working candidates, drafts and revisions are migrated during the upgrade; historical execution records keep their frozen input and digest bytes unchanged.

## Important

- On 0.2.3, run bansoko-server migrate check-vars (optionally with --json), replace every naming or notification template that still uses vars.*, and let affected in-flight work finish. The upgrade preflight blocks the upgrade while references remain and never rewrites historical frozen evidence.
