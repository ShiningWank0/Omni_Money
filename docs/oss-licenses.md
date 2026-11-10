# OSSの表示・配布・更新

ログイン画面、Desktopの初期設定・ロック解除画面ではカード外側の下に置く、背景・枠・影のない文字リンクから「オープンソースライセンス」を開けます。通常画面では全ユーザー共通のメニュー下部に置きます。認証APIや財務データを使わず、同梱した原文を表示します。既存の `PanelCloseButton` を使うオーバーレイで、見出しと閉じるボタンを固定し、本文だけ縦スクロールします。長い識別子・URL・原文はスマートフォン幅でも折り返します。

## 掲載対象

- npm: 本番バンドルに残ったモジュールから取得します。`devDependencies` という理由だけでは除外しません。Viteのpreload helperは配布されるため対象に含め、テスト専用のVitest・Playwright・jsdomなどは含めません。
- Go: Desktop 4構成とserver 2構成について、実際のproduction/desktop/server、SQLCipher、WebKit用タグとCGO設定で `go list -deps` を行います。テスト・CLI・開発サーバーだけの依存を一覧に追加しません。
- LICENSE等: 使われるパッケージからmodule rootまでの親ディレクトリにあるLICENSE、COPYING、NOTICE、COPYRIGHT、PATENTSを保持します。WebView loaderなどの別ライセンス境界も対象です。
- ネイティブ: Goランタイム、SQLCipher、SQLite、OpenSSLを含めます。SQLCipher・macOS OpenSSL・Go toolchainの指定版と掲載版を照合します。
- フォント: 現在のNunitoは本番バンドルに含まれません。使用する変更ではフォントとOFLの掲載・同梱もレビュー対象にしてください。

画面・配布用テキストとも、その配布物のOS・CPUアーキテクチャ・Desktop/Serverに一致するOSSだけを掲載します。
ビルド時に`licenses/targets.json`の6構成のいずれかへ固定し、閲覧するブラウザのOSやCPU、ログイン状態からは推測しません。
共通で使うOSSは各構成へ掲載し、DesktopだけのWailsやWindowsだけのWebView、ServerだけのWebAuthnは該当する構成だけに含めます。
表示用JavaScriptには選択した構成の一覧だけを組み込み、他構成の一覧を隠すだけの実装にはしません。
OpenSSLはmacOSでは固定版だけを表示し、その他では利用する3.x系列を示します。別OSの版は併記しません。
配布物の`BUILD_DEPENDENCIES.txt`やAlpineパッケージ一覧では、ビルド時に確認した具体的な版も記録します。

DockerはBuildKitの`TARGETARCH`から`server-linux-amd64/arm64`を選択し、Desktop releaseはmatrixで4構成を明示します。
通常の`wails build/dev`は専用frontendコマンドでDesktopのホスト構成を選択します。
クロスビルドで直接Wailsを使う場合は、同じ対象を`VITE_OSS_TARGET`にも指定してください。
例えばdarwin/amd64向けなら、既存の固定版Wails呼出しに`VITE_OSS_TARGET=desktop-darwin-amd64`を渡します。
サポート外や矛盾した対象はビルドを失敗させます。

frontendビルドは公開ディレクトリ外に`oss-display-inventory.json`を生成し、対象・表示するOSSと版・一覧を含むJavaScriptのSHA-256を記録します。
Desktop/Dockerの対象別パッケージ作成時に、原本catalogとの一致、対象の取り違え、出力JSの変更を検査します。
構成別表示も同じ`thirdPartyNotices.json`と`targets`から生成するため、DependabotのOSS情報自動同期で更新する元データを重複させません。

## 共通チェック

`.github/workflows/ci.yml` の `OSS licenses` が唯一の入口です。通常CI・Desktop Release・Docker Releaseのすべてのビルドはこの成功を待ちます。後続のリリースファイルは `workflow_call` 専用であり、独立したpush/PR/manual起動はありません。既存の `Go tests` という必須チェック名は維持し、OSSチェック失敗・cancel・skipも不合格にします。

mainのVERSION変更時はDesktop/Dockerを実行し、PRでは変更パスに応じて配布ビルドを検証します。Dockerリハーサルは **CI > Run workflow > release_rehearsal** で実行します。リハーサルはversion/latestタグを公開しません。出所証明の署名元は既存の `release-docker.yml` のままです。

