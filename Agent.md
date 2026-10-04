# Agent.md: Omni Money 開発・運用仕様（現行契約）

本資料は Omni Money の現行実装と運用境界を示す。実装の真実は Go/Vue ソース、`compose.yaml`、`.env.example`、Dockerfile、CI workflow とする。旧Python版のソースは現行ツリーに含まれない。将来設計とproduction capabilityを区別する。

### 現行モード契約

| Capability | Desktop | multi-user server |
| --- | --- | --- |
| Ledger / credential lifecycle | Wails、roleなしの単一 local vault、password/recovery、idle lock | Docker/headless、control DBとuser vault分離、Argon2id envelope、invite/reset、role、passkey、session/vault lease |
| CSV v3 | transactions/images/tags/links/ledger settingsを含む平文full ledger | 同左。auth/control/key、snapshot、volume recoveryは含まない |
| Snapshot | local vaultの手動create/list/restore API。SQLCipher暗号文 | 本人のrequest leaseに束縛された手動create/list/restore API。同じvault DEKの暗号文 |
| Automatic snapshot | `core.Service`のledger mutation成功後に非同期作成。burst coalesce、30世代/容量上限。時刻・retention設定/失敗通知UIなし | 同左。user vault単位で作成 |
| AI | production 非提供 | production 非提供。旧AI設定は列挙分を起動拒否 |
| Schema / legacy migration | schema migrationと明示的な旧root DB移行 | schema migrationのみ。旧single-DB serverからmulti-userへの自動移行は非提供、CSV v3による手動移行が必要 |
| safe-update | server用safe-update対象外。固定artifactをrelease workflowで検証 | project `omni-money` のcompose/env/digestを固定検証し、固定imageをatomic更新 |

server の金融 API は authenticated principal に束縛された vault lease のみを受け取り、Desktop/global DBへfallbackしない。`/api/v1/ai/*` と `/api/ai-console/*` はproductionで404、feature statusのAIはfalseである。

現行の責務は `backend/api`（router/CSV/snapshot/save receipt）、`backend/control`（identity/role/envelope metadata）、`backend/core`（vault-bound service）、`backend/database`（ledger/CSV/snapshot/save worker）、`backend/desktopaccount`（local lifecycle）、`backend/keyenvelope`（Argon2id/AES-GCM）、`backend/serverauth`、`backend/securedb`、`backend/vault`（lease/drain）に分かれる。`frontend/src` はUIとDesktop idle-lock、`scripts` は固定SQLCipherとsafe-update、`compose*.yaml` と `Dockerfile` はserver配備を担当する。

`core.Service` は各Desktop/server vault instanceの `StartAutoSnapshot` に束縛され、財務mutation成功後に非同期snapshotを作成する。これはwall-clock schedulerではなく、時刻・retention policy・失敗通知を利用者が設定する製品機能はない。

## 1. プロジェクト概要
Go/Vueで構成された ledger を、利用者端末の Desktop と Docker/headless の multi-user server の2モードで提供する。

* **画面側（フロントエンド）**: Vue.js 3 (Composition API)
* **サーバー側（バックエンド）**: Go
* **デスクトップアプリ化**: Wails
* **データベース**: SQLite
* **基盤構築・自動化（インフラ / CI・CD）**: Docker, GitHub Actions

## 2. 開発の手順と複数作業領域（git worktree）の活用
本計画は、主軸のコードを破壊しないよう、以下の手順に従って安全に開発を進めること。

1. **作業領域の選択**: 既存の作業状態を確認し、利用者が指定または作成を許可したworktree/branchを使用する。既存の未コミット変更を上書きしない。
2. **分岐（ブランチ）の作成**: 利用者の明示的な依頼に従う。GitHub上のPR・Issue・branch操作はGitHub連携ツールを使い、`gh`は使用しない。
3. **実装**: 利用者が承認した変更の範囲で、現行仕様に基づき編集する。
4. **変更要求（Pull Request / PR）の作成**: 利用者から明示的に依頼された場合だけ、指定のGitHub連携手段で行う。
5. **確認（レビュー）と修正**: 人間による確認を受け、必要に応じて修正を行う。
6. **統合（マージ）**: 承認後、`main` ブランチに統合する。
* **注意**: 直接 `main` ブランチへ変更を確定（コミット）してはならない。必ず変更要求を経由すること。

## 3. 構造と動作方式（アーキテクチャと動作モード）
一つのソースコード群から、以下の2つの動作方式を構築できるように設計すること。

