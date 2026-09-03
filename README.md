# Omni Money

Omni Money は、Go と Vue.js で構築された家計簿アプリケーションです。
Wails によるデスクトップアプリとして使えるほか、Docker でサーバーモードとして起動し、ブラウザから利用することもできます。

| Capability | Desktop | multi-user server |
| --- | --- | --- |
| Ledger / credential lifecycle | Wails、roleなしの単一 local vault、password/recovery、idle lock | Docker/headless、control DBとuser vault分離、Argon2id envelope、invite/reset、role、passkey、session/vault lease |
| CSV v3 | transactions/images/tags/links/ledger settingsを含む平文full ledger | 同左。auth/control/key、snapshot、volume recoveryは含まない |
| Snapshot | 手動のみ。自動snapshotは未接続 | 本人に束縛された手動APIのみ。自動snapshotは未接続 |
| Automatic snapshot | 未接続 | 未接続 |
| AI | production 非提供 | production 非提供。旧AI設定は列挙分を起動拒否 |
| Schema / legacy migration | schema migrationと明示的な旧root DB移行 | schema migrationのみ。旧single-DB serverからmulti-userへの自動移行は非提供、CSV v3による手動移行が必要 |
| safe-update | server用safe-update対象外。固定artifactをrelease workflowで検証 | project `omni-money` のcompose/env/digestを固定検証し、固定imageをatomic更新 |

旧 Python 版の `legacy_reference/` は参照専用です。現行の取引管理、複数口座、CSV ledger、snapshot復元、タグ分析を Go/Vue 構成で提供します。

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
- AI は両モードの production で提供していません（旧実装は dormant legacy）
- GitHub Actions による VERSION 起点のデスクトップ版リリースと Docker イメージリリース

### CSV 完全バックアップ（v3）

CSV出力は常にv3の完全ledger形式です。transactions、images、tags、links、ledger settingsを含み、画像はファイル名・検証済みMIMEタイプ・Base64バイナリとして別レコードに格納します。タグ・リンクは元IDを参照して復元時に安全に再採番します。CSVと画像のBase64は常に平文であり、auth/control DB、credential、DEK/key、snapshot、volume recovery materialは含みません。旧クライアント互換のtransactions-only v2が必要な場合だけ、明示的な `BackupToCSVV2` 互換APIを使用してください。v2は完全バックアップではなくappend用途に限られます。

v1/v2（`id,account,date,item,type,amount,balance[,memo]`）は引き続きappendインポートできます。旧形式のreplaceは、画像・タグ・リンクなどを表現できず安全な完全置換にならないため拒否されます。完全置換にはCSV v3を使用してください。v3ではバージョン、レコード種別、Base64画像を厳格に検証し、サイズ・MIME・重複ID・CSV式注入を拒否します。v3 export末尾には全record typeの件数とcanonical digestを含むmanifestを必ず付け、replaceでは公式完全ヘッダーとmanifestが一致しない入力をDB変更前に拒否します。CSV v3のreplaceインポートは全レコードと設定を1つのSQLite transactionで処理し、画像や関連付けの途中失敗を含め完全にrollbackします。appendは既存の取引関連データとledger設定を保持し、CSVのallowlist設定が既存値と異なる場合は競合としてatomicに中止します。既存の取引リンクを自動削除することはありません。ストリーミングのraw CSVは512 MiB、解析済みテキストは64 MiB、行数は100万行までです。後方互換のWails/JSON文字列経路は64 MiBに制限されるため、完全バックアップにはDesktopのファイルダイアログまたはserverのraw CSV uploadを使用してください。

CSVは画像を含め常に暗号化されない平文です。DesktopではダイアログでFileVault・BitLocker・LUKS等に保護された保存先を選び、serverではブラウザのダウンロード先が暗号化volume上であることを確認してください。保存先や共有先の安全性はアプリから検証できません。

### 画像添付の安全上限

通常Webとデスクトップの画像添付には同じ検証と保存上限が適用されます。AI APIはproductionでは提供しません。

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
│   ├── database/         # ledger、CSV v3、手動スナップショット
│   ├── control/          # server identity、role、session、envelope metadata
│   ├── desktopaccount/   # Desktop local password/recovery/lock lifecycle
│   ├── keyenvelope/      # Argon2id、AES-GCM、password/recovery/passkey envelope
│   ├── serverauth/       # multi-user password/passkey/invite/reset
│   ├── securedb/         # SQLCipher open/validation
│   ├── vault/            # per-user vault manager、lease、drain、zeroize
│   ├── middleware/       # session、CSRF、proxy、security boundary
│   └── models/           # データモデル
├── frontend/             # Vue フロントエンド（`src/`、`wailsjs/`、`index.html`）
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
- 固定版 Wails v2.11.0（配布workflowが導入）
- Docker

Desktopの開発・配布は、[利用ガイド](docs/how-to-use.md)と[SQLCipher鍵の運用](docs/sqlcipher-key-operations.md)に記載した固定SQLCipher手順、および固定版Wailsを使ってください。未固定版やタグなしのWails CLIを実行しないでください。

