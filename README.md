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

DB、SQLiteの一時ファイル、スナップショット、デスクトップ版が書き出すCSVは、所有者だけが読み書きできる権限で作成します。ただし現時点のSQLiteとCSVは暗号化形式ではありません。端末・ディスク・バックアップ媒体にはOSのフルディスク暗号化を有効にし、CSVを共有ストレージへ放置しないでください。全面暗号化の導入では、鍵をDBと同じ場所へ保存せず、公式に保守される暗号化SQLiteとOSキーストアを組み合わせる必要があります。

- macOS: `~/Library/Application Support/OmniMoney/omni_money.db`
- Windows: `%APPDATA%/OmniMoney/omni_money.db`
- Linux: `~/.local/share/OmniMoney/omni_money.db`

## サーバーモードで起動

フロントエンドをビルドしてから、`server` ビルドタグ付きで Go サーバーを起動します。

```bash
cd frontend
npm run build
cd ..
AUTH_PASSWORD_HASH='<bcrypt-hash>' \
ALLOWED_HOSTS='localhost:4000,127.0.0.1:4000' \
go run -tags server ./server.go
```

`<bcrypt-hash>` は実際に作成した bcrypt ハッシュへ置き換えてください。作成方法は[利用ガイド](docs/how-to-use.md#21-bcrypt-ハッシュを作成する)を参照してください。

直接起動した公開Webは標準で `127.0.0.1:4000` で待ち受けます。`ALLOWED_HOSTS` は直接起動でも必須です。外部公開はPangolin等のゲートウェイまたはVPNを経由し、AI APIは期限・権限・口座・タグ制約を持つ資格情報ファイルを指定した場合だけ有効になります。同梱のComposeはPangolin/Newt専用構成で、ホストへポートを公開しません。ローカル利用時だけ `compose.local.yaml` を重ねます。

```bash
umask 077
mkdir -p secrets
go run ./cmd/ai-audit-key init \
  --file secrets/ai_audit_hmac.json
go run ./cmd/ai-credential issue \
  --file secrets/ai_credentials.json \
  --id local-console \
  --expires-at '<RFC3339-within-90-days>' \
  --scope transactions:create \
  --scope analysis:summary \
  --scope analysis:transactions \
  --scope console:relay \
  --account '現金' \
  --tag-id 1 \
  --analysis-start-date '<YYYY-MM-DD>' \
  --analysis-end-date '<YYYY-MM-DD>' \
  --max-analysis-days 30 \
  --max-results 100 \
  --max-transactions-per-day 100 > secrets/ai_console_token

AUTH_PASSWORD_HASH='<bcrypt-hash>' \
AI_CREDENTIALS_FILE="$PWD/secrets/ai_credentials.json" \
AI_CONSOLE_TOKEN_FILE="$PWD/secrets/ai_console_token" \
AI_AUDIT_HMAC_KEYRING_FILE="$PWD/secrets/ai_audit_hmac.json" \
ALLOWED_HOSTS='localhost:4000,127.0.0.1:4000' \
go run -tags server ./server.go
```

Omni Money側にも認証アプリのTOTPを追加する場合は、Pangolinとは必ず別のseedを生成します。秘密ファイルは環境変数へ直接書かず、ファイルパスだけを設定してください。

```bash
go run ./cmd/omni-totp --out ./data/omni-money-totp.secret
export AUTH_TOTP_SECRET_FILE="$PWD/data/omni-money-totp.secret"
export AUTH_REQUIRE_TOTP=true
AUTH_PASSWORD_HASH='<bcrypt-cost-12-to-16-hash>' \
ALLOWED_HOSTS='localhost:4000,127.0.0.1:4000' \
go run -tags server ./server.go
```

コマンドはセットアップキーと `otpauth://` URI を表示し、認証アプリの現在コードを確認できた後にだけmode `0600`の秘密ファイルを作成します。その出力を端末ログやチャットへ残さず、秘密ファイルもバックアップ時に暗号化して保管してください。TOTPを使わない場合は両方のTOTP環境変数を空のままにします。

TOTPを有効化した場合でも、通常操作中に定期的なコード入力は要求しません。TOTPは新しいセッションへのログインと、無操作・絶対期限・サーバー再起動後の再ログインで使用します。有効なセッション内では通常の閲覧・編集をそのまま継続でき、CSV入出力やスナップショット復元等の高影響操作だけをOmni Moneyのパスワードで再確認します。

可視タブで実際に操作している間は、ブラウザが低頻度のheartbeat（最大4分間隔、操作停止後の末尾送信あり）を送ってサーバー側の無操作期限も延長します。非表示タブや操作のない状態からheartbeatは送信しません。

この場合、AI専用APIは標準で `127.0.0.1:4001` で待ち受けます。公開WebとAI APIは同じGoプロセスとSQLiteを使用しますが、HTTPルーターと認証境界は分離されています。

- 公開WebポートにはAI APIルートを登録しません。
- AI専用ポートには通常API、ログインAPI、静的ファイルを登録しません。
- `AI_CREDENTIALS_FILE` 未設定時はAI専用リスナー自体を起動しません。
- AI専用リスナーは既定でlocalhost以外へバインドできません。
- 非ループバック待受はTLS 1.3とクライアント証明書認証（mTLS）が必須です。
- 資格情報は最大90日で失効し、scope、口座、許可タグID、分析可能な固定日付範囲、1リクエストの期間、明細件数を個別に制限します。`--tag-id` を省略した資格情報は、タグの付与やタグ指定分析を行えません。

主な環境変数:

| 変数 | 既定値 | 説明 |
| --- | --- | --- |
| `DB_PATH` | `omni_money.db` | SQLite データベースの保存先 |
| `HOST_IP` | `127.0.0.1` | 直接起動時の待受アドレス（Docker内部は `0.0.0.0`） |
| `PORT` | `4000` | 待受ポート |
| `AUTH_PASSWORD_HASH` | なし（必須） | ログインパスワードの bcrypt ハッシュ（cost 12〜16） |
| `SESSION_MAX_AGE_HOURS` | `8` | セッションの絶対有効期間（時間） |
| `SESSION_IDLE_TIMEOUT_MINUTES` | `15` | 無操作セッションのタイムアウト（分）。画面も自動ロック |
| `SESSION_REAUTH_MAX_AGE_MINUTES` | `5` | CSV入出力・復元等の高影響操作で要求するパスワード再確認の有効期間（分） |
| `SESSION_MAX_CONCURRENT` | `3` | 1ユーザーあたりの同時セッション上限 |
| `AI_CREDENTIALS_FILE` | なし | ハッシュ化済みAI資格情報JSON。未設定ならAI API無効 |
| `AI_CONSOLE_TOKEN_FILE` | なし | 管理画面中継用の生トークンを格納したsecretファイル |
| `AI_AUDIT_HMAC_KEYRING_FILE` | なし（AI有効時は必須） | 監査口座参照用の専用current/previous HMAC keyring secret |
| `AUTH_TOTP_SECRET_FILE` | なし | Omni Money専用TOTPのBase32秘密ファイル。設定するとTOTPを有効化 |
| `AUTH_REQUIRE_TOTP` | `false` | `true` の場合、秘密ファイルがないと起動失敗。TOTPを採用した本番環境では `true` を推奨 |
| `AI_HOST_IP` | `127.0.0.1` | AI専用リスナーの待受アドレス |
| `AI_PORT` | `4001` | AI専用リスナーのポート |
| `AI_ALLOW_REMOTE` | `false` | AIを非ループバックで待受する明示許可。mTLS設定も必須 |
| `AI_TLS_CERT_FILE` / `AI_TLS_KEY_FILE` | なし | AIリスナーのサーバー証明書と秘密鍵 |
| `AI_TLS_CLIENT_CA_FILE` | なし | mTLSクライアント証明書を検証するCA |
| `AI_TLS_CA_FILE` | なし | 管理画面中継がAIサーバー証明書を検証するCA |
| `AI_TLS_CLIENT_CERT_FILE` / `AI_TLS_CLIENT_KEY_FILE` | なし | 管理画面中継用mTLSクライアント証明書と鍵 |
| `AI_TLS_SERVER_NAME` | なし | AIサーバー証明書の検証名 |
| `TRUSTED_PROXIES` | なし | 信頼する最後段プロキシの固定IPまたは狭いCIDR（IPv4は`/24`以上、IPv6は`/120`以上） |
| `FORCE_HTTPS` | `false` | 公開WebのHTTPSリダイレクト |
| `HTTPS_REDIRECT_HOST` | なし | HTTPSリダイレクト先 |
| `ALLOWED_HOSTS` | なし（必須） | すべてのリクエストで許可する正確な公開Host |
| `ALLOW_INSECURE_HTTP` | `false` | 非loopback平文HTTPを明示許可。loopbackだけへpublishするローカル構成以外では使わない |
| `CORS_ALLOWED_ORIGINS` | 同一オリジンのみ | 許可する CORS オリジンのカンマ区切りリスト |

## Docker で起動

```bash
docker build -t omni-money .
mkdir -p data
export AUTH_PASSWORD_HASH='<bcrypt-hash>'
docker run --rm \
  --user "$(id -u):$(id -g)" \
  -e AUTH_PASSWORD_HASH \
  -e ALLOWED_HOSTS='localhost:4000,127.0.0.1:4000' \
  -e ALLOW_INSECURE_HTTP=true \
  -p 127.0.0.1:4000:4000 \
  -v "$(pwd)/data:/app/data" \
  omni-money
```

起動後、ブラウザで `http://localhost:4000` を開きます。
Colima、LAN 公開、TrueNAS Custom App の手順は[利用ガイド](docs/how-to-use.md)を参照してください。

AI APIも利用する場合は、資格情報・監査HMAC keyring・CA・サーバー証明書・クライアント証明書をリポジトリ外の `secrets/` に準備し、`compose.ai.yaml` を重ねてください。非ループバックのコンテナ内待受ではmTLSなしの起動を拒否します。AIを利用しない場合はoverlayとsecretを省略します。

### Docker Compose / Pangolin / TrueNAS

同梱の `compose.yaml` はPangolin/Newt向けの閉じた構成です。Omni Moneyは`internal`な専用networkにだけ接続し、Web/AIともホストへポートを公開しません。Pangolinのtargetは`http://omni-money:4000`にします。Newt側も同じ`omni-money-pangolin` networkへ参加させ、`DOCKER_ENFORCE_NETWORK_VALIDATION=true`、公開FQDNに一致する`ALLOWED_HOSTS`、Newtだけを指す`TRUSTED_PROXIES`を設定してください。

```bash
cp .env.example .env
# .env の AUTH_PASSWORD_HASH、ALLOWED_HOSTS、TRUSTED_PROXIES、OMNI_DATA_DIR を編集
mkdir -p ./data
docker compose up -d --build
```

ネイティブLinuxでbind mountを使う場合、初回起動前に`./data`をコンテナの固定UID/GID `10001:10001`だけが書ける所有者・modeへ設定してください。TrueNASでは同等のACLを設定します。`chmod 777`やPrivileged modeは使いません。

```bash
sudo chown 10001:10001 ./data
chmod 700 ./data
```

ローカル端末だけから試す場合は、閉じたbase構成にloopback公開を重ねます。

```bash
docker compose -f compose.yaml -f compose.local.yaml up -d --build
```

Omni Money側TOTPを有効にする場合は、イメージ内のhelperを同じ非rootユーザーで実行します。認証アプリのコード確認が成功するまでファイルは作られません。

```bash
docker compose -f compose.yaml -f compose.local.yaml run --rm --no-deps \
  --entrypoint /app/omni-totp omni-money \
  --out /app/data/omni-money-totp.secret --issuer "Omni Money" --account admin
# .envへ次を設定してコンテナを再作成する:
# AUTH_TOTP_SECRET_FILE=/app/data/omni-money-totp.secret
# AUTH_REQUIRE_TOTP=true
docker compose -f compose.yaml -f compose.local.yaml up -d --force-recreate
```

bcryptハッシュは `$` を含むため、`.env` では例のとおり値全体をシングルクォートで囲んでください。

Composeはコンテナのroot filesystemをread-onlyにし、Linux capabilityをすべて削除して、権限昇格を禁止します。永続的に書き込めるアプリ領域は `/app/data` だけです。SQLiteの一時ファイルには再起動で消える `/tmp` tmpfsを使い、CPU・memory・PIDにも上限を設定します。既定値は `.env.example` の `OMNI_CPU_LIMIT`、`OMNI_MEMORY_LIMIT`、`OMNI_PIDS_LIMIT`、`OMNI_TMPFS_SIZE` で調整できます。上限を下げる場合は、最大サイズの画像処理、snapshot作成・復元、CSV取込を実データ量で検証してください。

TrueNAS Custom Appでは `compose.yaml` 相当の設定を使い、次を守ってください。

- `/app/data` を `/mnt/<pool>/apps/omni-money` 等の永続Datasetへ割り当てる
- Pangolin運用ではWebの`ports:`を削除し、Newtとだけ共有する専用networkへ接続する
- LAN限定の移行構成でもWeb publish先はTrueNASの正確な固定IPへ限定し、`ALLOWED_HOSTS`を一致させる
- AIのコンテナポート4001は公開しない（管理画面からの操作は同一コンテナ内relayを使う）
- 外部公開はPangolin等でTLS終端し、`TRUSTED_PROXIES`をNewtの固定IPだけに設定する

設定値は次のように生成できます。bcrypt生成はパスワードを対話入力するため、シェル履歴へ平文を残しません。TOTP秘密は `omni-totp` で生成し、Pangolin側のTOTP秘密と共有しないでください。

```bash
docker run -it --rm httpd:2.4-alpine htpasswd -nBC 12 omni
```

出力は `omni:` より後ろのbcryptハッシュだけを `AUTH_PASSWORD_HASH` に設定します。

## AI API

AI API は `AI_CREDENTIALS_FILE` を設定した場合のみ、公開Webとは別のAI専用リスナーで利用できます。`ai-credential issue` が一度だけ標準出力へ返す生トークンを安全なsecretへ保存し、リクエストのBearer認証に使います。JSONにはSHA-256ハッシュしか保存されません。

```bash
curl -X POST http://127.0.0.1:4001/api/v1/ai/analysis \
  -H 'Authorization: Bearer <AI_CREDENTIAL>' \
  -H 'Content-Type: application/json' \
  -d '{}'
```

取引追加には、再送しても重複登録されないよう、要求ごとに生成した16〜128文字の
`Idempotency-Key` が必須です。通信結果が不明な場合は同じ本文と同じkeyで再送してください。
同じkeyを異なる本文へ再利用すると409、資格情報ごとのUTC日次上限を超えると429になります。

```bash
curl -X POST http://127.0.0.1:4001/api/v1/ai/transactions \
  -H 'Authorization: Bearer <AI_CREDENTIAL>' \
  -H 'Idempotency-Key: <RANDOM-REQUEST-ID>' \
  -H 'Content-Type: application/json' \
  -d '{"account":"現金","date":"2026-08-09","item":"食費","type":"expense","amount":1000}'
```

利用可能なエンドポイント:

| Method | Path | 説明 |
| --- | --- | --- |
| `POST` | `/api/v1/ai/transactions` | 取引を追加 |
| `POST` | `/api/v1/ai/analysis` | 条件指定で収支を分析 |

AI API では `POST` のみ許可され、`GET`、`PUT`、`DELETE` などは拒否されます。
公開Webポート `:4000/api/v1/ai/*` ではAIトークンを受け付けません。

資格情報のscopeは `transactions:create`、`analysis:summary`、`analysis:transactions`、`analysis:memo`、`console:relay` です。AI取引の日次上限は `--max-transactions-per-day`（既定100、1〜1000）で設定し、成功件数とidempotency情報はSQLiteへ原子的に保存されます。raw idempotency keyは保存・記録しません。分析は既定で集計だけを返し、資格情報で許可された単一口座・固定日付範囲の中で最大30日（資格情報の上限が短ければその日数）へ自動的に絞ります。日付窓をずらして資格情報の固定範囲外を読むことはできません。タグの付与とタグ指定分析は、資格情報に列挙したタグIDだけを許可します。明細は `include_transactions: true`、メモはさらに `include_memo: true` と対応scopeが必要で、最大500件のカーソルページングです。

資格情報または監査keyringの更新は、ホスト上の通常ファイルを直接参照する構成では更新後に `SIGHUP` を送ると無停止で反映されます。不正な置換ファイルは拒否され、それぞれ直前の有効なsnapshotを維持します。Compose Secretsは実行中コンテナ内で置換内容が見えない場合があるため、更新後に `docker compose -f compose.yaml -f compose.ai.yaml up -d --force-recreate omni-money` を実行してください。

監査HMAC鍵はBearer credentialから完全に分離されています。同じ口座参照はBearer rotationを跨いでも変わりません。計画rotationでは次のコマンドが旧鍵を期限付きpreviousとして保持し、overlap中の監査イベントだけにcurrent/previous両方の非可逆参照と非秘密key IDを記録します。overlap終了後はpreviousを削除してください。

```bash
go run ./cmd/ai-audit-key rotate \
  --file secrets/ai_audit_hmac.json \
  --overlap 168h
# overlap終了後
go run ./cmd/ai-audit-key retire \
  --file secrets/ai_audit_hmac.json
```

CLIはraw鍵を標準出力へ返さず、初期作成は既存fileを上書きしない0600作成、rotationは0600の一時fileを同期したatomic置換で行います。keyringはリポジトリ外かつ所有者管理の親directoryで保管し、通常fileは0400/0600、Composeでは`/run/secrets`直下のread-only secretとしてmountしてください。WindowsではUnix modeに相当するDACL検証を安全に代替できないため、server modeのraw secret読込はfail closedです。Windows desktop modeはAI listenerを起動しないためkeyringを必要としません。APIアクセスはcredential ID、mTLS証明書fingerprint、key ID付きHMAC口座参照、期間、明細種別、該当／返却件数を、Web中継は実クライアントIPを構造化監査ログへ残します。トークン・HMAC鍵・本文・項目・メモ・金額は記録しません。運用環境では標準出力をアクセス制限された永続ログへ転送してください。

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

### AI APIの資格情報・通信境界

AI APIは単一の環境変数トークンではなく、期限・操作scope・口座制約を持つハッシュ化済み資格情報ファイルで有効化します。管理画面relay用トークンとAIクライアント資格情報は分離し、非loopback待受ではTLS 1.3/mTLSを必須にします。

- `AI_CREDENTIALS_FILE`: ハッシュ化済みAI資格情報JSON
- `AI_CONSOLE_TOKEN_FILE`: 管理画面relay専用の生トークンファイル
- `AI_TLS_CERT_FILE` / `AI_TLS_KEY_FILE` / `AI_TLS_CLIENT_CA_FILE`: AIサーバーmTLS
- `AI_TLS_CA_FILE` / `AI_TLS_CLIENT_CERT_FILE` / `AI_TLS_CLIENT_KEY_FILE`: 管理画面relayクライアントmTLS

Dockerでは `compose.ai.yaml` を重ね、資格情報と鍵をread-only secretとしてmountしてください。通常の `compose.yaml` だけではAI APIは起動しません。
