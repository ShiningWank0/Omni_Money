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

### 画像添付の安全上限

通常Web、デスクトップ、AI APIの画像添付には同じ検証と保存上限が適用されます。

- 対応形式: 静止画のJPEG、PNG、GIF、WebP（MIME、拡張子、実データが一致すること）
- 画像1件: 5 MiB、20メガピクセルまで
- 取引1件: 10画像、合計20 MiBまで
- 同名口座: 合計128 MiBまで
- DB全体: 合計256 MiBまで

サーバーモードではBase64を含むHTTPリクエスト全体が10 MiB上限のため、画面から一度に送る画像原データは合計7 MiBまでに制限されます。また、認証済みの `GET /api/image_storage` で現在の件数、使用量、上限、口座別使用量を確認できます。画像を削除する場合は、一覧取得で取引IDと画像IDを確認してから `DELETE /api/transaction_images/{transactionId}/{imageId}` を使用してください。URLの取引IDと画像の所属が一致しない削除は拒否されます。削除後は `GET /api/image_storage` で使用量が減ったことを確認してください。

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

- Go 1.26.6 以上（CI・リリースは 1.26.7）
- Node.js 24.19 以上
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
npm ci --ignore-scripts
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

DesktopとserverのDB、WAL、snapshotはSQLCipher 4.18.0で暗号化し、所有者だけが読み書きできる権限で作成します。SQLCipherが不足・不正な場合は平文DBへfallbackせず、DBを開く前に起動を拒否します。DesktopのCSV exportは平文のため、暗号化済みvolumeへ保存してください。server modeはさらに外部暗号化volumeの期限付きattestationも検証します。鍵の作成と復旧は[SQLCipher鍵の運用](docs/sqlcipher-key-operations.md)、volumeの設定と復旧試験は[保存時暗号化volumeの運用contract](docs/at-rest-encryption.md)を参照してください。FileVault、BitLocker、LUKSもdefense in depthとして有効にしてください。

- macOS: `~/Library/Application Support/OmniMoney/vaults/<vault-id>/omni_money.db`
- Windows: `%APPDATA%/OmniMoney/vaults/<vault-id>/omni_money.db`
- Linux: `~/.local/share/OmniMoney/vaults/<vault-id>/omni_money.db`

## サーバーモードで起動

フロントエンドをビルドしてから、`server` ビルドタグ付きで Go サーバーを起動します。

```bash
sudo apt-get install -y build-essential curl tcl libssl-dev
SQLCIPHER_PREFIX="$PWD/.build/sqlcipher" sh scripts/build-sqlcipher-linux.sh
cd frontend
npm ci --ignore-scripts
npm run build
cd ..
CONTROL_DB_PATH="$PWD/data/control/omni_control.db" \
CONTROL_DB_ENCRYPTION_KEY_FILE='/secure-config/control-database.key' \
VAULT_ROOT="$PWD/data/vaults" \
INITIAL_ADMIN_SETUP_TOKEN_FILE='/secure-config/initial-admin-setup.token' \
ALLOWED_HOSTS='localhost:4000,127.0.0.1:4000' \
DATA_AT_REST_MODE='external-encrypted-volume' \
DATA_AT_REST_ATTESTATION_FILE='/secure-config/omni-data-at-rest.json' \
CGO_CFLAGS="-I$PWD/.build/sqlcipher/include" \
CGO_LDFLAGS="-L$PWD/.build/sqlcipher/lib" \
LD_LIBRARY_PATH="$PWD/.build/sqlcipher/lib" \
go run -tags 'server libsqlite3 sqlite_omit_load_extension' ./server.go
```

control鍵とinitial-admin setup tokenは別々のowner-only secretとして生成します。setup tokenは32 byte以上のbase64url文字列にしてください。初回アクセスではブラウザがユーザーvault用の回復コードを生成して、保存確認後に最初のAdminを作成します。ログイン後は「サーバーユーザー管理」から招待、password reset token発行、user無効化を行えます。各ユーザーは「パスキー設定」からPRF対応パスキーを登録でき、以後はパスワードまたはパスキーのどちらでも自分のVaultへログインし、重要操作を再認証できます。invite/reset tokenはURLに含めず本人へ安全に渡してください。Adminのパスワードやcontrol鍵だけでは他ユーザーのvaultを開けません。

直接起動した公開Webは標準で `127.0.0.1:4000` で待ち受けます。`ALLOWED_HOSTS` は直接起動でも必須です。非loopback平文HTTPは許可されず、TLSまたは固定したtrusted proxy経由のHTTPSが必要です。同梱のComposeはPangolin/Newt専用構成で、ホストへポートを公開しません。ローカル利用時だけ `compose.local.yaml` を重ねます。

現在のmulti-user serverでは、ユーザーvaultに安全に紐付かないAI APIと、vault managerを迂回するsnapshot restoreを無効化しています。旧AI環境変数を指定すると起動を拒否します。詳細は[server multi-vault security model](docs/server-multi-vault.md)を参照してください。

