## Improved

- App updates are now a GitHub connection in Connections: "Update URL" can point at your own reverse proxy, and the egress is picked from "Proxy", so a proxy's address and credentials are maintained only under "Proxy". The URL defaults to the official address and takes effect on the next update once saved; disabling the connection or the selected proxy sends updates back to the official address directly. "Test" reads the update channel through the current URL and proxy and shows the result in the row.
- When picking seeds for a one-shot download, a selection belongs to the subtitle group you are browsing: after switching to another group the earlier picks are no longer kept, the selected count starts again from zero, and returning to a group you already opened does not restore them.
- Season 0 now names its folder Specials, so the full path is /Specials. Files already imported keep their current name; only newly imported files use Specials.
- The owner password minimum length is now 8 characters.
