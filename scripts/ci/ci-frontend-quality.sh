#!/usr/bin/env bash
# Unit/Static lane (frontend part): typecheck, lint, unit tests, build.
# Part of the same required lane as ci-unit-static.sh but runs on a Node image.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$root/frontend"

# CI 缓存可能携带不完整的 node_modules（例如缺少 rollup 的 platform 原生
# 二进制 @rollup/rollup-linux-x64-gnu）。pnpm 会依据 .modules.yaml 误判
# "Already up to date" 而跳过缺失的 optional 依赖，导致 vitest/rollup 启动
# 失败。删除 node_modules 后从 store/registry 完整重建，保证依赖完整。
rm -rf node_modules
pnpm install --frozen-lockfile
pnpm typecheck
pnpm lint
pnpm test:unit
pnpm build

echo "Unit/static lane (frontend) passed"
