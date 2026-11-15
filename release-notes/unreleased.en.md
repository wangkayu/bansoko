## New

- Library import completion and execution failure notifications support original file-name attributes through frt.*. File displacement notifications can show both files using frt.* and taker.frt.*.

## Improved

- Torrent structure summaries show names and sizes on separate lines without relying on monospace alignment. Long names and control characters no longer disrupt entry layout.
- System-default import notifications include the original file group. File displacement notifications show each file's group and specifications separately. Missing-value lines are hidden in both English and Chinese templates.

## Fixed

- Original file-name attributes remain with managed files after operation details are cleared, so later replacement notifications can still compare the two versions.

## Important

- Notification targets using system defaults adopt the new wording; custom templates remain unchanged.
- frt.* is supported only in naming, library import completion, execution failure and file displacement. Existing references in other events must be removed. taker.frt.* is supported only in file displacement.
- Older records without stored file-name attributes remain empty; renamed library files are not reparsed. Notifications for preparation-stage naming failures are outside this change.
