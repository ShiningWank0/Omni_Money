# Omni Money

Omni Money は、Go と Vue.js で構築された家計簿アプリケーションです。
Wails によるデスクトップアプリとして使えるほか、Docker でサーバーモードとして起動し、ブラウザから利用することもできます。

旧 Python 版の `legacy_reference/` を参照しながら、取引管理、複数口座管理、CSV バックアップ、スナップショット復元、タグ分析、AI 向け API などを Go/Vue 構成へ移行しています。

## 使い方

macOS デスクトップアプリ、Mac + Colima、TrueNAS Custom App の詳しい導入・アクセス・バックアップ手順は、[利用ガイド](docs/how-to-use.md)を参照してください。

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
│   ├── middleware/       # ユーザー認証、AI API 認証など
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
├── compose.yaml          # Docker Compose / TrueNAS 用構成
├── .env.example          # サーバー環境変数の雛形
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
AUTH_PASSWORD_HASH='<bcrypt-hash>' go run -tags server ./server.go
```

`<bcrypt-hash>` は実際に作成した bcrypt ハッシュへ置き換えてください。作成方法は[利用ガイド](docs/how-to-use.md#21-bcrypt-ハッシュを作成する)を参照してください。

公開Webは標準で `0.0.0.0:4000` で待ち受けます。AI APIも利用する場合だけ、32文字以上のランダムなトークンを追加します。

```bash
AUTH_PASSWORD_HASH='<bcrypt-hash>' \
AI_API_TOKEN='<32文字以上のランダム値>' \
go run -tags server ./server.go
```

この場合、AI専用APIは標準で `127.0.0.1:4001` で待ち受けます。公開WebとAI APIは同じGoプロセスとSQLiteを使用しますが、HTTPルーターと認証境界は分離されています。

- 公開WebポートにはAI APIルートを登録しません。
- AI専用ポートには通常API、ログインAPI、静的ファイルを登録しません。
- `AI_API_TOKEN` 未設定時はAI専用リスナー自体を起動しません。
- AI専用リスナーは既定でlocalhost以外へバインドできません。

主な環境変数:

| 変数 | 既定値 | 説明 |
| --- | --- | --- |
| `DB_PATH` | `omni_money.db` | SQLite データベースの保存先 |
| `HOST_IP` | `0.0.0.0` | 待受アドレス |
| `PORT` | `4000` | 待受ポート |
| `AUTH_PASSWORD_HASH` | なし（必須） | ログインパスワードの bcrypt ハッシュ |
| `SESSION_MAX_AGE_HOURS` | `24` | セッション有効期間（時間） |
| `AI_API_TOKEN` | なし | 32文字以上のAI API Bearerトークン。未設定ならAI API無効 |
| `AI_HOST_IP` | `127.0.0.1` | AI専用リスナーの待受アドレス |
| `AI_PORT` | `4001` | AI専用リスナーのポート |
| `AI_ALLOW_REMOTE` | `false` | AIを非ループバックで待受する明示許可。Docker内の待受時のみ利用 |
| `TRUSTED_PROXIES` | なし | 信頼するリバースプロキシIP/CIDR |
| `FORCE_HTTPS` | `false` | 公開WebのHTTPSリダイレクト |
| `HTTPS_REDIRECT_HOST` | なし | HTTPSリダイレクト先 |
| `ALLOWED_HOSTS` | なし | HTTPSリダイレクトで許可するホスト |
| `CORS_ALLOWED_ORIGINS` | 同一オリジンのみ | 許可する CORS オリジンのカンマ区切りリスト |

## Docker で起動

```bash
docker build -t omni-money .
mkdir -p data
export AUTH_PASSWORD_HASH='<bcrypt-hash>'
docker run --rm \
  --user "$(id -u):$(id -g)" \
  -e AUTH_PASSWORD_HASH \
  -p 127.0.0.1:4000:4000 \
  -v "$(pwd)/data:/app/data" \
  omni-money
```

起動後、ブラウザで `http://localhost:4000` を開きます。
Colima、LAN 公開、TrueNAS Custom App の手順は[利用ガイド](docs/how-to-use.md)を参照してください。

AI APIも利用する場合は、コンテナ内部では全インターフェースで待ち受けさせつつ、
Dockerホスト側では必ずlocalhostに限定して公開します。

