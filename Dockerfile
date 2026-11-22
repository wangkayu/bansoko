# Build stages run on the build host's platform; Go cross-compiles to the
# target, so only the PG tools and final stages execute per target arch.
FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:fe4f6d74e20370acb04ad889077d14d0f64a9c8ea6a42bc60cddf1e844699914 AS frontend-builder

WORKDIR /src/frontend
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
RUN npm install -g --force "pnpm@$(node -p "require('./package.json').packageManager.split('@')[1]")" && pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm build
# The notice list needs `go list` for the module graph, which this stage
# cannot run; its frontend half is collected here while node_modules is still
# present and handed to the Go stage.
RUN pnpm licenses list --prod --json > /tmp/frontend-licenses.json

FROM --platform=$BUILDPLATFORM golang:1.26.11-alpine@sha256:ae52b5bc6ed045a9ed26ab03dc19327a802a9cc9aa8b27ed57f5c6b60579b0bb AS go-builder
ARG TARGETOS=linux
ARG TARGETARCH

WORKDIR /src
RUN apk add --no-cache ca-certificates git python3 bash
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend-builder /src/frontend/dist ./frontend/dist
ARG VERSION=dev
ARG SOURCE_COMMIT=unknown
ARG BUILD_ID=development
ARG BUILD_TIME=unknown
ARG BUILD_DIRTY=true
ARG API_VERSION=v1
ARG SCHEMA_VERSION=
ARG BASE_COMPATIBILITY_VERSION=base-v1
RUN schema_version="${SCHEMA_VERSION}"; \
    if [ -z "$schema_version" ]; then \
      schema_version="$(for migration in db/migrations/*.sql; do \
        [ -f "$migration" ] || continue; \
        migration="${migration##*/}"; \
        printf '%s\n' "${migration%.sql}"; \
      done | sort -n | tail -n 1)"; \
    fi; \
    [ -n "$schema_version" ] || { echo "no migrations found" >&2; exit 1; }; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
    -ldflags="-s -w -buildid= -X bansoko/internal/runtime/buildinfo.Version=${VERSION} -X bansoko/internal/runtime/buildinfo.Commit=${SOURCE_COMMIT} -X bansoko/internal/runtime/buildinfo.BuildID=${BUILD_ID} -X bansoko/internal/runtime/buildinfo.BuildTime=${BUILD_TIME} -X bansoko/internal/runtime/buildinfo.BuildDirty=${BUILD_DIRTY} -X bansoko/internal/runtime/buildinfo.APIVersion=${API_VERSION} -X bansoko/internal/runtime/buildinfo.SchemaVersion=$schema_version -X bansoko/internal/runtime/buildinfo.BaseCompatibilityVersion=${BASE_COMPATIBILITY_VERSION}" \
    -o /out/bansoko-server ./cmd/bansoko-server

# Provenance the image carries. GPL-3.0-only section 6 asks a distributor who
# conveys object code to offer the Corresponding Source of that exact binary,
# and the image is conveyed from a registry with no accompanying checkout, so
# the offer has to live inside it: the file names the commit this binary was
# built from, which is the tree a recipient can read and rebuild.
RUN set -eu; \
    if printf '%s' "$SOURCE_COMMIT" | grep -Eqx '[0-9a-f]{40}'; then \
      { \
        echo '# Source for this image'; \
        echo; \
        echo 'This image was built from commit'; \
        echo; \
        echo "    $SOURCE_COMMIT"; \
        echo; \
        echo 'of the Bansoko source tree:'; \
        echo; \
        echo "    https://github.com/wangkayu/bansoko/tree/$SOURCE_COMMIT"; \
        echo; \
        echo 'That commit is the Corresponding Source for the program in this'; \
        echo 'image (GPL-3.0-only section 6). The image build does not patch or'; \
        echo 'generate source outside it: the binaries are compiled from that tree'; \
        echo 'by the Dockerfile in it, and the licence texts and the third-party'; \
        echo 'notice list beside this file describe the same build.'; \
        echo; \
        echo 'The same tree is downloadable anonymously, without any account:'; \
        echo; \
        echo "    https://github.com/wangkayu/bansoko/archive/$SOURCE_COMMIT.tar.gz"; \
        echo; \
        echo 'Read that copy, or build it again with:'; \
        echo; \
        echo "    curl -L -o bansoko.tar.gz https://github.com/wangkayu/bansoko/archive/$SOURCE_COMMIT.tar.gz"; \
        echo '    tar -xzf bansoko.tar.gz'; \
        echo "    docker build -t bansoko bansoko-$SOURCE_COMMIT"; \
      } > /out/SOURCE; \
    elif [ "$SOURCE_COMMIT" = unknown ]; then \
      { \
        echo '# Source for this image'; \
        echo; \
        echo 'This image is a local development build: it carries no source'; \
        echo 'commit, so there is no published tree to name.'; \
        echo 'Released builds always name their commit here.'; \
      } > /out/SOURCE; \
    else \
      echo "SOURCE_COMMIT is neither a full 40-character commit nor 'unknown': [$SOURCE_COMMIT]" >&2; \
      exit 1; \
    fi