## セットアップ

```bash
git clone <repository-url>
cd Omni_Money
cd frontend
npm ci --ignore-scripts
cd ..
```

## デスクトップアプリとして起動

開発・配布時も通常SQLiteや未固定CLIへ切り替えず、各OS向け `scripts/build-sqlcipher-*.sh` と release workflow の固定 `libsqlite3 sqlite_omit_load_extension` build tag/CGO設定を使用します。具体的な検証方法は[利用ガイド](docs/how-to-use.md)を参照してください。

デスクトップモードでは、SQLite データベースは OS 標準のアプリケーションデータディレクトリに保存されます。

DesktopとserverのDB、WAL、snapshotはSQLCipher 4.18.0で暗号化し、所有者だけが読み書きできる権限で作成します。SQLCipherが不足・不正な場合は平文DBへfallbackせず、DBを開く前に起動を拒否します。DesktopのCSV exportは平文のため、暗号化済みvolumeへ保存してください。server modeはさらに外部暗号化volumeの期限付きattestationも検証します。鍵の作成と復旧は[SQLCipher鍵の運用](docs/sqlcipher-key-operations.md)、volumeの設定と復旧試験は[保存時暗号化volumeの運用contract](docs/at-rest-encryption.md)を参照してください。FileVault、BitLocker、LUKSもdefense in depthとして有効にしてください。

- macOS: `~/Library/Application Support/OmniMoney/vaults/<vault-id>/omni_money.db`
- Windows: `%APPDATA%/OmniMoney/vaults/<vault-id>/omni_money.db`
- Linux: `$XDG_DATA_HOME/OmniMoney/vaults/<vault-id>/omni_money.db`（未設定時は `~/.local/share/OmniMoney`）

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

control鍵とinitial-admin setup tokenは別々のowner-only secretとして生成します。setup tokenは32 byte以上のbase64url文字列にしてください。初回アクセスではブラウザがユーザーvault用の回復コードを生成して、保存確認後に最初のAdminを作成します。ログイン後は「サーバーユーザー管理」から招待、password reset tokenの発行・取消、userの無効化・再有効化・role変更を行えます。各ユーザーは「認証情報の管理」でpassword/recovery code、全sessionを管理し、「パスキー設定」でPRF対応パスキーを登録・個別/一括失効できます。credential変更や失効後は全sessionと開いているVaultを閉じます。invite/reset tokenはURLに含めず本人へ安全に渡してください。Adminのパスワードやcontrol鍵だけでは他ユーザーのvaultを開けません。

直接起動した公開Webは標準で `127.0.0.1:4000` で待ち受けます。`ALLOWED_HOSTS` は直接起動でも必須です。非loopback平文HTTPは許可されず、TLSまたは固定したtrusted proxy経由のHTTPSが必要です。同梱のComposeはPangolin/Newt専用構成で、ホストへポートを公開しません。ローカル利用時だけ `compose.local.yaml` を重ねます。

multi-user serverのsnapshot APIは認証済み本人のvaultだけに束縛され、snapshotは既存per-vault SQLCipher DEKで暗号化されます。application Admin/APIでも他user vaultの平文を列挙・復号・復元できませんが、同じservice UID、host root/operator、差し替え可能なbinary、process memoryは同じtrust boundaryです。自動snapshotはproduction serverに未接続で、現行は明示的な手動APIだけです。旧AI環境変数を指定すると起動を拒否します。詳細は[server multi-vault security model](docs/server-multi-vault.md)を参照してください。

### Disaster Recovery (DR)

snapshot単体はDR setではありません。control DBとcontrol key、各user vaultと暗号化snapshot、volumeのkey/recovery material・attestation/復旧手順、各userのrecovery codeを別々に保管し、隔離環境でrestore drillを行います。

serverの環境変数は [.env.example](.env.example) を唯一の雛形とし、詳細な必須条件とsecret/owner/modeは[利用ガイド](docs/how-to-use.md)を参照してください。旧 single-DB、bcrypt/TOTP、旧AI envは設定時に起動を拒否します。

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

`compose.ai.yaml` は旧単一DB用で、現在のmulti-user serverでは意図的に起動拒否されます。旧AI packageは将来のuser-vault-bound設計の参考資料であり、production機能ではありません。

### Docker Compose / Pangolin / TrueNAS

同梱の `compose.yaml` はPangolin/Newt向けの閉じた構成です。Omni Moneyは`internal`な専用networkにだけ接続し、Web/AIともホストへポートを公開しません。Pangolinのtargetは`http://omni-money:4000`にします。Newt側も同じ`omni-money-pangolin` networkへ参加させ、`DOCKER_ENFORCE_NETWORK_VALIDATION=true`、公開FQDNに一致する`ALLOWED_HOSTS`、Newtだけを指す`TRUSTED_PROXIES`を設定してください。

