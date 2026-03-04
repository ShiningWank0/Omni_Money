# ===== Stage 1: フロントエンドのビルド =====
FROM node:20-alpine AS frontend-builder

WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm ci --production=false
COPY frontend/ ./
RUN npm run build

# ===== Stage 2: バックエンドのビルド =====
FROM golang:1.24-alpine AS backend-builder

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

# ===== Stage 3: 軽量ランタイム =====
FROM alpine:3.21

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

# スナップショット・データベース用ディレクトリ
RUN mkdir -p /app/data /app/snapshots && chown -R omni:omni /app

USER omni

# 環境変数のデフォルト値
ENV DB_PATH=/app/data/omni_money.db \
    HOST_IP=0.0.0.0 \
    PORT=4000

EXPOSE 4000

# ヘルスチェック
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- http://localhost:4000/api/accounts || exit 1

ENTRYPOINT ["./omni_money_server"]
