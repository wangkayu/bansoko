## Improved

- Improved multi-episode file numbering: one-shot downloads can map each single-numbered file to 1–12 consecutive episodes, while per-file overrides accept a final episode or a range of up to 12 episodes. Naming templates, RSS previews, and download, discovery, and library-import notifications now show complete episode ranges consistently; the CLI also supports --episode-span and --overrides.
- Refined episode parsing settings: parser selection now uses a compact segmented control with reltitle first, and unsaved choices flow into RSS previews; custom regexes support ordered editing, reordering, batch testing, and match highlighting, while instance parser settings load directly with the workspace to reduce waits and flicker when switching tabs.
- Filtered Jackett seed search results by category: automatically excludes unusable items such as ebooks, comics, pure audio, software, games, raw Blu-ray or DVD discs, modded Blu-rays, and external disc structures using standard Torznab IDs and tracker category names, while preserving anime, movies, TV series, documentaries, variety shows, live concerts, and music videos with no extra configuration.
- Faster cover and poster loading: the same-origin image proxy now supports a thumbnail rendition that derives and caches 320px-wide JPEG thumbnails on the server; browse and subscribed poster cards load thumbnails by default to speed up first visits while zoom previews still fetch the full-size original, with automatic fallback to original bytes for small or unsupported formats.
- Refined the tag filter editor: collapsed the role selector and candidate list behind a single Add tags toggle, removed the stray free-text tag input row and role-swap buttons, and grouped selected tags by dimension in inline pills; also added read-only tag filter summaries, live preview badge highlighting, and combined missing/blacklisted filter reasons within a row.

## Fixed

- Fixed per-file numbering edits being dropped after editing in batches: checkboxes now control batch actions only, and confirming applies every overridden row, including rows that were unchecked while editing another batch.
- Upgraded the image decoding dependency to address known WebP cover decoding vulnerabilities.
