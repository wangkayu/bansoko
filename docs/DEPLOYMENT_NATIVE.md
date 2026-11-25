# Native deployment

Bansoko runs directly on Linux (x86-64 and ARM64) and macOS (Apple silicon). The release archive contains the server, Web UI, SQL migrations, and MediaInfo. Run PostgreSQL 17 and qBittorrent as separate services.

## Install a release

Download the archive for your system and `SHA256SUMS.txt` from the same [GitHub Release](https://github.com/wangkayu/bansoko/releases). Choose `linux-amd64` for x86-64, `linux-arm64` for ARM64, or `darwin-arm64` for Apple silicon. Replace the example version and platform with the ones you downloaded.

```sh
archive=bansoko-0.3.0-linux-amd64.tar.gz
grep -F "  $archive" SHA256SUMS.txt | shasum -a 256 -c -
tar -xzf "$archive"
sudo install -m 0755 bansoko-server /usr/local/bin/bansoko-server
/usr/local/bin/bansoko-server version
```

Keep the archive's `LICENSE`, `SOURCE`, and third-party notices with the installation. On macOS, you can use an absolute path under your home directory in place of `/usr/local/bin/bansoko-server`.

## Configure PostgreSQL and qBittorrent

Create an empty UTF-8 database in a dedicated PostgreSQL 17 cluster. Install the `pgcrypto` extension package if your PostgreSQL distribution ships it separately. Set `BANSOKO_DATABASE_URL` to the database owner's connection string. The current migrations and role provisioning issue `ALTER ROLE … NOSUPERUSER`, which requires the migration connection to have `SUPERUSER` in PostgreSQL 17. Bansoko creates `bansoko_app` as a separate, restricted serving login. Give the service account exclusive read access to the owner credentials. Use a TLS-verified connection for remote PostgreSQL.

For Linux in-app updates and backup or restore, install the PostgreSQL 17 client tools. On Debian and Ubuntu use `postgresql-client-17` from PGDG. On RHEL and Fedora use `postgresql17` from PGDG. On macOS use Homebrew's `postgresql@17`. `doctor` checks `pg_dump` and `pg_restore` in the standard package locations. Missing tools block update apply and are reported by `doctor`. The server can still start and serve requests. The installed binary includes MediaInfo and needs no separate media inspection package.

Enable the qBittorrent Web UI and make it reachable from Bansoko. After signing in, add the qBittorrent connection in Settings. Give the Bansoko and qBittorrent OS accounts access to the configured download paths. Give Bansoko write permission on the library and download directories used for file operations.

## Run the managed server

Set `BANSOKO_DATABASE_URL` in the service environment or in your shell. Run the preflight and start using the same account and configuration directory.

```sh
bansoko-server doctor --config-dir /var/lib/bansoko
bansoko-server start --config-dir /var/lib/bansoko
```

The configuration directory must be writable. Resolution uses `--config-dir`, then `BANSOKO_CONFIG_DIR`, then an existing writable `/config`, then the operating system's per-user directory. The per-user default is `$XDG_CONFIG_HOME/bansoko` or `~/.config/bansoko` on Linux, and `~/Library/Application Support/bansoko` on macOS.

`start` prepares `secrets.json`, applies migrations, provisions the runtime role, and launches the server. Keep `secrets.json` with your database backups. It contains the encryption key, session secret, and runtime database credential. The default listener is `0.0.0.0:8080`. Set `BANSOKO_LISTEN_ADDRESS=127.0.0.1:8080` for a local reverse proxy. Open the Web UI to create the first administrator, and check `/api/v1/readyz` for readiness.

### Linux with systemd

Create a dedicated `bansoko` user and group. Copy [`bansoko-start.service`](../deployment/native/bansoko-start.service) to `/etc/systemd/system/`. Create `/etc/bansoko/bansoko.env` with mode `0600` and put `BANSOKO_DATABASE_URL` in it. The unit creates `/var/lib/bansoko` for the service account. Add settings such as `BANSOKO_LISTEN_ADDRESS` to the same environment file.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now bansoko-start.service
sudo journalctl -u bansoko-start.service -f
```

Managed startup defaults to embedded updates on Linux. Check or apply an update from Settings or via `bansoko-server update check|status|apply`. Updates run only on request. `Restart=always` lets the service restart after the updater stages the next release. Keep the installed binary at its original path and keep the configuration directory and its `updates` subdirectory across restarts. Bootstrap uses those files to verify the staged release and roll back if upgrade validation fails.

For manual updates, set `BANSOKO_UPDATE_MODE=manual`. For a fresh deployment with separate migration and serving commands, use [`bansoko-split.service`](../deployment/native/bansoko-split.service) in place of `bansoko-start.service`. The split unit runs `migrate` before `serve`, and `serve` refuses an incompatible schema. Supply the same config directory and database connection to both commands. Run only one unit for a given installation. On an installation already using an in-app update, check `bansoko-server recover status` and plan the manual upgrade using the currently running version before replacing the installed binary or changing supervisors.

### macOS with launchd

Use [`com.bansoko.server.plist`](../deployment/native/com.bansoko.server.plist) as a per-user LaunchAgent. Copy it to `~/Library/LaunchAgents/`, replace the database connection value, and use absolute values for the binary, configuration directory, and log paths. Set mode `0600` on the edited plist. Run `doctor` with the same connection and config directory before loading it.

```sh
plutil -lint ~/Library/LaunchAgents/com.bansoko.server.plist
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.bansoko.server.plist
launchctl print "gui/$(id -u)/com.bansoko.server"
```

macOS uses manual updates. To upgrade, back up the database and config directory, stop the LaunchAgent with `launchctl bootout "gui/$(id -u)" ~/Library/LaunchAgents/com.bansoko.server.plist`, replace the binary, and bootstrap the LaunchAgent again. For a split installation, run `migrate` before restarting `serve`.