主な環境変数:

| 変数 | 既定値 | 説明 |
| --- | --- | --- |
| `CONTROL_DB_PATH` | なし（serverでは必須） | attested data root配下のSQLCipher control DB |
| `CONTROL_DB_ENCRYPTION_KEY_FILE` | なし（serverでは必須） | control DB専用32 byte鍵の秘密ファイル（data root外） |
| `VAULT_ROOT` | なし（serverでは必須） | 同じattested data root配下のユーザーvault専用directory |
| `INITIAL_ADMIN_SETUP_TOKEN_FILE` | 初回のみ必須 | 最初のAdmin作成を認可するbase64url token file |
| `AUTH_KDF_CONCURRENCY` | `2` | Argon2id認証処理の同時実行上限（1〜16） |
| `DATA_AT_REST_MODE` | なし（serverでは必須） | `external-encrypted-volume`だけを許可 |
| `DATA_AT_REST_ATTESTATION_FILE` | なし（serverでは必須） | data root、非秘密key ID、検証・復旧・rotation時刻を記録したfile |
| `HOST_IP` | `127.0.0.1` | 直接起動時の待受アドレス（Docker内部は `0.0.0.0`） |
| `WEB_EXTERNAL_HOST` | なし | Docker等で内部待受と実際のpublish先が異なる場合の公開先host |
| `PORT` | `4000` | 待受ポート |
| `SESSION_MAX_AGE_HOURS` | `8` | セッションの絶対有効期間（時間） |
| `SESSION_IDLE_TIMEOUT_MINUTES` | `15` | 無操作セッションのタイムアウト（分）。画面も自動ロック |
| `SESSION_REAUTH_MAX_AGE_MINUTES` | `5` | CSV入出力・復元等の高影響操作で要求するパスワード再確認の有効期間（分） |
| `SESSION_MAX_CONCURRENT` | `3` | 1ユーザーあたりの同時セッション上限 |
| `TRUSTED_PROXIES` | なし | 信頼する最後段プロキシの固定IPまたは狭いCIDR（IPv4は`/24`以上、IPv6は`/120`以上） |
| `FORCE_HTTPS` | `false` | 公開WebのHTTPSリダイレクト |
| `HTTPS_REDIRECT_HOST` | なし | HTTPSリダイレクト先 |
| `ALLOWED_HOSTS` | なし（必須） | すべてのリクエストで許可する正確な公開Host |
| `PASSKEY_RP_ID` | 公開Hostから導出 | パスキーのRelying Party ID（scheme/portなし） |
| `PASSKEY_ORIGINS` | 公開Hostから導出 | WebAuthnを許可する正確なorigin（カンマ区切り） |
| `ALLOW_INSECURE_HTTP` | `false` | loopbackだけへpublishするローカル構成用。非loopback平文HTTPは許可されない |
| `CORS_ALLOWED_ORIGINS` | 同一オリジンのみ | 許可する CORS オリジンのカンマ区切りリスト |

## Docker で起動

```bash
docker build -t omni-money .
# 先に docs/at-rest-encryption.md に従って暗号化volumeと
# secrets/omni_data_at_rest.json、control鍵、initial setup tokenを準備する。
umask 077
mkdir -p data secrets
openssl rand -hex 32 > secrets/control-database.key
openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n' > secrets/initial-admin-setup.token
docker run --rm \
  --user "$(id -u):$(id -g)" \
  -e ALLOWED_HOSTS='localhost:4000,127.0.0.1:4000' \
  -e DATA_AT_REST_MODE=external-encrypted-volume \
  -e DATA_AT_REST_ATTESTATION_FILE=/run/secrets/omni_data_at_rest.json \
  -e CONTROL_DB_PATH=/app/data/control/omni_control.db \
  -e CONTROL_DB_ENCRYPTION_KEY_FILE=/run/secrets/omni_control_database_key \
  -e VAULT_ROOT=/app/data/vaults \
  -e INITIAL_ADMIN_SETUP_TOKEN_FILE=/run/secrets/omni_initial_admin_setup_token \
  --mount "type=bind,src=$(pwd)/secrets/omni_data_at_rest.json,dst=/run/secrets/omni_data_at_rest.json,readonly" \
  --mount "type=bind,src=$(pwd)/secrets/control-database.key,dst=/run/secrets/omni_control_database_key,readonly" \
  --mount "type=bind,src=$(pwd)/secrets/initial-admin-setup.token,dst=/run/secrets/omni_initial_admin_setup_token,readonly" \
  -e WEB_EXTERNAL_HOST=127.0.0.1 \
  -e ALLOW_INSECURE_HTTP=true \
  -p 127.0.0.1:4000:4000 \
  -v "$(pwd)/data:/app/data" \
  omni-money
```