1. **Desktop (Wails)**: OS標準windowでVue UIを表示する。single local vaultとして起動時はlocked、password/recoveryとidle-lockでvaultを開く。
2. **multi-user server (Docker/headless)**: Wailsを使わずVue静的成果物とHTTP APIを配信する。control DB、user vault、session/vault leaseを分離し、全金融APIをauthenticated principalへ束縛する。

## 4. フォルダ構成
役割を明確に分離し、既存の参照用コードと混同しないよう、以下の構成を厳守して実装すること。

```text
/
├── .github/
│   └── workflows/         # 自動構築・配信（CI/CD）定義
├── backend/               # Go言語 サーバー側（バックエンド）
│   ├── api/               # APIの接続口定義、通信経路（ルーティング）
│   ├── core/              # アプリケーションの主要な論理処理（ビジネスロジック）
│   ├── database/          # SQLite接続、ledger、CSV、snapshot lifecycle
│   ├── control/           # server identity、role、token metadata、key envelope
│   ├── desktopaccount/    # Desktop local password/recovery/lock lifecycle
│   ├── keyenvelope/       # Argon2id、AES-GCM、password/recovery/passkey envelope
│   ├── serverauth/        # server password/passkey/invite/reset authentication
│   ├── securedb/          # SQLCipher open/validation
│   ├── vault/             # per-user vault manager、lease、drain、zeroize
│   ├── models/            # 取引等の構造体・request/response型（ORMではない）
│   └── middleware/        # session、CSRF、proxy、rate/security boundary
├── frontend/              # Vue.js 画面側（フロントエンド）
│   ├── src/
│   │   ├── assets/        # 既存アプリから引き継ぐCSS、画像
│   │   ├── components/    # 再利用可能な画面部品
│   │   ├── store/         # 状態管理（口座選択状態などの保持）
│   │   └── utils/         # 通信処理などの補助機能
│   └── package.json
├── build/                 # Wails用のアイコン等 構築用資材
├── Dockerfile             # サーバーモード用のコンテナ定義
├── VERSION                # アプリバージョン（セマンティックバージョニング、CI/CDトリガー）
├── main.go                # Wailsアプリ用の起動地点
├── server.go              # サーバーモード（Docker）用の起動地点
├── wails.json             # Wails設定ファイル
└── Agent.md               # 現行仕様

```

## 5. 既存画面設計（UIデザイン）の踏襲と解析（重要）

現行の `frontend/src/components/` と `frontend/src/assets/` を画面設計の基準とする。変更前にCSS変数、クラス名、モーダル、レスポンシブ表示、Desktopのwindow操作を確認する。

* 既存の色、背景ぼかし、半透明設定、文字サイズ、操作感を尊重する。
* コンポーネントを分割してもDOM構造とCSSの対応を維持する。
* UI変更ではcomponent testと、利用可能なブラウザで表示・操作を確認する。ローカルDockerを必要とする検証は、明示的な依頼がある場合だけ行う。



## 6. 機能要件

### 6.1. 既存からの移行必須機能

* **クレジットカード機能**: クレジットカードとして登録した項目は、残高計算およびグラフ表示から除外する機能を維持すること。
* **取引記録**: 日時、項目、金額、種別（収入・支出）の正確な記録と保持。
* **データ管理**: 取引履歴の検索、手動CSV v3 full-ledger export/importを維持する。v3はtransactions/images/tags/links/ledger settingsを含むが常に平文で、auth/control/key、snapshot、volume recovery materialは含まない。v1/v2はappend互換のみで、replaceは拒否する。
* **複数口座管理**: 金融項目を複数登録し、画面上で任意の個数を選択して表示・合算する機能。

### 6.2. 新規追加機能

* **メモ機能**: 取引履歴のデータ構造に「メモ（文字列）」を追加し、画面から読み書きできるようにする。
* **検索範囲**: 検索機能は項目名（`item`）だけでなく、メモ（`memo`）の内容も対象とする。SQLクエリでは `item LIKE ? OR memo LIKE ?` の形式で両方を検索する。
* **取引の紐付け（リンク）機能**:
  * 取引同士を関連付ける中間表（`transaction_links`）を実装する。
  * 用途はクレジットカード支払い取引と銀行口座引き落とし取引の照合に限定する。
  * 紐付けの追加は「クレジットカード項目として設定された資金項目」と「銀行口座項目として設定された資金項目」の組み合わせだけ許可する。
  * 銀行口座項目は紐付け候補の分類にのみ使い、クレジットカード項目のように残高計算・残高推移から除外してはならない。
  * 取引更新や設定変更により既存の紐付けがこの条件を満たさなくなった場合は、不正な紐付けを削除して整合性を維持する。

