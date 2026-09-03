# Agent.md: Omni Money 開発・運用仕様（現行契約）

本資料は Omni Money の現行実装と運用境界を示す。実装の真実は Go/Vue ソース、`compose.yaml`、`.env.example`、Dockerfile、CI workflow とする。旧設計と `legacy_reference/` は参照専用であり、production capability ではない。

### 現行モード契約

| モード | 認証・データ | 現行制約 |
| --- | --- | --- |
| Desktop | Wails、roleを持たない単一 local vault | 起動時 locked、password/recovery、5〜120分（既定15分）のidle lock。AIは非提供。 |
| multi-user server | Docker/headless、control DB と user ごとの SQLCipher vault | Argon2id envelope、invite/reset、role、passkey。AIは非提供。 |

server の金融 API は authenticated principal に束縛された vault lease のみを受け取り、Desktop/global DBへfallbackしない。`/api/v1/ai/*` と `/api/ai-console/*` はproductionで404、feature statusのAIはfalseである。

現行の責務は `backend/api`（router/CSV/snapshot）、`backend/control`（identity/role/envelope metadata）、`backend/core`（vault-bound service）、`backend/database`（ledger/CSV/manual snapshot）、`backend/desktopaccount`（local lifecycle）、`backend/keyenvelope`（Argon2id/AES-GCM）、`backend/serverauth`、`backend/securedb`、`backend/vault`（lease/drain）に分かれる。`frontend/src` はUIとDesktop idle-lock、`scripts` は固定SQLCipherとsafe-update、`legacy_reference` は非実行参照、`compose*.yaml` と `Dockerfile` はserver配備を担当する。

`backend/database` に自動 snapshot の部品・テストは残るが、production serverの自動スケジューラには未接続である。server snapshotは本人が明示的に呼ぶAPIのみである。

## 1. プロジェクト概要
Go/Vueで構成された ledger を、利用者端末の Desktop と Docker/headless の multi-user server の2モードで提供する。旧Python版は `legacy_reference/` の参照資料に限る。

* **画面側（フロントエンド）**: Vue.js 3 (Composition API)
* **サーバー側（バックエンド）**: Go
* **デスクトップアプリ化**: Wails
* **データベース**: SQLite
* **基盤構築・自動化（インフラ / CI・CD）**: Docker, GitHub Actions

## 2. 開発の手順と複数作業領域（git worktree）の活用
本計画は、主軸のコードを破壊しないよう、以下の手順に従って安全に開発を進めること。

1. **複数作業領域（git worktree）の活用**: AIエージェントの自律的な開発や検証においては、`git worktree` を積極的に使用すること。これにより、主軸（`main` ブランチ）の作業状態を汚染することなく、複数の分岐（ブランチ）での作業を安全に並行して進める。
2. **分岐（ブランチ）の作成**: 利用者が指定したworktree/branchだけを使用する。勝手なbranch作成・自動merge・GitHub操作は行わず、`gh`を前提にしない。
3. **実装**: 仕様に基づきプログラムを作成する。
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
│   ├── database/          # SQLite接続、ledger、CSV、手動snapshot（自動server接続なし）
│   ├── control/           # server identity、role、session metadata、key envelope
│   ├── desktopaccount/    # Desktop local password/recovery/lock lifecycle
│   ├── keyenvelope/       # Argon2id、AES-GCM、password/recovery/passkey envelope
│   ├── serverauth/        # server password/passkey/invite/reset authentication
│   ├── securedb/          # SQLCipher open/validation
│   ├── vault/             # per-user vault manager、lease、drain、zeroize
│   ├── models/            # データベースの構造定義（ORMモデル）
│   └── middleware/        # session、CSRF、proxy、rate/security boundary
├── frontend/              # Vue.js 画面側（フロントエンド）
│   ├── src/
│   │   ├── assets/        # 既存アプリから引き継ぐCSS、画像
│   │   ├── components/    # 再利用可能な画面部品
│   │   ├── views/         # 各画面（ページ）
│   │   ├── store/         # 状態管理（口座選択状態などの保持）
│   │   └── utils/         # 通信処理などの補助機能
│   └── package.json
├── legacy_reference/      # 参照専用。productionに組み込まない
├── build/                 # Wails用のアイコン等 構築用資材
├── Dockerfile             # サーバーモード用のコンテナ定義
├── VERSION                # アプリバージョン（セマンティックバージョニング、CI/CDトリガー）
├── main.go                # Wailsアプリ用の起動地点
├── server.go              # サーバーモード（Docker）用の起動地点
├── wails.json             # Wails設定ファイル
└── Agent.md               # 現行仕様

