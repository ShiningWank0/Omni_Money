# ===== Stage 1: フロントエンドのビルド =====
FROM node:24.19.0-alpine@sha256:d32cdf619f63fe0471182d08996dd516c6275bb5fd31ae06e55a570bd9e1ad43 AS frontend-builder

WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm ci --include=dev --ignore-scripts
COPY frontend/ ./
RUN npm run build

# ===== Stage 2: バックエンドのビルド =====
FROM golang:1.26.7-alpine@sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468 AS backend-builder

# CGO有効化（SQLite用）
RUN apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# ソースコードをコピー
COPY . .

# フロントエンドのビルド成果物をコピー（go:embed用ではなくサーバーモード用）
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist

# バージョン情報
ARG VERSION=dev

# サーバーモードでビルド（-tags server で server.go を使用）
RUN CGO_ENABLED=1 go build \
    -tags server \
    -ldflags "-X main.version=${VERSION} -s -w" \
    -o /omni_money_server \
    ./server.go

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
ENV VERSION=${VERSION}

# タイムゾーンとCA証明書
RUN apk add --no-cache ca-certificates tzdata

# セキュリティ: 非rootユーザーで実行。UID/GIDを固定し、bind mountの
# ACLを事前に安全に設定できるようにする。
RUN addgroup -S -g 10001 omni && \
    adduser -S -D -H -u 10001 -G omni omni

WORKDIR /app

# バイナリとフロントエンド成果物をコピー
COPY --from=backend-builder /omni_money_server ./omni_money_server
COPY --from=backend-builder /omni-totp ./omni-totp
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist

# スナップショット・データベース用ディレクトリ
RUN mkdir -p /app/data /app/snapshots && chown -R omni:omni /app

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