起動後、ブラウザで `http://localhost:4000` を開きます。初回Admin作成後の再起動では、setup tokenの環境変数とmountを外します。
Colima、LAN 公開、TrueNAS Custom App の手順は[利用ガイド](docs/how-to-use.md)を参照してください。

`compose.ai.yaml` は旧単一DB用で、現在のmulti-user serverでは意図的に起動拒否されます。

### Docker Compose / Pangolin / TrueNAS

同梱の `compose.yaml` はPangolin/Newt向けの閉じた構成です。Omni Moneyは`internal`な専用networkにだけ接続し、Web/AIともホストへポートを公開しません。Pangolinのtargetは`http://omni-money:4000`にします。Newt側も同じ`omni-money-pangolin` networkへ参加させ、`DOCKER_ENFORCE_NETWORK_VALIDATION=true`、公開FQDNに一致する`ALLOWED_HOSTS`、Newtだけを指す`TRUSTED_PROXIES`を設定してください。

```bash
cp .env.example .env
# .env の ALLOWED_HOSTS、TRUSTED_PROXIES、OMNI_DATA_DIR、attestation、
# control key、initial-admin setup tokenのpathを編集
umask 077
mkdir -p ./data ./secrets
test -s ./secrets/control-database.key || openssl rand -hex 32 > ./secrets/control-database.key
test -s ./secrets/initial-admin-setup.token || \
  openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n' > ./secrets/initial-admin-setup.token
sudo chown 10001:10001 ./data ./secrets/control-database.key ./secrets/initial-admin-setup.token
sudo chown root:root ./secrets/omni_data_at_rest.json
sudo chmod 700 ./data
sudo chmod 400 ./secrets/control-database.key ./secrets/initial-admin-setup.token
sudo chmod 444 ./secrets/omni_data_at_rest.json
docker compose -f compose.yaml -f compose.bootstrap.yaml up -d --build
```

ネイティブLinuxでbind mountを使う場合、初回起動前に`./data`と秘密ファイルをコンテナの固定UID/GID `10001:10001`へ設定してください。dataは`0700`、control keyとsetup tokenは`0400`にします。非秘密のattestationはroot所有`0444`にします。TrueNASでは同等のACLを設定します。`chmod 777`やPrivileged modeは使いません。

ローカル端末だけから試す場合は、閉じたbase構成にloopback公開を重ねます。

```bash
docker compose -f compose.yaml -f compose.bootstrap.yaml -f compose.local.yaml up -d --build
```

最初のAdminを作成したらbootstrap overlayを外してcontainerを再作成し、起動確認後にsetup tokenを安全にretireします。

```bash
docker compose -f compose.yaml -f compose.local.yaml up -d --force-recreate
```

Composeはコンテナのroot filesystemをread-onlyにし、Linux capabilityをすべて削除して、権限昇格を禁止します。永続的に書き込めるアプリ領域は `/app/data` だけです。SQLiteの一時ファイルには再起動で消える `/tmp` tmpfsを使い、CPU・memory・PIDにも上限を設定します。既定値は `.env.example` の `OMNI_CPU_LIMIT`、`OMNI_MEMORY_LIMIT`、`OMNI_PIDS_LIMIT`、`OMNI_TMPFS_SIZE` で調整できます。上限を下げる場合は、最大サイズの画像処理やCSV取込を実データ量で検証してください。

本番更新では固定version imageと[safe update手順](docs/safe-update.md)を使用してください。更新前のoffline checkpoint、ingressから隔離したhealthcheck、更新失敗時だけの旧data/image復元を自動化しています。`latest`を直接deployしたり、`down -v`を使ったりしないでください。

TrueNAS Custom Appでは `compose.yaml` 相当の設定を使い、次を守ってください。

- `/app/data` を `/mnt/<pool>/apps/omni-money` 等の永続Datasetへ割り当てる
- Pangolin運用ではWebの`ports:`を削除し、Newtとだけ共有する専用networkへ接続する
- LAN限定の移行構成でもWeb publish先はTrueNASの正確な固定IPへ限定し、`ALLOWED_HOSTS`を一致させる
- AI用の追加ポートやlegacy AI overlayを公開しない
- 外部公開はPangolin等でTLS終端し、`TRUSTED_PROXIES`をNewtの固定IPだけに設定する

## AI API（multi-user serverでは一時無効）

AI資格情報はまだcontrol userとvault DEKに結び付いていません。このためproduction serverはAI関連環境変数が1つでも設定されていると起動を拒否し、AI listenerとWeb consoleを登録しません。旧AI実装とCLIは将来のuser-bound capability移行の参考としてsourceに残していますが、現在のserver運用では使用しないでください。進捗は[AI連携ロードマップ](docs/ai-integration-roadmap.md)と[server multi-vault security model](docs/server-multi-vault.md)を参照してください。

<!-- Legacy AI protocol documentation is intentionally omitted from the active server instructions. -->

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