```

## 5. 既存画面設計（UIデザイン）の踏襲と解析（重要）

開発用エージェントは視覚的なデザインを確認できないため、既存のデザインを維持するために以下の手順を厳守すること。

1. **既存資産の解析と移行**:
* `legacy_reference/` フォルダ内に配置された旧アプリのコードを必ず参照し、仕様を解析すること。
* 特に `legacy_reference/static/css/style.css` およびHTML雛形（`legacy_reference/templates/*.html`）を解析対象とする。
* 使用されているCSS変数（例：色、文字の大きさ）、クラス名、HTMLの文書構造を正確に把握する。
* `frontend/src/assets/` に既存のCSSを配置し、Vueの構成部品内でそれを読み込んで使用する。
* すりガラス調（グラスモーフィズム）の表現に必要な半透明設定、背景ぼかし（`backdrop-filter`）などのCSS属性を完全に維持すること。


2. **部品化（コンポーネント化）時の注意**:
* Vueの構成部品に分割する際も、既存のHTML構造とクラス名との対応関係を壊さないように注意する。



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

* **スナップショット**: `backend/database/` の自動作成部品は参照・テスト用であり、production serverの自動スケジューラには未接続である。現行serverは認証済み本人の明示的な手動APIだけを提供し、snapshotは同じvault DEKの暗号文として扱う。application Admin/APIには他userの平文を開示しないが、同じservice UID、host root/operator、binary、process memoryはtrust boundary内である。snapshot単体はDR setではなく、control DB/key、vault/snapshot、volume recovery material、recovery codeを揃える。



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
* **passkey**: WebAuthn PRF の鍵で vault DEK を別 envelope に包む。旧 `AUTH_TOTP_SECRET_FILE` / `AUTH_REQUIRE_TOTP` は現行仕様外で、値が存在すれば production 起動を拒否する。
* **公開 allowlist**: unauthenticated route は `backend/middleware/session.go` の exact allowlist と server router を source of truth とする。代表例は静的配信、login/status、初回 bootstrap、passkey login options/finish、invite acceptance、password-reset completion であり、その他の API は認証必須である。allowlistを文書で再実装しない。

#### 6.7.2. HTTPS / TLS およびリバースプロキシ対応

TLS、trusted proxy、host allowlist、直接TLS終端の挙動は `backend/middleware/proxy.go`、`backend/middleware/security.go`、`backend/config/server.go` と関連テストを source of truth とする。公開構成では固定 trusted proxy と `ALLOWED_HOSTS` を設定し、任意の forwarded header や Host を信頼しない。未実装の数値・header契約をここで追加しない。

#### 6.7.3. レート制限（Rate Limiting）

外部公開時のrate limitは現行 `backend/middleware/` 実装とテストを source of truth とする。ログイン等の認証境界を弱めず、AI用の未提供 endpoint や旧数値契約を追加しない。

#### 6.7.4. セキュリティヘッダーとCORS

security headers と CORS は `backend/middleware/security.go` と server router の現行実装・テストを source of truth とする。認証付きAPIで wildcard を許可せず、headers/CORS の未検証の固定値をこの文書に複製しない。

#### 6.7.5. サーバーモード起動時の環境変数一覧（詳細はsource of truthへリンク）

server環境変数の完全な source of truth は [`.env.example`](.env.example) と [利用ガイド](docs/how-to-use.md) である。主要な必須値は `CONTROL_DB_PATH`、`CONTROL_DB_ENCRYPTION_KEY_FILE`、`VAULT_ROOT`、`DATA_AT_REST_MODE`、`DATA_AT_REST_ATTESTATION_FILE`、`ALLOWED_HOSTS` とする。control key はattested data root外、vault rootはattested data root内の専用領域に置く。

旧 `DB_PATH`、`DB_ENCRYPTION_KEY_FILE`、`AUTH_PASSWORD_HASH`、`AUTH_REQUIRE_TOTP`、`AUTH_TOTP_SECRET_FILE` および `AI_API_TOKEN`、`AI_CREDENTIALS_FILE`、`AI_CONSOLE_TOKEN_FILE`、`AI_HOST_IP`、`AI_PORT`、`AI_ALLOW_REMOTE`、AI TLS関連fileは設定時に production 起動を拒否する。文書で旧名を紹介するときは廃止設定であることを明記する。


## 7. データベース設計（SQLite）

既存の構成を拡張し、以下の構造体（テーブル）を実装すること。

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
  * UNIQUE制約: `(name, parent_id)` の組み合わせで一意

* **TransactionTags（取引タグ紐付け）**
  * `transaction_id`: 取引ID（外部キー）
  * `tag_id`: タグID（外部キー）
  * 複合主キー: `(transaction_id, tag_id)`

* **Settings（設定情報）**
  * ledger設定はschema migrationと現行database実装に従う。root tag名は正規化され、legacy duplicate markerを除外するpartial unique indexで一意性を維持する。



## 8. 自動構築（CI/CD）の要件

**重要方針**: 正式な配布 build は workflow の固定 toolchain で行う。Desktop は固定 Wails v2.11.0 と固定 SQLCipher、server は Dockerfile または固定 SQLCipher と `server libsqlite3 sqlite_omit_load_extension` tags を使う。latest tag、bare Wails、未固定 tag は使わない。

### 8.1. バージョン管理とリリーストリガー

リポジトリのルートに `VERSION` ファイルを配置し、セマンティックバージョニング（`MAJOR.MINOR.PATCH`）で管理する。

* **`VERSION` ファイルの形式**: ファイルには `0.1.0` のようなバージョン文字列のみを1行で記載する。改行以外の余分な文字は含めない。
* **初期値**: `0.1.0` から開始する。
* **更新規則**:
  - `PATCH`（例: 0.1.0 → 0.1.1）: バグ修正、軽微な改修
  - `MINOR`（例: 0.1.1 → 0.2.0）: 機能追加、画面変更
  - `MAJOR`（例: 0.2.0 → 1.0.0）: 破壊的変更、大規模刷新
* **CI/CDトリガー条件**: `VERSION` 起点の release workflow と、PR/push/schedule の検証 workflow を分ける。Desktop release は `VERSION` と実行可能コード・build設定に関係する path の変更時だけ起動する。

### 8.2. GitHub Actions ワークフロー構成

`.github/workflows/` の既存 workflow を source of truth とする。CI は security、SQLCipher fail-closed、safe-update、Compose boundary、docs contract を検証する。

#### 8.2.1. デスクトップ用構築（`release-desktop.yml`）

* **トリガー**: `main` ブランチへの `push` イベントで、`paths` フィルターにより `VERSION` ファイルが変更された場合のみ実行する。
  ```yaml
  on:
    push:
      branches: [main]
      paths: ['VERSION']
  ```
* **バージョン読み取り**: ワークフロー冒頭で `VERSION` ファイルの内容を読み取り、環境変数 `APP_VERSION` に格納する。
  ```yaml
  - name: Read version
    run: echo "APP_VERSION=$(cat VERSION)" >> $GITHUB_ENV
  ```
* **ビルドマトリクス**: macOS Intel (`darwin/amd64`)、macOS Apple Silicon (`darwin/arm64`)、Windows (`windows/amd64`)、Linux (`linux/amd64`) の4 artifactを固定 toolchain で並列生成する。
* **Wails/SQLCipher**: Wails v2.11.0 を固定し、各OSの `scripts/build-sqlcipher-*.sh` と release workflow の固定 tags/CGO 設定を使用する。
* **バージョンの埋め込み**: ビルド時に `-ldflags "-X main.version=${{ env.APP_VERSION }}"` を用いてバイナリにバージョン情報を埋め込む。
* **GitHub Releasesへの発行**: 既存 workflow の固定 action と release 手順に従う。GitHub操作を手作業で代替したり、未承認のPR/Issue操作を行ったりしない。
* **重複リリース防止**: 同一バージョンのReleaseが既に存在する場合はスキップする。

#### 8.2.2. コンテナ用構築（`release-docker.yml`）

* **トリガー**: `main` の `VERSION` 変更時に実行し、release workflow の実際の path filter を変更しない。
* **Dockerイメージの構築**: サーバーモード用の `Dockerfile` を用いてコンテナイメージを構築する。ビルド引数でバージョンを渡す。
  ```yaml
  - name: Build and push Docker image
    uses: docker/build-push-action@v5
    with:
      push: true
      tags: |
        ghcr.io/${{ github.repository }}:${{ env.APP_VERSION }}
        ghcr.io/${{ github.repository }}:latest
      build-args: |
        VERSION=${{ env.APP_VERSION }}
  ```
* **登録先**: GitHub Container Registryへ version tag を公開し、安定版だけ既存 workflow の方針に従って `latest` を更新する。
* **マルチアーキテクチャ**: linux/amd64 と linux/arm64 の両方を構築する。

### 8.3. バージョンのアプリへの埋め込み

`VERSION` ファイルの値を実行時に参照する仕組みは、現行の `main.go`、frontend build設定、Dockerfile、release workflowをsource of truthとして維持・検証する。

* **Go側**: `main.go` にパッケージ変数 `var version = "dev"` を定義する。CI/CDでのビルド時に `-ldflags` でこの変数を上書きする。ローカル開発時は `"dev"` のまま動作する。
* **フロントエンド側**: ビルド時に `VITE_APP_VERSION` 環境変数として渡し、Vue.jsから `import.meta.env.VITE_APP_VERSION` で参照可能にする。画面のフッターやバージョン情報画面で表示する。
* **Docker**: `Dockerfile` 内で `ARG VERSION=dev` を定義し、 ビルド時の `--build-arg` で渡す。環境変数として実行時にも参照可能にする。


## 9. 開発端末の導入済み環境について
本計画を自律的に進めるにあたり、実行環境（M4 Proチップ搭載 Mac / zsh環境）には以下の技術要素が既に導入済みである。AIエージェントはこれらを前提として各種命令を実行し、動作確認やプログラムの構築（ビルド）を行うこと。

1. **Go言語 (`go`)**
   - **用途**: サーバー側の開発、およびWailsを介したデスクトップアプリの構築。
   - **使用方法**: 依存関係の解決とGo検証に使用する。server確認はDockerfile、または固定SQLCipherと`server libsqlite3 sqlite_omit_load_extension` tags/CGO設定で行い、通常SQLiteのserver起動は行わない。

2. **Node.js および npm (`node`, `npm`)**
   - **用途**: 画面側（Vue.js）の構成部品の取得、および静的ファイルの構築。
   - **使用方法**: `frontend` フォルダ内での `npm install`（依存関係の追加）や `npm run build`（画面側の構築）に使用すること。

3. **C言語翻訳プログラム（Xcode Command Line Tools / `clang` または `gcc`）**
   - **用途**: SQLiteデータベースをGoで動かすための仕組み（cgo）の利用、およびMac向けWailsアプリの画面描画処理の構築に必須となる。
   - **使用方法**: 固定SQLCipher build script と workflow の固定 tags/CGO 設定から利用する。通常SQLiteへのfallbackはしない。

4. **Wails v2.11.0**
   - **用途**: Desktop 4 artifactの固定版build。
   - **使用方法**: release workflowと同じ固定版・SQLCipher・build tagsを使う。latest tagやbare CLIを使わない。

5. **Docker仮想化環境（OrbStack / `docker`）**
   - **用途**: サーバーモード（Dockerコンテナ）の動作確認およびイメージ構築。
   - **使用方法**: 端末はApple Silicon（M4 Pro）であるため、ARM構造（`linux/arm64`）での動作を基本とすること。`Dockerfile` の動作検証として `docker build` や `docker run` を適宜実行し、サーバー単独での正常動作を確認すること。


## 10. AIエージェントへの実装指示（ステップ）

開発は必ず以下の順序で進めること。

* **ステップ1〜5**: 現行のfolder責務、Vue UI、database schema、vault-bound service、既存UIを維持・検証する。新規実装時はsourceとsecurity boundaryを先に照合する。
* **ステップ6**: AI用APIは実装しない。自動状態保存部品もproduction serverへ接続しない。将来設計は `docs/ai-integration-roadmap.md` に隔離する。
* **ステップ7〜9**: 現行 Dockerfile/server起動、固定SQLCipher build、CI/release workflow、server middlewareとDesktop local authを維持・検証する。セキュリティ・認証の契約は §6.7 と各source/testを参照する。