# The notice list the image ships. It is generated here rather than stored in
# the tree: its input is the module graph of this exact build. A module whose
# licence text cannot be identified aborts the build, so the image never ships
# with an unlisted component.
COPY --from=frontend-builder /tmp/frontend-licenses.json /tmp/frontend-licenses.json
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH bash scripts/generators/generate-third-party-notices.sh \
      /out/THIRD_PARTY_NOTICES.md /tmp/frontend-licenses.json

# The licence text of every bundled module, collected where its source already
# is. The image is the distribution and MIT/BSD require the notice to travel
# with the binary, so the texts ship inside it rather than only in the source
# repository. A module that ships no licence file is named in the output, so the
# gap is visible instead of silent.
RUN set -eu; \
    : > /out/THIRD_PARTY_LICENSES.txt; \
    go list -m -f '{{.Path}}|{{.Dir}}' all | sort | while IFS='|' read -r path dir; do \
      [ -n "$dir" ] || continue; \
      found=0; \
      for file in "$dir"/LICENSE* "$dir"/COPYING* "$dir"/NOTICE*; do \
        [ -f "$file" ] || continue; \
        found=1; \
        { echo '================================================================================'; \
          echo "$path -- ${file##*/}"; \
          echo '================================================================================'; \
          cat "$file"; \
          echo; } >> /out/THIRD_PARTY_LICENSES.txt; \
      done; \
      [ "$found" = 1 ] || echo "NO LICENCE FILE IN MODULE: $path" >> /out/THIRD_PARTY_LICENSES.txt; \
    done; \
    [ -s /out/THIRD_PARTY_LICENSES.txt ] || { echo "no third-party licence text collected" >&2; exit 1; }

# postgres:17-alpine, pinned by its registry manifest digest. Copy only
# PostgreSQL's own files; the runtime APK repositories supply their OS libraries.
FROM postgres@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73 AS pg17-tools
RUN set -eux; \
    mkdir -p /stage/bin /stage/lib /out; \
    for tool in pg_dump pg_restore psql; do cp "/usr/local/bin/$tool" /stage/bin/; done; \
    cp -L /usr/local/lib/libpq.so.5 /stage/lib/; \
    version="$(pg_config --version | awk '{print $2}')"; \
    printf '%s\n' "$version" | grep -Eq '^17\.[0-9]+$'; \
    printf 'postgresql\t%s\tPostgreSQL\thttps://www.postgresql.org/about/licence/\n' "$version" > /out/pg17-components.tsv

# alpine:3.24.4, pinned by its multi-platform manifest digest.
FROM alpine@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf

# ARG is per stage, so the values the labels report are re-declared here; a
# label naming an undeclared ARG silently renders empty. The publish script
# passes the same values as --label, which overrides these for released images.
ARG VERSION=dev
ARG SOURCE_COMMIT=unknown
LABEL org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.source="https://github.com/wangkayu/bansoko" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$SOURCE_COMMIT"

# mediainfo reads the execution-time media facts (file.resolution, video_codec,
# audio_codec, container) of every downloaded video; it is started directly
# with the file path as one argument, never through a shell.
# Event scripts can use curl for HTTP and jq for their JSON input.
# Install the PG clients' direct OS dependencies by soname. APK resolves their
# transitive dependencies and owns the versions used by all runtime tools.
RUN apk upgrade --no-cache && \
    apk add --no-cache ca-certificates tzdata mediainfo curl jq \
    'so:libcrypto.so.3' 'so:libedit.so.0' 'so:libgssapi_krb5.so.2' \
    'so:libldap.so.2' 'so:liblz4.so.1' 'so:libssl.so.3' 'so:libz.so.1' 'so:libzstd.so.1'
WORKDIR /app

COPY --from=go-builder /out/bansoko-server /app/bansoko-server
COPY --from=go-builder /src/db/migrations /app/db/migrations
COPY --from=go-builder /src/deployment/defaults /app/deployment/defaults
COPY --from=go-builder /src/frontend/dist /app/frontend/dist
# The image is the distribution: the licence and the third-party notices
# travel with it so a recipient can read them without the source checkout
# (GPL-3.0-only section 4 requires the licence to accompany the program), and
# THIRD_PARTY_LICENSES.txt carries the full text of every bundled module.
# LICENSE's one-line header is the copyright notice section 4 asks for; the
# FSF text below it carries the warranty terms.
COPY LICENSE /app/LICENSE
COPY --from=go-builder /out/THIRD_PARTY_NOTICES.md /app/THIRD_PARTY_NOTICES.md
COPY --from=go-builder /out/THIRD_PARTY_LICENSES.txt /app/THIRD_PARTY_LICENSES.txt

