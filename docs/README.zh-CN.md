# Bansoko

自托管的番剧订阅与媒体库整理工具：按配置的订阅源挑选种子，交给你自己的 qBittorrent 下载，完成后按命名模板整理进媒体库。

[English](../README.md)

## 快速开始

将下面任一配置保存为 `compose.yaml`，执行 `docker compose up -d` 即可启动（完整注释模板见 [`compose.yaml`](../compose.yaml)）。

### 方案一：内置 PostgreSQL（推荐）

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
      TZ: Asia/Shanghai
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

### 方案二：复用外置 PostgreSQL

需使用 PostgreSQL 17（目前仅验证过该版本），并提前准备专属库及具备 `CREATEROLE` 权限的库所有者账号。应用内更新和镜像接管还要求 `CREATEDB` 及终止运行时会话的权限。非超级用户的库所有者需要 `pg_signal_backend`。

```sql
CREATE ROLE <user> LOGIN PASSWORD '<password>' CREATEROLE CREATEDB;
CREATE DATABASE <db> OWNER <user>;
GRANT pg_signal_backend TO <user>;
-- 示例:
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
      TZ: Asia/Shanghai
      # BANSOKO_PORT: 8080
    volumes:
      - ./config:/config
      - ./data:/data
    ports:
      - "8000:8080"
    restart: unless-stopped
```

打开 `http://<主机>:8000`，首次访问时创建管理员账号。

## 免责声明

本软件不提供、不托管、不索引任何媒体内容；它只按用户自行配置的订阅源、下载器和目录，自动完成订阅、下载与媒体库整理。用户应自行遵守所在地区的著作权法律，以及所使用站点的条款。

## 许可

以 GPL-3.0-only 发布，见 [`LICENSE`](../LICENSE)。
