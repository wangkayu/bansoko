## Improved

- Run Bansoko without Docker on Linux x86-64, Linux ARM64, and Apple silicon Macs. The download includes the Web UI, database migrations, and MediaInfo. PostgreSQL 17 and qBittorrent run separately.
- Use bansoko-server doctor to check the host before starting. Managed Linux deployments support on-demand in-app updates on both architectures. macOS uses manual updates.
- Custom naming Vars is retired: naming and notification templates no longer support vars.*, plugin webhook envelopes no longer include vars.*, and the /api/v1/naming-vars* API and variable-program runtime have been removed.
- Business snapshots now use business-snapshot-v3. Active subscriptions, working candidates, drafts and revisions are migrated during the upgrade; historical execution records keep their frozen input and digest bytes unchanged.
- The reltitle title-parser component no longer loads manually placed or custom-compiled binaries: only the builtin parser and official update channel builds on the same major.minor line and strictly newer than the builtin are used. After upgrading bansoko-server, cached channel builds that are no longer ahead, or that sit on another version line, are silently ignored (never executed).

## Important

- On 0.2.3, run bansoko-server migrate check-vars (optionally with --json), replace every naming or notification template that still uses vars.*, and let affected in-flight work finish. The upgrade preflight blocks the upgrade while references remain and never rewrites historical frozen evidence.
- If you previously placed a custom-compiled reltitle component in the rt/ directory under your config directory (for example with a custom dictionary), that file is no longer loaded and title parsing falls back to the builtin parser. Check your saved title-tag filter rules to make sure they do not rely on values only the custom dictionary provided.
