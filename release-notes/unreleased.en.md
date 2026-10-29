## New

- Torrent search adds Nyaa as a source, with its own access settings under connections.
- The update check shows the release notes of newer versions.
- One-shot downloads can choose whether files without an episode number are skipped or organized as unknown content.
- The one-shot settings step lets you pick the files of each seed, including videos and subtitles in subfolders; several picked copies of one episode follow the global collision policy.
- Execution records show the seed: list rows carry the subtitle group, main tags, version, size and torrent title, and replacements carry a "Replaces a version" mark; details add the source site, published time, info hash (copyable) and all tags.
- Episode offsets can be set per subtitle group before a subscription starts: drafts and not-yet-started subscriptions offer the offset on every group in the RSS tab, and the test run re-numbers immediately. The offset dialog shows a conversion example such as "Episode 53 → Episode 5".
- While editing an anime title (including the one-shot settings step), the localized and original TMDB and BGM names and the source title appear as suggestions you can click to fill in; confirming a TMDB match can also set the title to the matched name.
- When a TMDB/BGM match is missing or the template uses a variable that does not exist, finished downloads wait for configuration: the files stay in the download folder, the import continues by itself once the settings are complete, and it does not count as a failure.

## Improved

- RSS access can be added directly in connection settings; built-in connections can only be edited.
- The proxy and downloader dialogs check input before saving, with clearer layout and wording.
- Required subtitle groups no longer take an automatic selection slot, and a group score is at most 100.
- When an anime is bound to a TMDB movie, the config title area shows Movie and links to the movie page.
- The poster wall uses the full window width, and the browse page shows more titles per row as the window widens.
- In the default order for multiple versions of an episode, "reltitle Subtitle language" now ranks combined Simplified+Traditional/Japanese first, then Simplified/Japanese. The upgrade updates only this item of the global setting, and only if you never changed it; existing subscriptions keep their settings, so change a subscription yourself to use the new order.
- Clicking an anime you already subscribe to in Browse or search opens that subscription instead of a new draft; for a second subscription, use "Duplicate as New Subscription" on the subscription card.
- An anime's type (TV or movie) is now its own setting: it is filled once from the first TMDB match and is not changed when the match is replaced or cleared.
- After you change a subscription's title, season, year or TMDB/BGM match, its library files are renamed to the new settings, as when the naming template changes; when a match is missing or the template uses a variable that does not exist, the file stays where it is and the file list shows why.
- After you change a subgroup's episode offset, library files move to their new episode: files of the same episode are compared under the subscription's version-keeping mode, the preferred one is kept and the others are deleted; when all are kept, the collision policy applies, and the subscription activity records the result. The offset dialog explains these effects.
- When a file is aligned onto a path that already holds the same episode of the same subscription, the two are compared by the version-keeping mode first and the kept file moves; other cases follow the collision policy.
- A file whose subscription settings changed while it downloaded moves to the path the current settings give right after import, whether or not automatic alignment is on.
- After the title, bindings, season, year, type or naming rules change, a subscription's files are checked again against the current settings; with automatic alignment on they move to the new place, and nothing is downloaded again.
- Saving metadata now re-evaluates the unsubmitted releases in the same save, with no separate re-evaluation.
- The browse season dropdown always offers the next two seasons back to two years ago, so the current season is always one pick away; reach older seasons with the arrows, or find works by search.
- Deselecting a subgroup cancels its unfinished downloads in the subscription and deletes them from qBittorrent together with their downloaded data, and other subgroups' releases download the episode instead; finished downloads and imports in progress are not touched, and no failure notification is sent.
- After a subgroup is selected again, the releases skipped while it was deselected are evaluated again right away, without waiting for an RSS refresh or a new release; when it is selected while the subscription is stopped, they are evaluated when the subscription resumes.
- A download that has not been imported yet can be cancelled in the execution record dialog (also through the API, MCP and CLI), and its record shows Cancelled; a subscription release can be downloaded again with Retry, while a cancelled one-shot download cannot be retried.

## Fixed

- Upgrading with existing download and import records no longer fails and rolls back because of a database migration conflict.
- Subtitles tagged JPSC or JPTC in their file names now get a language, so Simplified/Japanese and Traditional/Japanese subtitles no longer collide on one name.
- Opening a seed's file list now fetches its torrent through the source's configured proxy instead of a direct connection.
- Releases carrying AniBT codec, bit-depth and similar tags no longer get stuck at the end of import; records whose files were copied but still show import in progress complete on their own after upgrading.
- On phones, picking a season from the dropdown no longer jumps back to the current season, and switching sections from settings keeps the chosen season.
- A new subscription whose sources refresh at the same time no longer downloads the same episode twice: its candidates wait for comparison and only one copy is downloaded.
- With the collision policy set to skip or add version, a quality replacement that lands on the same path no longer fails to import because the target exists; the activity log shows "will replace" at acceptance and "replaced" only after the import completes.
- When a library file is held open (by a player, for example), deleting a replaced version and overwriting on import retry every 5 minutes for up to 24 hours; a final failure is logged and notified, no longer blocks deployment recovery, and no longer always reads as missing write permission.
- Clearing a TMDB match also clears the season, year and total episodes it pre-filled; replacing the match uses the new match's values instead of keeping the first match's.
- Posters that failed to load during a service restart or a network blip now retry on their own, and at once when the window regains focus or the network comes back, so the page no longer needs a reload.
- The subscription list now updates its import progress and status as downloads and imports move, without a manual refresh.
- Changing the TMDB season while downloading no longer downloads the same episode a second time as a different episode: episodes are identified by subscription and episode number, and the season is used only for naming.
- The next automatic task time in the top-bar status now updates once it passes and is re-read when the status panel opens, instead of showing a time already gone.
- When editing a title, the title suggestions no longer cover the input's focus outline.
- Template variables with long examples (such as the info hash or the file name) wrap inside their item instead of pushing narrow columns such as the one-shot settings out of the window.
- Submitting a one-shot download of a season batch (24 episodes with extra files, for example) no longer fails with an internal error, and box sets listing several hundred files download as well. A torrent that lists too many files to record is refused with "This torrent lists too many files to download.", and such a torrent in a subscription is marked with the same reason.
- Torrents with more than 128 files no longer fail at file verification once downloaded, and every video of a box set is imported.
- File verification checks each file's size and location instead of reading the whole file, so verifying a large box set no longer takes longer as it grows.
- Each video in a box set gets its own media check for resolution, codec and similar details, so later videos no longer lose them to a shared time limit.
- With an empty subtitle suffix template, releases that include subtitles no longer fail while their files are prepared for import.

## Important

- Only one downloader is supported, and its download folder is now required.
- On upgrade, if one subscription already holds files from different seasons, only the current season's files take part in version replacement; the others stay where they are, and the subscription activity records how many, for manual cleanup.
- An anime's type cannot be switched between TV and movie while it has library files or unfinished downloads or imports.
- The anime metadata type field is renamed media_kind in the API and MCP; scripts and assistants that use it need the new name.
- In the API, target_facts of one-shot downloads and download tasks no longer list each target; they give the number of targets as target_count, and existing records are rewritten on upgrade.
- In the API, file fingerprints use the metadata form (the file size and, when available, the device and inode) instead of a sha256 content digest; existing records are rewritten on upgrade.
