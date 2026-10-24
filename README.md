# Bansoko

Self-hosted anime subscription and library tool: it picks releases from the
feed sources you configure, hands them to your own qBittorrent, and files the
finished files into your library with a naming template.

[中文说明](docs/README.zh-CN.md)

## Quick start

The test image is published on Docker Hub as `wangkayu/bansoko`
(linux/amd64, linux/arm64). All you need is Docker and Compose.

```bash
git clone https://github.com/wangkayu/bansoko.git
cd bansoko
docker compose up -d
```

Open `http://<host>:8000` and create the administrator account on the first
visit. First steps after that: connect qBittorrent in Settings, add a feed
source, subscribe to a title. Test-stage data may be reset between versions.

## Disclaimer

This software does not provide, host or index any media content; it only
automates subscription, download and library filing according to the sources,
downloader and directories the user configures. Users are responsible for
complying with the copyright law of their jurisdiction and with the terms of
the sites they use.

## License

GPL-3.0-only, see [`LICENSE`](LICENSE).