`licenses/approved.json` は構成要素ごとの確認済みバージョン、ライセンス種別、原文・NOTICEのSHA-256です。新規依存、版変更、原文の変更、未確認ライセンス、古い承認の残留、表示用データの更新漏れはチェックを失敗させます。チェック内で承認情報を自動更新することはありません。

`licenses/targets.json` に配布構成と実際のビルドタグを定義します。`licenses/dependency-usage.json` はnpm lock/Go module graph内の各依存について、版、直接・間接・開発依存の区別、配布対象／除外、対象構成、ライセンスと判定根拠を記録する生成物です。開発依存でも配布コードを含む場合は `included` になります。`externalTargets` はOS提供の実行時ライブラリを示します。最終配布物のmanifestにはアプリのリリース版も記録します。テストに配布OSS一覧やその版を複製しません。

`frontend/bundled-dependencies.json` もGit管理し、本番ビルドで得た一覧とコミット済みの一覧が一致することを共通チェックで確認します。依存の更新時にはこのファイルも同じPRに含めます。`licenses/build/` は配布構成・アプリ版ごとに生成し、共通チェックの `oss-license-inventories` artifactと最終配布物に保存します。原文・承認情報・対象構成・表示用一覧はGitでレビューできます。

UIのcomponent testに加え、共通チェックで6構成それぞれの本番frontendをビルドし、320/375/768/1280pxで実際の画面を検証します。
表示されるOSSのID・版・配布構成が原本catalogの対象別一覧と完全一致すること、他モードのOSSが混ざらないことを確認します。
横方向のoverflow、本文の縦スクロール、×ボタンの位置、閉じた後のフォーカス復帰も同じ検証に含めます。

## 依存更新時の手順

1. 依存とlockを更新して `cd frontend && npm ci --ignore-scripts && npm run build` を実行します。
2. リポジトリのルートから `go mod download` と `node scripts/oss-notices.mjs review` を実行します。
3. `licenses/build/review.json` の追加・変更・削除、原文、著作権表示、NOTICE、複数ライセンス、対象構成を確認します。必要に応じてネイティブ構成情報も更新します。
4. 確認した内容だけを `licenses/approved.json` に反映します。reviewの候補を無条件に承認する運用にはしません。
5. `node scripts/oss-notices.mjs refresh` で `frontend/src/generated/thirdPartyNotices.json` を更新し、再度frontendをビルドして `node scripts/oss-notices.mjs check` を実行します。
6. 承認・表示用データ・依存更新を同じPRでレビューします。生成物の元になる依存・原文は読み取り専用で照合します。

## 配布物の確認

Desktopはアーカイブ内の `licenses/`、macOSはappの `Contents/Resources/licenses/`、Dockerは `/app/licenses/` にアプリのLICENSEと対象別THIRD_PARTY_NOTICESを同梱します。

Desktopではバイナリの `go version -m` と確認済みの対象別モジュール一覧を照合します。SQLCipherの実際のライセンスをchecksum固定sourceから保存し、macOS/WindowsではOpenSSLの提供元のライセンスも保持します。圧縮後に展開し、文書の欠落・変更を検査してから出所証明・公開へ進みます。

単一ファイルからのserverビルドでは、Goがアプリ自身を `(devel)` の依存として記録します。`go.mod` から取得した `applicationModule` と完全一致し、置換されていないこの1件だけを外部OSSの比較から除外します。それ以外の追加・欠落・版違い・置換は不合格です。Windowsの原文照合では双方のCRLF/LFだけを揃え、著作権表示やライセンス本文の変更は引き続き検出します。

Dockerは独立したbuilderからマウントした検証器・manifestで、実際のファイルのハッシュ、SQLCipher原文、バイナリのモジュール・Go版を確認します。公開assetsの既存allowlistは広げません。ライセンスデータはJavaScriptに組み込まれ、一般のファイルシステムを公開しません。

## OSパッケージと自動判定の範囲

Dockerの最終段で、`apk upgrade` 後の実際のパッケージ版・ライセンス識別子・source origin・recipe commitを `ALPINE_PACKAGES.txt` と `alpine-installed.txt` に記録します。ビルド専用stageのパッケージは対象外です。source provenanceの不足はビルド失敗にします。

このOS一覧・ソース参照は、GPL等の対応ソース提供や書面によるオファーの代替ではありません。Alpine/BusyBox等の再配布条件と対応ソースの提供方法は、配布者として別途確認する必要があります。本チェックは、定義済みの掲載・同梱ルールと確認済み依存からの逸脱を検出するもので、すべてのライセンスの法的解釈を自動で保証するものではありません。
