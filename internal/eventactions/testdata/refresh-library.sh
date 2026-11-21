#!/bin/sh

emby_url='http://emby:8096'
emby_token='DEMO_TOKEN'

report() {
  jq -cn --arg summary "$1" --arg status "${2:-}" '
    {version: 1, summary: $summary}
    + (if $status == "" then {} else {http_status: ($status | tonumber)} end)
  '
}

jq -e '.event == "file_operation_completed"' >/dev/null || exit 1

if status=$(
  printf 'X-Emby-Token: %s\n' "$emby_token" |
    curl -q --silent --show-error --max-time 10 --header @- --request POST \
      --output /dev/null --write-out '%{http_code}' "$emby_url/Library/Refresh"
); then
  case "$status" in
    2[0-9][0-9]) report 'Emby 已接受媒体库刷新请求' "$status"; exit 0 ;;
    [1-5][0-9][0-9]) report 'Emby 刷新请求返回非成功状态' "$status"; exit 1 ;;
    *) report '未取得有效的 HTTP 状态'; exit 1 ;;
  esac
else
  rc=$?
  report '未能完成 Emby 刷新请求'
  exit "$rc"
fi
