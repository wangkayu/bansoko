## New

- Every candidate in the metadata match dialog now has a light "EP List" entry that shows episode titles and air dates before a candidate is selected. TMDB TV candidates are browsed by season; Bangumi candidates are grouped into main episodes and specials with no season picker. The list is reference only: it does not select the candidate or change the match settings or Apply Season.
- One-shot downloads can set final season and episode numbers per video, with path filtering, sequential batch numbering and restoration of automatic recognition. Manual episodes do not receive the episode offset, and subtitles always follow their bound video.
- File-manager recognition uses the same per-file numbering editor, with identical overrides in preview and execution.

## Improved

- Accepted downloads now use their frozen per-file identities during artifact inspection instead of parsing filenames a second time to make another episode decision.

## Fixed

- Upgraded the build and CI toolchain to Go 1.26.11 to fix the standard-library HTTP/1 server header race.