### 6.3. スナップショット（手動APIとmutation連動）

Desktopとserverは手動create/list/restoreを提供し、`core.Service` が公開するledger mutationの成功後にはbound vault instanceの自動snapshotを非同期作成する。burst中は最大1回のfollow-upへcoalesceする。手動・自動とも共通のcreate処理が返却前に30世代と `SNAPSHOT_MAX_TOTAL_BYTES` の範囲へpruneし、自動workerも追加cleanupを行う。これは時刻schedulerではなく、時刻・retention policy・失敗通知の設定UIはない。server snapshotは認証済み本人のrequest leaseに束縛され、同じvault DEKの暗号文として扱う。application Admin/APIには他userの平文を開示しないが、同じservice UID、host root/operator、binary、process memoryはtrust boundary内である。snapshot単体はDR setではなく、control DB/key、vault/snapshot、volume recovery material、recovery codeを揃える。

### 6.4. AI向けAPI（廃止済み・将来設計）

Desktop と multi-user server の両 production mode では AI を提供しない。`/api/v1/ai/*` と `/api/ai-console/*` は 404 であり、旧AI環境変数を設定すると server の起動を拒否する。以下の旧API・資格情報・listener案は dormant legacy と、将来 user-vault-bound に再設計する Stage 4（planned/unshipped）の資料であり、現行機能として実装・文書化してはならない。詳細は [AI連携ロードマップ](docs/ai-integration-roadmap.md) を参照する。

旧AIのAPI、credential scope、専用listener、Discord連携案は retired legacy であり、ここでは仕様として実装しない。将来に再設計する場合も user/vault binding、明示的なscope、検証、audit、private transportを満たす Stage 4 の検討事項として [AI連携ロードマップ](docs/ai-integration-roadmap.md) を更新する。

### 6.5. 画像添付機能

取引に対して画像ファイルを添付できる機能を実装する。

* **保存方式**: 画像データはSQLiteのBLOBカラムに格納する（`transaction_images` テーブル）。1つの取引に対して複数の画像を添付可能とする。
* **GUI操作**: 取引追加・編集モーダルに画像添付エリアを設ける。以下の2つの方法で添付可能とする。
  * ファイル選択ダイアログからの画像ファイル選択
  * ドラッグ&ドロップによる画像ファイルの添付
  * 添付済み画像はサムネイルプレビューで表示し、個別に削除可能とする。
* **AI API対応**: 現行 production のAI APIは提供しない。画像の手動添付は通常のDesktop/server ledger UIだけで行う。
* **対応形式**: JPEG, PNG, GIF, WebP を許容する。

### 6.6. タグシステム

取引にタグを付与し、タグ別の収支分析を可能にする機能を実装する。

* **階層構造**: タグは最大3階層（タグ → サブタグ → サブサブタグ）をサポートする。例：`推し活` → `映画` → `超かぐや姫！`
* **タグの管理**: 取引追加・編集モーダルからタグの選択・新規作成を行えるようにする。階層ドロップダウンUI（タグ → サブタグ → サブサブタグ）を用いる。
* **タグ別円グラフ**:
  * Chart.jsを用いた円グラフでタグ別の収入・支出割合を表示する。
  * 円グラフの各セグメントにカーソルを合わせると、具体的な金額と割合をツールチップで表示する。
  * セグメントをクリックすると、そのタグの下位階層（サブタグ→サブサブタグ）に分解した円グラフを表示する（ドリルダウン）。
  * 期間フィルタ: 通期、年区切り、月区切り、日区切りの4つの区分で表示を切り替えられるようにする。

### 6.7. セキュリティ・認証基盤（サーバーモード外部公開対応）

HTTP middleware（session、CSRF、proxy、security headers、CORS、rate limit）は server mode にだけ適用する。ただし Desktop も HTTP middlewareを使わない local vault auth として password、recovery、idle-lock を持つ。外部公開時の詳細な実装契約は現行 source と [server model](docs/server-multi-vault.md) に従い、未実装の旧設計を追加しない。

#### 6.7.1. ユーザー認証とセッション管理（現行 server は multi-user）

現行 server は control DB の user identity と vault-bound session を使い、全API（明示された公開 account route と静的配信を除く）に認証を必須とする。旧 single-user 認証の説明は互換性参照用である。

