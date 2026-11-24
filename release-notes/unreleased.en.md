## Improved

- Faster cover and poster loading: the same-origin image proxy now supports a thumbnail rendition that derives and caches 320px-wide JPEG thumbnails on the server; browse and subscribed poster cards load thumbnails by default to speed up first visits while zoom previews still fetch the full-size original, with automatic fallback to original bytes for small or unsupported formats.
- Refined the tag filter editor: collapsed the role selector and candidate list behind a single Add tags toggle, removed the stray free-text tag input row and role-swap buttons, and grouped selected tags by dimension in inline pills; also added read-only tag filter summaries, live preview badge highlighting, and combined missing/blacklisted filter reasons within a row.
