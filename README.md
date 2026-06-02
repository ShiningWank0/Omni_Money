# Omni Money

Omni Money は、Go と Vue.js で構築された家計簿アプリケーションです。
Wails によるデスクトップアプリとして使えるほか、Docker でサーバーモードとして起動し、ブラウザから利用することもできます。

旧 Python 版の `legacy_reference/` を参照しながら、取引管理、複数口座管理、CSV バックアップ、スナップショット復元、タグ分析、AI 向け API などを Go/Vue 構成へ移行しています。

## 主な機能

- 収入・支出の取引登録、編集、削除
- 複数口座の登録、選択表示、合算残高表示
- 項目名とメモを対象にした取引検索
- 取引日時、項目、種別、金額、残高、メモの管理
- クレジットカード扱いの項目を残高計算とグラフから除外
- CSV バックアップと CSV インポート
- 残高推移グラフ
- SQLite データベースのスナップショット作成、一覧表示、復元
- 取引画像の添付、一覧取得、削除
- 最大 3 階層のタグ管理とタグ別円グラフ分析
- 取引同士の紐付け
- AI エージェント向けの取引追加 API と分析 API
- GitHub Actions による VERSION 起点のデスクトップ版リリースと Docker イメージリリース

## 技術スタック

- Backend: Go, SQLite
- Frontend: Vue 3, Vite, Pinia, Chart.js
- Desktop: Wails
- Server: Go HTTP server, Docker
- CI/CD: GitHub Actions

## ディレクトリ構成

```text
.
├── backend/              # Go バックエンド
│   ├── api/              # サーバーモード用 REST API
│   ├── core/             # ビジネスロジック
│   ├── database/         # SQLite 初期化、スナップショット
│   ├── middleware/       # AI API 認証など
│   └── models/           # データモデル
├── frontend/             # Vue フロントエンド
│   └── src/
│       ├── components/   # 画面部品
│       ├── store/        # Pinia store
│       └── utils/        # Wails/API 通信ラッパー
├── legacy_reference/     # 旧 Python 版の参照用コード
├── build/                # Wails ビルド資材
├── main.go               # Wails デスクトップアプリ起動点
├── server.go             # サーバーモード起動点
├── Dockerfile            # サーバーモード用 Docker 定義
├── wails.json            # Wails 設定
└── VERSION               # リリースバージョン
```

## 必要な環境

- Go 1.23 以上
- Node.js 20 以上
- npm
- Wails CLI
- Docker

Wails CLI が未インストールの場合:

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@latest
```

## セットアップ

```bash
git clone <repository-url>
cd Omni_Money
cd frontend
npm install
cd ..
```

## デスクトップアプリとして起動

開発モード:

```bash
wails dev
```

ビルド:

```bash
wails build
```

デスクトップモードでは、SQLite データベースは OS 標準のアプリケーションデータディレクトリに保存されます。

- macOS: `~/Library/Application Support/OmniMoney/omni_money.db`
- Windows: `%APPDATA%/OmniMoney/omni_money.db`
- Linux: `~/.local/share/OmniMoney/omni_money.db`

## サーバーモードで起動

フロントエンドをビルドしてから、`server` ビルドタグ付きで Go サーバーを起動します。

```bash
cd frontend
npm run build
cd ..
go run -tags server ./server.go
```

標準では `0.0.0.0:4000` で待ち受けます。

主な環境変数:

| 変数 | 既定値 | 説明 |
| --- | --- | --- |
| `DB_PATH` | `omni_money.db` | SQLite データベースの保存先 |
| `HOST_IP` | `0.0.0.0` | 待受アドレス |
| `PORT` | `4000` | 待受ポート |
| `AI_API_TOKEN` | なし | AI API の Bearer トークン |
| `CORS_ALLOWED_ORIGINS` | 同一オリジンのみ | 許可する CORS オリジンのカンマ区切りリスト |

## Docker で起動

```bash
docker build -t omni-money .
docker run --rm -p 4000:4000 -v "$(pwd)/data:/app/data" omni-money
```

起動後、ブラウザで `http://localhost:4000` を開きます。

## AI API

AI API は `AI_API_TOKEN` を設定した場合のみ利用できます。
リクエストには `Authorization: Bearer <AI_API_TOKEN>` を付与してください。

```bash
AI_API_TOKEN=example-token go run -tags server ./server.go
```

利用可能なエンドポイント:

| Method | Path | 説明 |
| --- | --- | --- |
| `POST` | `/api/v1/ai/transactions` | 取引を追加 |
| `POST` | `/api/v1/ai/analysis` | 条件指定で収支を分析 |

AI API では `POST` のみ許可され、`GET`、`PUT`、`DELETE` などは拒否されます。

## 開発時の確認

Go のテスト:

```bash
go test ./...
```

フロントエンドのビルド:

```bash
cd frontend
npm run build
```

## リリース

`VERSION` を更新して `main` に反映すると、GitHub Actions がリリース処理を実行します。

- `validate-version.yml`: PR で `VERSION` の後退を検知
- `release-desktop.yml`: macOS、Windows、Linux 向け Wails アプリをビルド
- `release-docker.yml`: GHCR 向け Docker イメージをビルド

## 機能追加リスト

今後追加・強化したい機能の候補です。

- サーバーモード向けのユーザー認証とセッション管理
- ログイン試行制限、API レート制限、セキュリティヘッダーの追加
- リバースプロキシ配下での `X-Forwarded-*` ヘッダー対応
- `FORCE_HTTPS`、`TLS_CERT_FILE`、`TLS_KEY_FILE` による HTTPS 対応
- 取引画像のプレビュー UI とドラッグアンドドロップ操作の改善
- タグ分析グラフの期間フィルタとドリルダウン操作の拡充
- 取引紐付けの検索・候補表示 UI の改善
- スナップショット作成タイミングの設定化
- CSV インポート時の差分確認、重複検出、プレビュー機能
- AI 分析 API の集計軸追加とレスポンス形式の拡張
- 外部公開時の運用手順、バックアップ手順、復旧手順のドキュメント化

## ライセンス

このプロジェクトは `LICENSE` を参照してください。