* **認証方式**: セッションベース認証を基本とする。Cookieにセッション識別子を格納し、サーバー側でセッション状態を管理する。
* **パスワード**: 現行は固定 Argon2id profile と暗号化 envelope を使用し、`AUTH_PASSWORD_HASH`（bcrypt）は受け付けない。旧設定は値が存在すれば production 起動を拒否する。
* **セッション**: server-side session、CSRF、recent reauthentication、idle/absolute expiry、session concurrency は `backend/middleware/session.go` と関連テストを source of truth とする。高影響操作には現行実装が要求する再認証を適用し、AI操作という未提供機能を追加しない。
* **passkey**: 認証済みrequest leaseの開いたVault鍵を、control鍵から用途分離した専用鍵でwrapする。登録にアカウントpasswordの再入力やPRF assertionを要求しない。loginはWebAuthn署名・challenge・RP/origin・user handle・user verificationを検証してからcredentialに束縛したenvelopeを開く。旧PRF/password-required形式はpassword loginまたはログイン済みの設定画面で自動移行する。control鍵とcontrol DBを持つhost operatorにはcustody envelopeのDEKが復号可能だが、application Admin APIは他userのVaultを開けない。旧 `AUTH_TOTP_SECRET_FILE` / `AUTH_REQUIRE_TOTP` は設定時にproduction起動を拒否する。
* **ログアウト**: 通常logoutはsession family（並行する再認証rotationを含む）を失効してcookieを削除し、Vault cleanupは非同期に行う。受付済みwriteは自身のleaseで継続し、同じDEKを証明した新loginは処理中のinstanceを再利用できる。credential失効・disable・restore・shutdownの明示drainは新loginによる再利用を許可しない。frontendは私的状態を直ちに消去し、遅れて到着した応答をgenerationで拒否する。
* **公開 allowlist**: unauthenticated route は `backend/middleware/session.go` の exact allowlist と server router を source of truth とする。代表例は静的配信、login/status、初回 bootstrap、passkey login options/finish、invite acceptance、password-reset completion であり、その他の API は認証必須である。allowlistを文書で再実装しない。

#### 6.7.2. HTTPS / TLS およびリバースプロキシ対応

TLS、trusted proxy、host allowlist、直接TLS終端の挙動は `backend/middleware/proxy.go`、`backend/middleware/security.go`、`backend/config/server.go` と関連テストを source of truth とする。公開構成では固定 trusted proxy と `ALLOWED_HOSTS` を設定し、任意の forwarded header や Host を信頼しない。未実装の数値・header契約をここで追加しない。

#### 6.7.3. レート制限（Rate Limiting）

外部公開時のrate limitは現行 `backend/middleware/` 実装とテストを source of truth とする。ログイン等の認証境界を弱めず、AI用の未提供 endpoint や旧数値契約を追加しない。

#### 6.7.4. セキュリティヘッダーとCORS

security headers と CORS は `backend/middleware/security.go` と server router の現行実装・テストを source of truth とする。認証付きAPIで wildcard を許可せず、headers/CORS の未検証の固定値をこの文書に複製しない。

#### 6.7.5. サーバーモード起動時の環境変数一覧（詳細はsource of truthへリンク）

server環境変数の完全な source of truth は [`.env.example`](.env.example) と [利用ガイド](docs/how-to-use.md) である。主要な必須値は `CONTROL_DB_PATH`、`CONTROL_DB_ENCRYPTION_KEY_FILE`、`VAULT_ROOT`、`DATA_AT_REST_MODE`、`DATA_AT_REST_ATTESTATION_FILE`、`ALLOWED_HOSTS` とする。control key はattested data root外、vault rootはattested data root内の専用領域に置く。

旧 `DB_PATH`、`DB_ENCRYPTION_KEY_FILE`、`AUTH_PASSWORD_HASH`、`AUTH_REQUIRE_TOTP`、`AUTH_TOTP_SECRET_FILE` および `AI_API_TOKEN`、`AI_CREDENTIALS_FILE`、`AI_CONSOLE_TOKEN_FILE`、`AI_AUDIT_HMAC_KEYRING_FILE`、`AI_HOST_IP`、`AI_PORT`、`AI_ALLOW_REMOTE`、`AI_TLS_CERT_FILE`、`AI_TLS_KEY_FILE`、`AI_TLS_CLIENT_CA_FILE`、`AI_TLS_CA_FILE`、`AI_TLS_CLIENT_CERT_FILE`、`AI_TLS_CLIENT_KEY_FILE`は設定時に production 起動を拒否する。文書で旧名を紹介するときは廃止設定であることを明記する。