```bash
cp .env.example .env
# .env の ALLOWED_HOSTS、TRUSTED_PROXIES、OMNI_DATA_DIR、at-rest/update attestation、
# control key、initial-admin setup tokenのpathを編集
umask 077
mkdir -p ./data ./secrets
test -s ./secrets/control-database.key || openssl rand -hex 32 > ./secrets/control-database.key
test -s ./secrets/initial-admin-setup.token || \
  openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n' > ./secrets/initial-admin-setup.token
sudo chown 10001:10001 ./data ./secrets/initial-admin-setup.token
sudo chown root:10001 ./secrets/control-database.key
sudo chown root:root ./secrets/omni_data_at_rest.json
sudo chmod 700 ./data
sudo chmod 440 ./secrets/control-database.key
sudo chmod 400 ./secrets/initial-admin-setup.token
sudo chmod 444 ./secrets/omni_data_at_rest.json
docker compose -f compose.yaml -f compose.bootstrap.yaml up -d --build
```

ネイティブLinuxでbind mountを使う場合、初回起動前に`./data`を固定UID/GID `10001:10001`へ設定し、control keyは`root:10001`・`0440`、setup tokenは`10001:10001`・`0400`、非秘密のattestationは`root:root`・`0444`にします。safe-updateのhost secret contractもこのowner/modeとdevice/inode/hashを固定し、差替えを拒否します。TrueNASでは同等のACLを設定します。`chmod 777`やPrivileged modeは使いません。

ローカル端末だけから試す場合は、閉じたbase構成にloopback公開を重ねます。

```bash
docker compose -f compose.yaml -f compose.bootstrap.yaml -f compose.local.yaml up -d --build
```

最初のAdminを作成したらbootstrap overlayを外してcontainerを再作成し、起動確認後にsetup tokenを安全にretireします。

```bash
docker compose -f compose.yaml -f compose.local.yaml up -d --force-recreate
```

Composeはコンテナのroot filesystemをread-onlyにし、Linux capabilityをすべて削除して、権限昇格を禁止します。永続的に書き込めるアプリ領域は `/app/data` だけです。SQLiteの一時ファイルには再起動で消える `/tmp` tmpfsを使い、CPU・memory・PIDにも上限を設定します。既定値は `.env.example` の `OMNI_CPU_LIMIT`、`OMNI_MEMORY_LIMIT`、`OMNI_PIDS_LIMIT`、`OMNI_TMPFS_SIZE` で調整できます。上限を下げる場合は、最大サイズの画像処理やCSV取込を実データ量で検証してください。

本番更新では固定version imageと[safe update手順](docs/safe-update.md)を使用してください。更新前のoffline checkpoint、ingressから隔離したhealthcheck、更新失敗時だけの旧data/image復元を自動化しています。checkpoint rootはattested data mountから導出した固定pathへ限定し、Compose env fileも更新処理と完全に一致させます。`latest`を直接deployしたり、`down -v`を使ったりしないでください。

TrueNAS Custom Appでは `compose.yaml` 相当の設定を使い、次を守ってください。

- `/app/data` を `/mnt/<pool>/apps/omni-money` 等の永続Datasetへ割り当てる
- Pangolin運用ではWebの`ports:`を削除し、Newtとだけ共有する専用networkへ接続する
- LAN限定の移行構成でもWeb publish先はTrueNASの正確な固定IPへ限定し、`ALLOWED_HOSTS`を一致させる
- AI用の追加ポートやlegacy AI overlayを公開しない
- 外部公開はPangolin等でTLS終端し、`TRUSTED_PROXIES`をNewtの固定IPだけに設定する

## AI（両 production mode で非提供）

AI資格情報はまだcontrol userとvault DEKに結び付いていません。このためproduction serverは `backend/config/server.go` が列挙する旧設定（`AI_API_TOKEN`、`AI_CREDENTIALS_FILE`、`AI_CONSOLE_TOKEN_FILE`、`AI_AUDIT_HMAC_KEYRING_FILE`、`AI_HOST_IP`、`AI_PORT`、`AI_ALLOW_REMOTE`、AI TLS関連file）が設定されている場合に起動を拒否し、AI listenerとWeb consoleを登録しません。列挙外の環境変数まで一括拒否する契約ではありません。旧AI実装とCLIは将来のuser-bound capability移行の参考としてsourceに残していますが、現在のserver運用では使用しないでください。進捗は[AI連携ロードマップ](docs/ai-integration-roadmap.md)と[server multi-vault security model](docs/server-multi-vault.md)を参照してください。

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
- `release-desktop.yml`: macOS Intel、macOS Apple Silicon、Windows、Linux の4 Desktop artifactを固定Wails/SQLCipherでビルド
- `release-docker.yml`: GHCR 向け Docker イメージをビルド

## 機能追加リスト

今後追加・強化したい機能の候補です。

- 取引画像のプレビュー UI とドラッグアンドドロップ操作の改善
- タグ分析グラフの期間フィルタとドリルダウン操作の拡充
- 取引紐付けの検索・候補表示 UI の改善
- スナップショット作成タイミングの設定化
- CSV インポート時の差分確認、重複検出、プレビュー機能
- 将来の user-vault-bound AI 設計（Stage 4、未出荷）の検討

## ライセンス

このプロジェクトは `LICENSE` を参照してください。
