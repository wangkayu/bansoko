## Improved

- The embedded updater no longer requires deployment/defaults/naming-vars.js in staged payloads (while continuing to accept transition payloads that still carry it) ahead of the 0.3 retirement.
- Removed the dead hidden frontend custom naming Vars editor code.

## Fixed

- File maintenance and path calibration no longer refuse historical records solely because their frozen Vars evaluation is missing or not ok; templates that do not reference vars.* now render normally.

## Important

- Custom naming Vars (vars.*) is scheduled for retirement in 0.3. Server startup now scans all editable naming and notification templates and logs a deprecation warning if any reference vars.*; operators can also run bansoko-server migrate check-vars (with optional --json) to list every affected template before upgrading to 0.3.