# The staged files go under /usr/local only, without replacing APK-owned files.
COPY --from=pg17-tools /stage/ /usr/local/

# Add notices for the runtime APK packages and the pinned PostgreSQL files.
# Their licence texts ship from deployment/licenses. Missing licence metadata
# or text aborts the build.
COPY --from=pg17-tools /out/pg17-components.tsv /tmp/pg17-components.tsv
COPY --from=go-builder /src/scripts/generators/generate-base-image-notices.sh /tmp/generate-base-image-notices.sh
COPY deployment/licenses /tmp/licenses
RUN sh /tmp/generate-base-image-notices.sh /app/THIRD_PARTY_NOTICES.md /tmp/pg17-components.tsv /tmp/licenses /app/THIRD_PARTY_LICENSES.txt && \
    rm -rf /tmp/generate-base-image-notices.sh /tmp/pg17-components.tsv /tmp/licenses && \
    chmod 0444 /app/THIRD_PARTY_NOTICES.md /app/THIRD_PARTY_LICENSES.txt
# The GPL source offer for this exact binary (generated in the Go stage, which
# is where the build's SOURCE_COMMIT is in scope).
COPY --from=go-builder /out/SOURCE /app/SOURCE

# Fail-closed toolchain verification: every copied PG binary must resolve all
# dynamic deps and answer --version with major 17. A silent half-working
# pg_dump would break the update backup contract later.
RUN set -eux; \
    for tool in pg_dump pg_restore psql; do \
      ldd "/usr/local/bin/$tool"; \
      "/usr/local/bin/$tool" --version | grep -q " 17\."; \
    done

# Fail-closed verification of the media tool: it must resolve every dynamic
# dependency and answer --Version. The installed Alpine package version is
# printed into the build log as build evidence.
RUN set -eux; \
    ldd /usr/bin/mediainfo; \
    mediainfo --Version | grep -q "MediaInfoLib"; \
    apk list --installed mediainfo libmediainfo

RUN set -eu; \
    curl --version; \
    jq --version; \
    printf '%s\n' '{"facts":{"out.path":"/library/example.mkv"}}' | \
      jq -e '.facts["out.path"] == "/library/example.mkv"'

# The container starts as root: the unified binary's bootstrap mode prepares
# updates root for PUID:PGID (default 1000:1000) and then drops to that account
# for good before migrating or serving. /app stays root-owned and read-only to
# the app.
RUN mkdir -p /config

# Image defaults users never set. Updates are on by default: a deployment names
# the lane it belongs to and the lane names this project's own channel branch
# (stable = tagged releases, test = builds of main), so an unconfigured
# deployment updates itself in place from the stable lane (the image ENV counts
# as the explicit mode the production config requires). Set
# BANSOKO_UPDATE_MODE=manual to pin a deployment to image pulls, or
# BANSOKO_UPDATE_LANE=test on a test instance. The server defaults to listening
# on 0.0.0.0:8080 (override with BANSOKO_PORT or BANSOKO_LISTEN_ADDRESS) and
# stores embedded updates at /config/updates (<BANSOKO_CONFIG_DIR>/updates,
# override with BANSOKO_UPDATES_ROOT). The only required input is
# BANSOKO_DATABASE_URL (schema owner); secrets are generated into /config on
# first start. Until a release is published on the lane, a check reports itself
# unavailable.
ENV BANSOKO_ENVIRONMENT=production \
    BANSOKO_UPDATE_MODE=embedded \
    BANSOKO_UPDATE_LANE=stable \
    BANSOKO_SCHEMA_DIR=/app/db/migrations \
    BANSOKO_FRONTEND_DIR=/app/frontend/dist

EXPOSE 8080
# P0.7: release/restore admission must not depend on DB-ping /system/health;
# /livez proves process liveness only (readiness is /readyz).
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD port="${BANSOKO_PORT:-8080}"; if [ -n "${BANSOKO_LISTEN_ADDRESS:-}" ]; then port="${BANSOKO_LISTEN_ADDRESS##*:}"; fi; wget --no-verbose --tries=1 --spider "http://127.0.0.1:${port}/api/v1/livez" || exit 1
# The unified binary starts in bootstrap mode by default: bare start migrates
# the selected tree and then runs the server. Explicit serve and migrate modes
# are used by bootstrap after it has selected and verified the runtime tree.
ENTRYPOINT ["/app/bansoko-server"]
CMD ["bootstrap"]