## 7. データベース設計（SQLite）

ledger schema versionは6。構造・migration・index/trigger検証は `backend/database/database.go` と `backend/database/transaction_save_schema.go` を正とする。以下は主要なledgerデータで、control DBのuser/token/envelopeや廃止機能の互換テーブルとは区別する。

### サーバーの取引保存受付

`transaction_save_requests` は保存操作のUUID、内容hash、対象ID、state、入力、結果をVault DB内に保持する。取引エディターの作成・更新は `/api/transaction-saves` で入力をFULL synchronousで永続化し、202で受付を確認してからworkerが処理する。UUIDは操作単位で固定し、同じ内容の別操作は許可する。同じUUIDと異なる内容は409。取引反映とcompleted記録は同一SQL transactionで確定する。

ログアウトが待つのは送信・受付確認とsession失効までで、取引反映・画像処理・自動snapshotは待たない。処理中・失敗入力は本人だけが参照し、失敗入力と画像は本人による通知削除まで暗号化して保持する。ID/hashの再送防止記録は残す。再起動後のpendingは次回Vault unlockで再開する。通常HTTP bodyの上限は10 MiB、UIの画像原データは合計7 MiBまで。queue内部は1入力32 MiB、保持入力32件/合計128 MiBの上限を持つが、HTTP上限を緩和しない。

CSV v3はこの受付queueと再送防止記録を含まない。schema 6のsnapshotとwhole-data-root archiveはDB内の記録も保全する。過去のsnapshotへのrestoreは後の受付記録も巻き戻す。ブラウザの未確認入力はメモリのみで、永続offline outboxは提供しない。詳細は [API契約](docs/api-errors.md) と [server安全モデル](docs/server-multi-vault.md) を参照する。

### 主要なledgerデータ

* **Transactions（取引）**
  * `id`: 主キー
  * `account`: 口座名
  * `date`: 日時
  * `item`: 項目
  * `type`: 種別（income / expense）
  * `amount`: 金額
  * `balance`: 残高
  * `memo`: メモ

* **TransactionLinks（取引紐付け情報）**
  * `parent_id`: 親取引のID
  * `child_id`: 子取引のID

* **TransactionImages（取引画像）**
  * `id`: 主キー
  * `transaction_id`: 取引ID（外部キー）
  * `filename`: ファイル名
  * `data`: 画像データ（BLOB）
  * `mime_type`: MIMEタイプ（デフォルト: `image/jpeg`）
  * `created_at`: 作成日時

* **Tags（タグ）**
  * `id`: 主キー
  * `name`: タグ名
  * `parent_id`: 親タグID（NULLの場合はトップレベル）
  * `level`: 階層レベル（1: タグ、2: サブタグ、3: サブサブタグ）
  * childは `(name, parent_id)` の組み合わせをUNIQUE制約で一意にする。
  * active rootは `parent_id IS NULL AND legacy_duplicate = 0` を条件とする partial unique index `idx_tags_root_name_unique` で一意にする。legacy duplicateはarchive-only markerとして保持する。

* **TransactionTags（取引タグ紐付け）**
  * `transaction_id`: 取引ID（外部キー）
  * `tag_id`: タグID（外部キー）
  * 複合主キー: `(transaction_id, tag_id)`

* **Settings（設定情報）**
  * ledger設定はschema migrationと現行database実装に従う。



## 8. 自動構築（CI/CD）の要件

**重要方針**: 正式な配布 build は workflow の固定 toolchain で行う。Desktop は`go.mod` 指定版の Wails と固定 SQLCipher、server は Dockerfile または固定 SQLCipher と `server libsqlite3 sqlite_omit_load_extension` tags を使う。latest tag、bare Wails、未固定 tag は使わない。

### 8.1. バージョン管理とリリーストリガー

リポジトリのルートに `VERSION` ファイルを配置し、セマンティックバージョニング（`MAJOR.MINOR.PATCH`）で管理する。

* **`VERSION` ファイルの形式**: ファイルには `0.1.0` のようなバージョン文字列のみを1行で記載する。改行以外の余分な文字は含めない。
* **現行値**: リポジトリの `VERSION` を参照する。
* **更新規則**:
  - `PATCH`（例: 0.1.0 → 0.1.1）: バグ修正、軽微な改修
  - `MINOR`（例: 0.1.1 → 0.2.0）: 機能追加、画面変更
  - `MAJOR`（例: 0.2.0 → 1.0.0）: 破壊的変更、大規模刷新
