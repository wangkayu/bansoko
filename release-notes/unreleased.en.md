## Improved

- Automatic metadata matching now uses a local bangumi-data snapshot first to resolve Bangumi, Mikan and TMDB mappings. The snapshot checks for updates every six hours in the background and keeps the old data after a failed refresh; subscriptions with an existing Bangumi ID can use it directly on later match attempts.
- TMDB season matching now considers bangumi-data's season reference, the season number in the original title, season premiere dates and episode air dates. If the requested season is absent and the dates clearly conflict, matching waits instead of falling back to the latest season. Episode dates confirm only the season and never set an EP offset automatically.
- Auto Search in the metadata match dialog now runs the automatic matching pipeline and preselects the resolved work and TMDB season, leaving final confirmation to the user. Search still lists direct candidates for the current input.
