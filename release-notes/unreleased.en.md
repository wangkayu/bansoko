## New

- Torrent search adds Nyaa as a source, with its own access settings under connections.
- The update check shows the release notes of newer versions.
- One-shot downloads can choose whether files without an episode number are skipped or organized as unknown content.
- The one-shot settings step lets you pick the files of each seed, including videos and subtitles in subfolders; several picked copies of one episode follow the global collision policy.
- Execution records show the seed: list rows carry the subtitle group, main tags, version, size and torrent title, and replacements carry a "Replaces a version" mark; details add the source site, published time, info hash (copyable) and all tags.

## Improved

- RSS access can be added directly in connection settings; built-in connections can only be edited.
- The proxy and downloader dialogs check input before saving, with clearer layout and wording.
- Required subtitle groups no longer take an automatic selection slot, and a group score is at most 100.
- When an anime is bound to a TMDB movie, the config title area shows Movie and links to the movie page.
- The poster wall uses the full window width, and the browse page shows more titles per row as the window widens.
- In the default order for multiple versions of an episode, "reltitle Subtitle language" now ranks combined Simplified+Traditional/Japanese first, then Simplified/Japanese. The upgrade updates only this item of the global setting, and only if you never changed it; existing subscriptions keep their settings, so change a subscription yourself to use the new order.

## Fixed

- Subtitles tagged JPSC or JPTC in their file names now get a language, so Simplified/Japanese and Traditional/Japanese subtitles no longer collide on one name.
- Opening a seed's file list now fetches its torrent through the source's configured proxy instead of a direct connection.
- Releases carrying AniBT codec, bit-depth and similar tags no longer get stuck at the end of import; records whose files were copied but still show import in progress complete on their own after upgrading.
- On phones, picking a season from the dropdown no longer jumps back to the current season, and switching sections from settings keeps the chosen season.
- A new subscription whose sources refresh at the same time no longer downloads the same episode twice: its candidates wait for comparison and only one copy is downloaded.
- With the collision policy set to skip or add version, a quality replacement that lands on the same path no longer fails to import because the target exists; the activity log shows "will replace" at acceptance and "replaced" only after the import completes.
- When a library file is held open (by a player, for example), deleting a replaced version and overwriting on import retry every 5 minutes for up to 24 hours; a final failure is logged and notified, no longer blocks deployment recovery, and no longer always reads as missing write permission.
- Clearing a TMDB match also clears the season, year and total episodes it pre-filled; replacing the match uses the new match's values instead of keeping the first match's.
- Posters that failed to load during a service restart or a network blip now retry on their own, and at once when the window regains focus or the network comes back, so the page no longer needs a reload.

## Important

- Only one downloader is supported, and its download folder is now required.
