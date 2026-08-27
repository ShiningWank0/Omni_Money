# ===== Stage 1: フロントエンドのビルド =====
FROM node:24.18.0-alpine@sha256:a0b9bf06e4e6193cf7a0f58816cc935ff8c2a908f81e6f1a95432d679c54fbfd AS frontend-builder

WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm ci --include=dev
COPY frontend/index.html frontend/vite.config.js ./
COPY frontend/src ./src
RUN npm run build

# ===== Stage 2: バックエンドのビルド =====
FROM golang:1.25.12-alpine3.24@sha256:56961d79ea8129efddcc0b8643fd8a5416b4e6228cfd477e3fd61deb2672c587 AS backend-builder

# CGO有効化（SQLite用）
RUN apk add --no-cache gcc musl-dev

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# サーバービルドに必要なソースだけをallow-listでコピーする。
# ランタイムDB、WAL、secret、任意の作業ファイルをbuilder layerへ持ち込まない。
COPY server.go ./
COPY backend ./backend

# バージョン情報
ARG VERSION=dev

# サーバーモードでビルド（-tags server で server.go を使用）
RUN CGO_ENABLED=1 go build \
    -tags server \
    -ldflags "-X main.version=${VERSION} -s -w" \
    -o /omni_money_server \
    ./server.go

# ===== Stage 3: 軽量ランタイム =====
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# バージョン情報を実行時環境変数として参照可能にする（§8.3準拠）
ARG VERSION=dev
ENV VERSION=${VERSION}

# タイムゾーンとCA証明書
RUN apk add --no-cache ca-certificates tzdata

# セキュリティ: 非rootユーザーで実行
RUN addgroup -S omni && adduser -S omni -G omni

WORKDIR /app

# バイナリとフロントエンド成果物をコピー
COPY --from=backend-builder /omni_money_server ./omni_money_server
COPY --from=frontend-builder /app/frontend/dist ./frontend/dist

# DB、WAL、復元用backup、snapshotはすべてこの永続directory配下へ保存する。
RUN mkdir -p /app/data && chown -R omni:omni /app

USER omni

# 環境変数のデフォルト値
ENV DB_PATH=/app/data/omni_money.db \
    HOST_IP=0.0.0.0 \
    PORT=4000 \
    AI_HOST_IP=127.0.0.1 \
    AI_PORT=4001 \
    AI_ALLOW_REMOTE=false

EXPOSE 4000 4001

# ヘルスチェック
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD if [ -n "$TLS_CERT_FILE" ]; then \
          wget --no-check-certificate -qO- "https://127.0.0.1:${PORT}/healthz"; \
        else \
          wget -qO- "http://127.0.0.1:${PORT}/healthz"; \
        fi || exit 1

ENTRYPOINT ["./omni_money_server"]
