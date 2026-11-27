# Bansoko

Self-hosted anime subscription and library tool: it picks releases from the
feed sources you configure, hands them to your own qBittorrent, and files the
finished files into your library with a naming template.

[中文说明](docs/README.zh-CN.md)

## Quick start

Save either snippet below as `compose.yaml` and run `docker compose up -d`
(see [`compose.yaml`](compose.yaml) for the full annotated template).

### Option 1: Bundled PostgreSQL (recommended)

```yaml
services:
  postgres:
    image: postgres:17-alpine
    environment:
      POSTGRES_DB: bansoko
      POSTGRES_USER: bansoko
      POSTGRES_PASSWORD: bansoko
      PGDATA: /var/lib/postgresql/data/pgdata
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U bansoko -d bansoko"]
      interval: 10s
      timeout: 5s
      retries: 10
    restart: unless-stopped

  app:
    image: wangkayu/bansoko:latest
    environment:
      BANSOKO_DATABASE_URL: postgres://bansoko:bansoko@postgres:5432/bansoko?sslmode=disable
      PUID: 1000
      PGID: 1000
      TZ: Etc/UTC
      # BANSOKO_PORT: 8080
    volumes:
      - ./config:/config
      - ./data:/data
    ports:
      - "8000:8080"
    depends_on:
      postgres:
        condition: service_healthy
    restart: unless-stopped

volumes:
  postgres_data:
```

### Option 2: External PostgreSQL

Requires PostgreSQL 17 (the only version verified). Prepare a dedicated database
and owner role with `CREATEROLE`. Embedded updates and image takeover also
require `CREATEDB` and the ability to terminate runtime sessions
(`pg_signal_backend` for a non-superuser database owner):

```sql
CREATE ROLE <user> LOGIN PASSWORD '<password>' CREATEROLE CREATEDB;
CREATE DATABASE <db> OWNER <user>;
GRANT pg_signal_backend TO <user>;
-- Example:
-- CREATE ROLE bansoko LOGIN PASSWORD 'your-password' CREATEROLE CREATEDB;
-- CREATE DATABASE bansoko OWNER bansoko;
```

```yaml
services:
  app:
    image: wangkayu/bansoko:latest
    environment:
      BANSOKO_DATABASE_URL: postgres://<user>:<password>@<host>:5432/<db>?sslmode=disable
      # BANSOKO_DATABASE_URL: postgres://bansoko:your-password@192.168.1.10:5432/bansoko?sslmode=disable
      PUID: 1000
      PGID: 1000
      TZ: Etc/UTC
      # BANSOKO_PORT: 8080
    volumes:
      - ./config:/config
      - ./data:/data
    ports:
      - "8000:8080"
    restart: unless-stopped
```

Open `http://<host>:8000` and create the administrator account on the first
visit.

## Disclaimer

This software does not provide, host or index any media content; it only
automates subscription, download and library filing according to the sources,
downloader and directories the user configures. Users are responsible for
complying with the copyright law of their jurisdiction and with the terms of
the sites they use.

## License

GPL-3.0-only, see [`LICENSE`](LICENSE).
