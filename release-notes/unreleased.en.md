## New

- Torrent search adds Nyaa as a source, with its own access settings under connections.
- The update check shows the release notes of newer versions.
- One-shot downloads can choose whether files without an episode number are skipped or organized as unknown content.
- The one-shot settings step lets you pick the files of each seed, including videos and subtitles in subfolders; several picked copies of one episode follow the global collision policy.

## Improved

- RSS access can be added directly in connection settings; built-in connections can only be edited.
- The proxy and downloader dialogs check input before saving, with clearer layout and wording.
- Required subtitle groups no longer take an automatic selection slot, and a group score is at most 100.
- When an anime is bound to a TMDB movie, the config title area shows Movie and links to the movie page.

## Fixed

- Subtitles tagged JPSC or JPTC in their file names now get a language, so Simplified/Japanese and Traditional/Japanese subtitles no longer collide on one name.
- Opening a seed's file list now fetches its torrent through the source's configured proxy instead of a direct connection.
- Releases carrying AniBT codec, bit-depth and similar tags no longer get stuck at the end of import; records whose files were copied but still show import in progress complete on their own after upgrading.
- On phones, picking a season from the dropdown no longer jumps back to the current season, and switching sections from settings keeps the chosen season.
- A new subscription whose sources refresh at the same time no longer downloads the same episode twice: its candidates wait for comparison and only one copy is downloaded.
- With the collision policy set to skip or add version, a quality replacement that lands on the same path no longer fails to import because the target exists; the activity log shows "will replace" at acceptance and "replaced" only after the import completes.

## Important

- Only one downloader is supported, and its download folder is now required.
