# ===== Stage 1: フロントエンドのビルド =====
FROM node:24.19.0-alpine@sha256:d32cdf619f63fe0471182d08996dd516c6275bb5fd31ae06e55a570bd9e1ad43 AS frontend-builder

WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm ci --include=dev --ignore-scripts
COPY frontend/ ./
RUN npm run build

# ===== Stage 2: バックエンドのビルド =====
FROM golang:1.26.7-alpine@sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468 AS backend-builder

# SQLCipherは公式v4.18.0 source archiveをchecksum固定でbuildする。
RUN apk add --no-cache build-base linux-headers curl tcl openssl-dev

COPY scripts/build-sqlcipher-linux.sh /tmp/build-sqlcipher-linux.sh
RUN SQLCIPHER_PREFIX=/usr/local SQLCIPHER_BUILD_JOBS=2 sh /tmp/build-sqlcipher-linux.sh

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# サーバーバイナリに必要なGoソースだけをコピーする。
# UIだけの変更でバックエンドビルドキャッシュが無効になることを避ける。
COPY server.go ./
COPY backend/ ./backend/
COPY cmd/omni-totp/ ./cmd/omni-totp/

# バージョン情報
ARG VERSION=dev

# サーバーモードでビルド（-tags server で server.go を使用）
RUN CGO_ENABLED=1 \
    CGO_CFLAGS="-I/usr/local/include" \
    CGO_LDFLAGS="-L/usr/local/lib" \
    go build \
    -tags "server libsqlite3 sqlite_omit_load_extension" \
    -ldflags "-X main.version=${VERSION} -s -w" \
    -o /omni_money_server \
    ./server.go && \
    LD_LIBRARY_PATH=/usr/local/lib /omni_money_server 2>&1 | grep -q "AUTH_PASSWORD_HASH"

# TOTP enrollment helper. This binary is copied into the runtime image so the
# secret can be created by the same non-root UID that later reads it.
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags "-s -w" \
    -o /omni-totp \
    ./cmd/omni-totp

# ===== Stage 3: 軽量ランタイム =====
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# バージョン情報を実行時環境変数として参照可能にする（§8.3準拠）
ARG VERSION=dev
ENV VERSION=${VERSION} \
    LD_LIBRARY_PATH=/usr/local/lib

# タイムゾーンとCA証明書。固定したAlpine系列内で公開済みのセキュリティ修正を適用する。
RUN apk upgrade --no-cache && \
    apk add --no-cache ca-certificates tzdata libcrypto3

# セキュリティ: 非rootユーザーで実行。UID/GIDを固定し、bind mountの
# ACLを事前に安全に設定できるようにする。
RUN addgroup -S -g 10001 omni && \
    adduser -S -D -H -u 10001 -G omni omni

WORKDIR /app

# バイナリとフロントエンド成果物をコピー
COPY --from=backend-builder /omni_money_server ./omni_money_server
COPY --from=backend-builder /usr/local/lib/libsqlite3.so /usr/local/lib/libsqlite3.so
COPY --from=backend-builder /omni-totp ./omni-totp
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist

# DB、WAL、復元用backup、snapshotはすべてこの永続directory配下へ保存する。
RUN mkdir -p /app/data /run/secrets && chown -R omni:omni /app

USER omni

# 環境変数のデフォルト値
ENV DB_PATH=/app/data/omni_money.db \
    HOST_IP=0.0.0.0 \
    PORT=4000 \
    AI_HOST_IP=127.0.0.1 \
    AI_PORT=4001 \
    AI_ALLOW_REMOTE=false

# 4001は同一コンテナ内のloopback専用AI listenerであり公開しない。
EXPOSE 4000

# ヘルスチェック
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD if [ -n "$TLS_CERT_FILE" ]; then \
          wget --no-check-certificate -qO- "https://127.0.0.1:${PORT}/healthz"; \
        else \
          wget -qO- "http://127.0.0.1:${PORT}/healthz"; \
        fi || exit 1

ENTRYPOINT ["./omni_money_server"]
