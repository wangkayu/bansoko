#!/usr/bin/env bash
# Read-only API compression probe: measures TTFB, transferred bytes, and
# Content-Encoding for GET endpoints with and without gzip, plus the first
# event latency of the SSE stream. Use it before and after deploying response
# compression (or a proxy change) to compare the same deployment.
#
# Usage:
#   BANSOKO_BASE_URL=http://127.0.0.1:8000 scripts/verify/api-compression-probe.sh
#   BANSOKO_BASE_URL=https://example:8000 BANSOKO_TOKEN=<read-only token> \
#     scripts/verify/api-compression-probe.sh /api/v1/anime /api/v1/anime/<id>/workspace
#
# Environment:
#   BANSOKO_BASE_URL   required; no trailing slash, no path prefix
#   BANSOKO_TOKEN      optional bearer token (read-only scope is enough)
#   BANSOKO_PROBE_SSE  set to 0 to skip the /events/stream first-event check
#
# The script only issues GET requests, never logs the token, and removes its
# temporary files on exit.
set -euo pipefail

: "${BANSOKO_BASE_URL:?set BANSOKO_BASE_URL to the server origin (no trailing slash)}"
base="${BANSOKO_BASE_URL%/}"
paths=("$@")
if [ "${#paths[@]}" -eq 0 ]; then
	paths=(/api/v1/anime)
fi

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

auth=()
if [ -n "${BANSOKO_TOKEN:-}" ]; then
	auth=(-H "Authorization: Bearer ${BANSOKO_TOKEN}")
fi

printf '%-58s %8s %12s %12s  %s\n' PATH TTFB_s TRANSFER_B RAW_B ENCODING

probe() {
	local path="$1" accept="$2"
	local body="$workdir/body" headers="$workdir/headers"
	local args=(curl -sS --max-time 60 -o "$body" -D "$headers"
		-w '%{time_starttransfer} %{size_download}'
		-H 'Accept: application/json' "${auth[@]}")
	if [ "$accept" = gzip ]; then
		args+=(-H 'Accept-Encoding: gzip')
	fi
	local stats
	stats="$("${args[@]}" "${base}${path}")" || {
		printf '%-58s request failed (%s)\n' "${path} [${accept}]" "$?"
		return 0
	}
	local encoding raw
	encoding="$(awk 'BEGIN{IGNORECASE=1} /^Content-Encoding:/{gsub(/\r/,"",$2); print $2}' "$headers")"
	if [ -n "$encoding" ]; then
		raw="$(gzip -dc "$body" 2>/dev/null | wc -c | tr -d ' ')"
	else
		encoding="-"
		raw="$(wc -c <"$body" | tr -d ' ')"
	fi
	printf '%-58s %8s %12s %12s  %s\n' "${path} [${accept}]" \
		"${stats% *}" "${stats#* }" "$raw" "$encoding"
}

for path in "${paths[@]}"; do
	probe "$path" identity
	probe "$path" gzip
done

if [ "${BANSOKO_PROBE_SSE:-1}" != 0 ]; then
	# First-event latency: -N disables curl buffering; time_starttransfer is
	# when the response headers arrive, and the stream must send its initial
	# ": connected" comment immediately. A compressed SSE response would
	# instead hang until timeout.
	sse_out="$workdir/sse"
	set +e
	stats="$(curl -sS -N --max-time 5 -o "$sse_out" \
		-w '%{time_starttransfer}' \
		-H 'Accept: text/event-stream' -H 'Accept-Encoding: gzip' \
		"${auth[@]}" "${base}/api/v1/events/stream" 2>/dev/null)"
	rc=$?
	set -e
	first_line="$(head -c 64 "$sse_out" 2>/dev/null | head -n 1)"
	printf '%-58s %8s  first line: %s (curl rc=%d; rc=28 means the stream stayed open, which is expected)\n' \
		'/api/v1/events/stream [gzip]' "$stats" "$first_line" "$rc"
fi
