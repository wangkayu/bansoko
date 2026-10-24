# Bansoko

自托管的番剧订阅与媒体库整理工具：按配置的订阅源挑选种子，交给你自己的 qBittorrent 下载，完成后按命名模板整理进媒体库。

[English](../README.md)

## 快速开始

测试镜像发布在 Docker Hub：`wangkayu/bansoko`（linux/amd64、linux/arm64）。只需要 Docker 和 Compose。

```bash
git clone https://github.com/wangkayu/bansoko.git
cd bansoko
docker compose up -d
```

打开 `http://<主机>:8000`，首次访问时创建管理员账号。之后的起步步骤：在设置里连接
qBittorrent，添加订阅源，订阅一部番剧。测试阶段的数据可能在版本之间被重置。

## 免责声明

本软件不提供、不托管、不索引任何媒体内容；它只按用户自行配置的订阅源、下载器和目录，自动完成订阅、下载与媒体库整理。用户应自行遵守所在地区的著作权法律，以及所使用站点的条款。

## 许可

以 GPL-3.0-only 发布，见 [`LICENSE`](../LICENSE)。