```bash
export AI_API_TOKEN='<32文字以上のランダム値>'
docker run --rm \
  --user "$(id -u):$(id -g)" \
  -p 127.0.0.1:4000:4000 \
  -p 127.0.0.1:4001:4001 \
  -e AUTH_PASSWORD_HASH \
  -e AI_API_TOKEN \
  -e AI_HOST_IP=0.0.0.0 \
  -e AI_ALLOW_REMOTE=true \
  -v "$(pwd)/data:/app/data" \
  omni-money
```

`-p 4001:4001` のようにホストIPを省略してAIポートを公開しないでください。
AIを利用しない場合は `AI_API_TOKEN` と4001番ポートの公開を両方省略します。

### Docker Compose / TrueNAS

同梱の `compose.yaml` は、家計簿Webを4000番、AI APIをDockerホストのlocalhost:4001へ分離して、
1コンテナで起動します。

```bash
cp .env.example .env
# .env の AUTH_PASSWORD_HASH、AI_API_TOKEN、OMNI_DATA_DIR を編集
docker compose up -d --build
```

bcryptハッシュは `$` を含むため、`.env` では例のとおり値全体をシングルクォートで囲んでください。

TrueNAS Custom Appでは `compose.yaml` 相当の設定を使い、次を守ってください。

- `/app/data` を `/mnt/<pool>/apps/omni-money` 等の永続Datasetへ割り当てる
- Webのコンテナポート4000だけをLANまたはリバースプロキシへ公開する
- AIのコンテナポート4001はホストIP `127.0.0.1` に限定する
- TrueNAS UIでホストIPを限定できない場合は4001を公開しない
- 外部公開はCaddy/Nginx等でTLS終端し、`TRUSTED_PROXIES`を限定設定する

設定値は次のように生成できます。bcrypt生成はパスワードを対話入力するため、シェル履歴へ平文を残しません。

```bash
docker run -it --rm httpd:2.4-alpine htpasswd -nBC 12 omni
openssl rand -hex 32
```

1つ目の出力は `omni:` より後ろのbcryptハッシュだけを `AUTH_PASSWORD_HASH` に設定し、
2つ目の出力を `AI_API_TOKEN` に設定します。

## AI API

AI API は `AI_API_TOKEN` を設定した場合のみ、公開Webとは別のAI専用リスナーで利用できます。
リクエストには `Authorization: Bearer <AI_API_TOKEN>` を付与してください。

```bash
curl -X POST http://127.0.0.1:4001/api/v1/ai/analysis \
  -H 'Authorization: Bearer <AI_API_TOKEN>' \
  -H 'Content-Type: application/json' \
  -d '{}'
```

利用可能なエンドポイント:

| Method | Path | 説明 |
| --- | --- | --- |
| `POST` | `/api/v1/ai/transactions` | 取引を追加 |
| `POST` | `/api/v1/ai/analysis` | 条件指定で収支を分析 |

AI API では `POST` のみ許可され、`GET`、`PUT`、`DELETE` などは拒否されます。
公開Webポート `:4000/api/v1/ai/*` ではAIトークンを受け付けません。

サーバーモードのメニューには「クレジットカード設定」の直下に「AI API操作」が表示されます。この画面は通常のセッション認証を通過し、サーバー内部からAI専用リスナーへ固定された分析・取引追加だけを中継します。AI用Bearer tokenはブラウザへ返しません。

クラウドLLMを使う場合も、AIトークンをLLMへ渡したりAIポートをインターネット公開したりせず、
ローカルのエージェントプロセスがLLMのtool callを受けて `127.0.0.1:4001` を呼び出してください。

Discordレシート登録、LLM仲介プロセス、画像受け渡し、項目・口座context API、AI Managerの別プロセス化は[AI連携ロードマップ](docs/ai-integration-roadmap.md)にまとめています。

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

- 取引画像のプレビュー UI とドラッグアンドドロップ操作の改善
- タグ分析グラフの期間フィルタとドリルダウン操作の拡充
- 取引紐付けの検索・候補表示 UI の改善
- スナップショット作成タイミングの設定化
- CSV インポート時の差分確認、重複検出、プレビュー機能
- AI 分析 API の集計軸追加とレスポンス形式の拡張

## ライセンス

このプロジェクトは `LICENSE` を参照してください。