* **CI/CDトリガー条件**: `VERSION` 起点の release workflow と、PR/push/schedule の検証 workflow を分ける。Desktop releaseは関連pathを変更したPRでbuild検証し、`main`では`VERSION`またはrelease workflow自体の変更時にpublishする。

### 8.2. GitHub Actions ワークフロー構成

`.github/workflows/` の既存 workflow を source of truth とする。CI は security、SQLCipher fail-closed、safe-update、Compose boundary、docs contract を検証する。

#### 8.2.1. デスクトップ用構築（`release-desktop.yml`）

* **PR**: build設定・実行可能コードに関係するpathの変更時に、release workflowが4 artifactのbuild検証を行う。PR buildは配布Releaseやversion tagを作成しない。
* **main**: `VERSION`またはrelease workflow自体の変更時に、`go.mod`指定版のWails・固定SQLCipher・固定tags/CGO設定でmacOS Intel (`darwin/amd64`)、macOS Apple Silicon (`darwin/arm64`)、Windows (`windows/amd64`)、Linux (`linux/amd64`) の4 artifactをbuildし、version releaseを公開する。
* **source of truth**: path filter、version埋め込み、固定action、重複release防止は `release-desktop.yml` を参照する。

#### 8.2.2. コンテナ用構築（`release-docker.yml`）

* **トリガー**: `main` の `VERSION`変更時に、Dockerfileと固定SQLCipher buildを使って実行する。PRのDesktop artifact buildとは別のserver image releaseである。
* **マルチアーキテクチャ**: linux/amd64 と linux/arm64 の両方を構築し、version tagを公開する。`latest`はstable releaseに限って更新する。
* **source of truth**: image action、権限、tag、push条件は `release-docker.yml` を参照し、無条件のlatest例を文書へ複製しない。

### 8.3. バージョンのアプリへの埋め込み

`VERSION` ファイルの値を実行時に参照する仕組みは、現行の `main.go`、frontend build設定、Dockerfile、release workflowをsource of truthとして維持・検証する。

* **Go側**: `main.go` にパッケージ変数 `var version = "dev"` を定義する。CI/CDでのビルド時に `-ldflags` でこの変数を上書きする。ローカル開発時は `"dev"` のまま動作する。
* **フロントエンド側**: ビルド時に `VITE_APP_VERSION` を渡し、Vue.jsから `import.meta.env.VITE_APP_VERSION` で表示する。
* **server / Docker**: `server.go` にも `var version = "dev"` があり、Dockerfileは `ARG VERSION=dev` をfrontendの `VITE_APP_VERSION`、Goの `-ldflags`、runtimeの `VERSION` へ渡す。配布workflowがルートの `VERSION` から同じ値を供給する。


## 9. 開発環境と検証範囲
端末のCPU、OS、Docker実装、導入済みCLIを固定して仮定しない。Goは `go.mod`、Nodeは `.node-version`、npm依存は `frontend/package-lock.json`、Wailsは `go.mod` の指定版を基準とする。Frontendの依存導入には `npm ci --ignore-scripts` を使用する。

正式なSQLCipher・Desktop・Docker buildとserver/browser E2EはGitHub Actionsで検証する。ローカルDockerの起動・buildやSQLCipherのsource buildは、利用者が明示的に依頼した場合だけ行う。変更に応じた検証を選び、負荷の高いbuild/race testを無断で並列実行しない。ドキュメントだけの変更では、リンク、現行ソースとの照合、CIのdocumentation contract、`git diff --check` を確認する。

## 10. 今後の変更時のguardrails

- mode、auth、vault、CSV、snapshot、AI、releaseの変更は、先に現行 source/test とこの文書の capability matrixを照合する。
- Desktop は roleのない local vault、server は control/vault分離を維持する。旧 single-DB server、bcrypt/TOTP、旧AI envを復活させない。
- production AIは現状非提供である。自動server snapshotはmutation連動で提供済みだが、時刻schedule・可変retention・失敗通知を追加する場合は、既存のuser-vault bindingと暗号化境界を保ち、別設計、明示的な承認、security testを必須にする。
- 配布は固定SQLCipher/Wails/Docker workflowを使う。コード変更に必要なGo/Nodeテスト、frontend build、security tests、actionlintは変更範囲に応じて実施し、ローカルの負荷と実行許可を確認する。全変更で `git diff --check` を確認する。
