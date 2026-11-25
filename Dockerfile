# すべてのartifactで共有するrelease version。各stageで再宣言してscopeへ取り込む。
ARG VERSION=dev

# ===== Stage 1: フロントエンドのビルド =====
FROM node:26.13.1-alpine@sha256:6bd9ab43c0ec0190ca7b2d193906cb8b7edf6e7f4afa43ddeccd6452710bffb5 AS frontend-builder

ARG VERSION
ARG TARGETARCH

WORKDIR /app/frontend
COPY frontend/package*.json ./
COPY frontend/vendor/picomatch/ ./vendor/picomatch/
RUN npm ci --include=dev --ignore-scripts
COPY frontend/ ./
COPY LICENSE /app/LICENSE
COPY licenses/approved.json /app/licenses/approved.json
COPY licenses/targets.json /app/licenses/targets.json
COPY scripts/oss-notices.mjs /app/scripts/oss-notices.mjs
COPY scripts/verify-picomatch-patch.mjs /app/scripts/verify-picomatch-patch.mjs
COPY scripts/security-patches/picomatch.json /app/scripts/security-patches/picomatch.json
RUN VITE_APP_VERSION="$VERSION" VITE_OSS_TARGET="server-linux-${TARGETARCH}" npm run build
RUN node /app/scripts/oss-notices.mjs package "server-linux-${TARGETARCH}"

# ===== Stage 2: バックエンドのビルド =====
FROM golang:1.27.4-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS go-toolchain
FROM ghcr.io/shiningwank0/omni_money-build-alpine-builder@sha256:d377e73c51954b514e3cefa41b688b15f8f753e2a7aea68d85474c90d6828379 AS backend-builder

# Go updates remain independent of the frozen OS/package environment.
RUN rm -rf /usr/local/go
COPY --from=go-toolchain /usr/local/go /usr/local/go
ENV GOTOOLCHAIN=local

ARG VERSION

# The saved environment already contains C/Tcl/OpenSSL and their dependency closure.
RUN test "$(cut -d. -f1,2 /etc/alpine-release)" = 3.24 \
 && apk info -v libcrypto3 > /opt/omni-environment/libcrypto3.version

COPY scripts/build-sqlcipher-linux.sh /tmp/build-sqlcipher-linux.sh
RUN SQLCIPHER_PREFIX=/usr/local SQLCIPHER_BUILD_JOBS=2 sh /tmp/build-sqlcipher-linux.sh

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# サーバーバイナリに必要なGoソースだけをコピーする。
# UIだけの変更でバックエンドビルドキャッシュが無効になることを避ける。
COPY server.go ./
COPY backend/ ./backend/
COPY cmd/verify-public-assets/ ./cmd/verify-public-assets/
COPY cmd/verify-licenses/ ./cmd/verify-licenses/
RUN CGO_ENABLED=0 go build -trimpath -o /verify-public-assets ./cmd/verify-public-assets
RUN CGO_ENABLED=0 go build -trimpath -o /verify-licenses ./cmd/verify-licenses

# サーバーモードでビルド（-tags server で server.go を使用）
RUN CGO_ENABLED=1 \
    CGO_CFLAGS="-I/usr/local/include" \
    CGO_LDFLAGS="-L/usr/local/lib" \
    go build \
    -tags "server libsqlite3 sqlite_omit_load_extension" \
    -ldflags "-X main.version=${VERSION} -s -w" \
    -o /omni_money_server \
    ./server.go && \
    LD_LIBRARY_PATH=/usr/local/lib /omni_money_server 2>&1 | grep -q "CONTROL_DB_PATH is required"

# ===== Stage 3: 軽量ランタイム =====
FROM ghcr.io/shiningwank0/omni_money-build-alpine-runtime@sha256:87111362c801d41c984a7658dacc15d34e714a662a199e273c0694564c4d617e

# バージョン情報を実行時環境変数として参照可能にする（§8.3準拠）
ARG VERSION
ARG TARGETARCH
ENV VERSION=${VERSION} \
    LD_LIBRARY_PATH=/usr/local/lib

# No package resolution during an application build. Require matching libcrypto
# and Alpine series rather than accepting a different runtime provider silently.
RUN --mount=type=bind,from=backend-builder,source=/opt/omni-environment/libcrypto3.version,target=/tmp/builder-libcrypto.version,ro \
    test "$(cut -d. -f1,2 /etc/alpine-release)" = 3.24 \
 && apk info -v libcrypto3 > /tmp/runtime-libcrypto.version \
 && cmp /tmp/builder-libcrypto.version /tmp/runtime-libcrypto.version \
 && rm /tmp/runtime-libcrypto.version

# セキュリティ: 非rootユーザーで実行。UID/GIDを固定し、bind mountの
# ACLを事前に安全に設定できるようにする。
RUN addgroup -S -g 10001 omni && \
    adduser -S -D -H -u 10001 -G omni omni

WORKDIR /app

# バイナリとフロントエンド成果物をコピー
COPY --from=backend-builder /omni_money_server ./omni_money_server
COPY --from=backend-builder /usr/local/lib/libsqlite3.so /usr/local/lib/libsqlite3.so
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist
COPY --from=frontend-builder /app/frontend/public-assets-manifest.json ./frontend/public-assets-manifest.json
COPY --from=frontend-builder /app/licenses/build/server-linux-${TARGETARCH}/ ./licenses/
COPY --from=backend-builder /usr/local/share/licenses/sqlcipher/LICENSE.md ./licenses/native/SQLCipher-LICENSE.txt
COPY scripts/record-image-licenses.sh /app/record-image-licenses.sh

# DB、WAL、復元用backup、snapshotはすべてこの永続directory配下へ保存する。
RUN --mount=type=bind,from=backend-builder,source=/verify-public-assets,target=/tmp/verify-public-assets,ro \
    --mount=type=bind,from=backend-builder,source=/verify-licenses,target=/tmp/verify-licenses,ro \
    --mount=type=bind,from=frontend-builder,source=/app/licenses/build/server-linux-${TARGETARCH}/manifest.json,target=/tmp/license-manifest.json,ro \
    /tmp/verify-licenses "server-linux-${TARGETARCH}" && \
    sh /app/record-image-licenses.sh /app/licenses && \
    rm /app/record-image-licenses.sh && \
    /tmp/verify-public-assets /app/frontend/dist /app/frontend/public-assets-manifest.json && \
    mkdir -p /app/data /run/secrets && chown -R omni:omni /app

USER omni

# 環境変数のデフォルト値
ENV CONTROL_DB_PATH=/app/data/control/omni_control.db \
    VAULT_ROOT=/app/data/vaults \
    HOST_IP=0.0.0.0 \
    PORT=4000

EXPOSE 4000

# ヘルスチェック
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD if [ -n "$TLS_CERT_FILE" ]; then \
          wget --no-check-certificate -qO- "https://127.0.0.1:${PORT}/healthz"; \
        else \
          wget -qO- "http://127.0.0.1:${PORT}/healthz"; \
        fi || exit 1

ENTRYPOINT ["./omni_money_server"]
