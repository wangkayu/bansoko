## New

- Download, import and unrecognized-file notifications support task.origin to distinguish subscription tasks (subscription) from one-shot tasks (one_shot). Source-aware events add release.site for the publishing site and release.link for an explicit release details page; download.link remains the download address.
- Containers now support bansoko-server update check|apply|status for embedded update checks, admission and status, with human-readable and JSON output. Access is limited to root or the serving PUID and requires no web session or API token.
- Library import completion and execution failure notifications support original file-name attributes through frt.*. File displacement notifications can show both files using frt.* and taker.frt.*.

## Improved

- Torrent structure summaries show names and sizes on separate lines without relying on monospace alignment. Long names and control characters no longer disrupt entry layout.
- System-default import notifications include the original file group. File displacement notifications show each file's group and specifications separately. Missing-value lines are hidden in both English and Chinese templates.
- Test deliveries mark the title with [TEST]; bodies and business notifications are unaffected.
- In library import completion notifications, out.name is now the actual placed file name, and out.dir and out.path are the absolute parent directory and absolute path in the application runtime. The system default no longer shows the directory line, while custom templates can still reference these variables. out.subtitles is now the list of final file names of bound subtitles that were placed successfully, without directories, shown one per line in processing order and hidden entirely when empty.
- The notification template editor now uses a card layout: the event list and the editor each sit in their own card, system default templates show as labeled read-only panels, and events with custom templates use an accent badge.

## Fixed

- One-shot tasks from keyword search no longer render the internal search identity (search-query:…) into preview paths as the naming group fact; preview and actual import now agree, with anime.group empty in both.
- Secret inputs in forms such as notification channels (Bot Token, webhook URL, signing secret) had lost their standard height and text size; they now match the other inputs.
- Original file-name attributes remain with managed files after operation details are cleared, so later replacement notifications can still compare the two versions.

## Important

- Keyword-search one-shot tasks no longer parse a group name from the release title, and the unknown placeholder is gone: ${group} (anime.group) renders empty in both preview and import for search one-shots. To use the group named in the title, reference ${rt.group} (release title parse) or ${frt.group} (file name parse). Subscription and custom source group names are unchanged.
- media.resolution in file naming now comes only from file inspection or the media.quality frozen at acceptance; the release-title parse fallback for historical inputs is removed, so early tasks without a frozen value render it empty on re-render.
- New notifications use release.src consistently for the acquisition channel: anibt, mikan, nyaa, jackett or custom, instead of provider_group/custom_rss. Custom templates that compare the old values need manual adjustment. This source change does not append source lines to default or custom templates.
- Source evidence is captured when a task is accepted or an event is created; delayed delivery and retries do not query the current source again. Missing site or details-page evidence stays empty. Credential-bearing or malformed links are withheld, and a download address never substitutes for a details page.
- This change does not support replay of old task inputs, search snapshot digests or request protocols, and does not backfill historical sources. Finish in-flight tasks before upgrading, then search again and issue new requests. Frozen notification content is not rewritten; replay of historical pending notifications is not guaranteed.
- Local update commands require the new server; older images and overlays do not gain them retroactively, so the first upgrade still uses the Web UI or an image switch. After an overlay upgrade, /app/bansoko-server may remain the old base. Read current under BANSOKO_UPDATES_ROOT and invoke the bansoko-server in the releases directory it points to, or /app/bansoko-server when no overlay exists. update help includes the complete container invocation; it needs neither /proc/1/exe access nor a Docker management socket. apply reports admission only by default. --wait retries only while the command remains alive: handoff can terminate docker exec, and neither disconnection nor a Docker exit code establishes the update outcome. After restart, query update status with a new exec.
- Notification targets using system defaults adopt the new wording; custom templates remain unchanged.
- frt.* is supported only in naming, library import completion, execution failure and file displacement. Existing references in other events must be removed. taker.frt.* is supported only in file displacement.
- Older records without stored file-name attributes remain empty; renamed library files are not reparsed. Notifications for preparation-stage naming failures are outside this change.
- Historical notifications and frozen notification data remain unchanged; nothing is backfilled.
