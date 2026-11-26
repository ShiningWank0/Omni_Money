# 依存更新のレビュー

Dependabotは週次で確認し、各エコシステムの通常更新PR上限は5件とする。
Go・npm・GitHub Actions・Dockerのminor/patch更新をそれぞれまとめ、major更新は個別PRにする。
脆弱性修正はエコシステム毎に別グループとしてminor/patchをまとめ、majorは個別PRにする。
更新対象を恒久的にignoreしない。

定期検査が作成するIssueも同じ方針で、同一パッケージの複数advisoryは1件へ集約する。

## 定期監視の完了条件とBot報告

mainの定期実行と通常の手動実行は、問題の検出から報告までを行う監視として扱う。
CIの手動実行で`release_rehearsal`を指定した場合は、従来の配布前検証を適用する。

| 実行目的 | 問題を検出した際の動作 | 完了条件 |
| --- | --- | --- |
| 定期・手動監視 | 失敗内容を保存し、実行可能な残りの検査と報告を続ける | 問題・未検査範囲をIssueまたは既存Dependabot PRへ報告済み |
| PR・配布前検証 | テスト・整合性・脆弱性の既存判定を適用する | 必須の検証に成功 |

脆弱性、外部advisoryの変更、テスト失敗、検査不能、前提条件の不足による未実行を報告対象にする。
定期監視の成功は、脆弱性なし・全テスト成功という意味ではない。Summaryで問題と未検査の件数を確認する。
同じjob内の前提条件が壊れている場合は、その前提を必要とする処理を実行せず、未実行として報告する。
報告jobは固定ビルド環境や監視処理の回帰テストの成功に依存させない。

`continue-on-error`を適用する前の`steps.*.outcome`を各jobから保存する。
Actions APIのjob結果と合わせて集約し、継続設定によって成功に変わった失敗も報告する。
証跡の欠落・不正・別attemptの混入も検査不備として通知し、問題なしとして扱わない。
保存するstep証跡は名前と成否に限定し、actionのoutputsや認証情報をコピーしない。

新しい報告には`[Bot]`とGitHub Actionsによる自動作成である旨を表示し、`github-actions[bot]`が投稿する。
既存Dependabot PRに依存名またはadvisory識別子が完全一致する場合は、そのPRへBotコメントを追加する。
それ以外はIssueへ報告する。人が作成したIssue・PRの本文を上書きせず、同じ状態の再通知を避ける。
依存バージョンの固定と通常のDependabot更新PRは維持し、修正版が未確定の問題もIssueとして追跡する。

投稿は1実行25回までとし、個別報告しきれない内容はBotの集約Issueへ全件保存する。
必要に応じて本文を複数ページに分割し、次回は個別報告待ちを優先する。
この集約保存も投稿上限に含め、期限付きartifactだけに未報告の内容を残さない。
GitHub API障害や投稿容量の不足などで全件を届けられない場合は、監視処理を失敗として通知する。

Wails、WebAuthn、DockerのGo/Nodeは通常グループから除外し、個別PRで扱う。
Goの1.x間、WebAuthnの0.x間はSemVerではminorでも、関連設定やAPI移行が必要になるためである。

## Vue Test Utils 2.6のprops更新

Vue本体は3.5.45を維持し、テスト支援ライブラリを2.6.0へ更新する。
2.6では通常のオブジェクト・配列propsをテストツールが自動で深くreactiveにしない。
同じ通常オブジェクトを変更してから`setProps`へ再度渡しても、更新の通知にはならない。

- 親のreactiveな状態を変更する場面では、fixture作成時にVueの`reactive()`を明示する。
- API応答など通常の値を差し替える場面では、新しいオブジェクト・配列を`await wrapper.setProps(...)`へ渡す。
- 確認対象はDOMや子コンポーネントへ渡る表示データとし、テストツール内部の再描画だけを成功条件にしない。

残高グラフの入れ子の残高更新、タグの子要素追加、タグ一覧の差し替えをこの2経路で検証する。
既存の削除・保存失敗時の検査も維持する。
上流の[2.6.0リリース説明](https://github.com/vuejs/test-utils/releases/tag/v2.6.0)を参照。

## ビルド版の固定元

- Goの最低対応版は `go.mod` の `go`、CI/配布用固定版は `toolchain`。CIの `.go-version` も同じ版へ同期し、各workflowは `go-version-file: .go-version` を使う。`node scripts/runtime-versions.mjs check`でDocker・toolchain・CIの一致を検証する。
- NodeのCI/配布用固定版は `.node-version`。`frontend/package.json` の `engines` は対応範囲であり、固定版とは別にレビューする。
- Wails CLIは `go.mod` のWailsモジュールと同じ固定版を導入する。`latest` は使わない。
- DockerのGo/Nodeイメージは同じ版の `tag@sha256` を維持する。Alpineの系列変更は別途サポート範囲とSQLCipherの動作をレビューする。

DockerのGo/Node更新PRでは、新しいタグとdigestを確認してから次を実行し、関連ファイルを同じPRに含める。

```sh
node scripts/runtime-versions.mjs sync
node scripts/runtime-versions.mjs check
```

`sync` はDockerfileを基に `go.mod` のtoolchain、`.go-version`、`.node-version`を同期する。
Go最低対応版、Node対応範囲、API利用コード、ドキュメントの意味を自動変更するものではない。
最低対応版より古いGoイメージ、digest未固定や曖昧なビルドstageは拒否する。
CIの整合性チェックとGoテストは、Dockerだけの更新による固定版の不一致を拒否する。

### Go本体と標準ライブラリのセキュリティ更新

DependabotのGo modules更新だけでは、Go本体の`toolchain`を新しいリリースへ更新するPRを期待できない。
DockerのGoイメージ更新は、このリポジトリでは週次の確認であり、緊急修正の公開直後にPRが作成されるとは限らない。
`groups.exclude-patterns`の`golang`はグループ化から外して個別PRにする指定であり、更新を無視する指定ではない。

Go標準ライブラリの検出はCIの`govulncheck`も確認する。標準ライブラリの修正が必要な場合は、Go modulesの更新を待たずに、
DockerのGoイメージとdigest、`go.mod`の`toolchain`、必要なら最低対応版の`go`を同じPRで更新する。
`runtime-versions.mjs sync`は最低対応版を変更しないため、対応するGo系列の修正版を個別に確認する。
例えば`GO-2026-6859`はGo 1.26.11 / 1.27.4で修正されており、最低対応版を1.26.11、配布用固定版を1.27.4に設定する。
既存の配布済みバイナリやコンテナへ修正を適用するには、更新したGoで再ビルドした配布物への更新も必要になる。

参考：[Goのリリース履歴](https://go.dev/doc/devel/release)、
[Dependabotのtoolchain更新要望](https://github.com/dependabot/dependabot-core/issues/13520)。

## DependabotのNode固定版の自動同期

DockerのNode更新ではDependabotが`.node-version`を更新しないため、#243と#264で同じ不一致が発生した。
`dependabot-node-sync.yml`はCIの開始通知（`workflow_run: requested`）を受け、Nodeのminor/patch更新に同じPRへの同期コミットを追加する。
同期コミットから通常のPRチェックが新しく起動し、古いコミットのCIは既存のconcurrency設定でキャンセルされる。
初回CIが先に不一致を検出する場合もあるため、マージ判断には同期後の最新コミットの結果を使う。

対象は、このリポジトリ内のDependabot作成・main向け・openなNode Docker更新PRに限る。
Dockerfileは`frontend-builder`のdigest固定されたNodeイメージ1行だけの変更を許可し、同じmajor内の増分かつ既存の`engines.node`の対応範囲内であることを確認する。
Nodeのmajor更新、Goのtoolchain更新、ほかのコードを含むPR、手動変更された固定版は、引き続き前節の手順でレビューする。
digestだけの更新と同期済みのPRにはコミットを追加しない。

処理はworkflow自身の既定ブランチのコミットだけをcheckoutし、PRのファイルはGitHub API経由のデータとして読む。
PR内のスクリプト、依存関係、artifactは実行しない。
書き込み直前にPRを再確認し、検証したheadを親にして`.node-version`だけを変更する。
並行更新があればfast-forwardできない更新を失敗させ、強制pushや別コミットへの自動再試行はしない。

### GitHub Appの初回設定

通常の`GITHUB_TOKEN`で追加したコミットではPR workflowの実行に承認が必要になるため、無人で再検査するには専用GitHub Appを使う。
GitHubの仕様は[workflowの起動](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow)を参照。

1. [個人設定のGitHub Apps](https://github.com/settings/apps)で`New GitHub App`を選ぶ。既存の専用Appがあれば再利用する。
   - App名はGitHub全体で未使用の名前にする。Homepage URLはこのリポジトリのURLでよい。
   - ユーザーとしてのOAuth認証は使わないため、Callback URLは不要。Webhookの`Active`を外す。
   - Repository permissionsを`Contents: Read and write`、`Pull requests: Read-only`にする。`Metadata: Read-only`は自動で付く。
   - インストール先は自分のアカウントだけに限定して作成する。
2. Appの設定画面で`Client ID`を控える。このworkflowが使うのは数字のApp IDではなくClient IDである。
   `Private keys`の`Generate a private key`からPEMファイルを取得する。Client secretの発行は不要。
3. Appの`Install App`から自分のアカウントへインストールし、`Only select repositories`で`Omni_Money`だけを選ぶ。
   main保護のbypass権限は付与しない。すでにインストール済みなら、対象リポジトリと権限を確認する。
4. [リポジトリのActions設定値](https://github.com/ShiningWank0/Omni_Money/settings/secrets/actions)で、次の2つを登録する。

   | タブ・操作 | 名前 | 値 |
   | --- | --- | --- |
   | Variables → New repository variable | `NODE_SYNC_APP_CLIENT_ID` | AppのClient ID |
   | Secrets → New repository secret | `NODE_SYNC_APP_PRIVATE_KEY` | PEMファイルの内容全体。BEGIN/END行と改行を含む |

   ファイル名やパス、Client secret、base64へ変換した文字列は秘密鍵の代わりにならない。秘密鍵をチャット・リポジトリ・PR本文へ記載しない。
   通常のActions設定値を使い、Dependabot secretsやEnvironment secretsへ登録するものではない。
5. このPRをmainへマージして同期workflowを有効にする。App設定だけでも、PRのマージだけでも自動同期は完成しない。
   リポジトリ全体の`GITHUB_TOKEN`を書き込み可能へ変更したり、ActionsによるPR作成・承認を許可したりする必要はない。
6. 次の対象Node Docker更新PRで、Actionsの`Sync Dependabot Node version`と、同じPRへの`.node-version`同期コミットを確認する。
   その最新コミットで通常CI・Docker・desktopチェックが起動し、固定版の整合性検査が通ることを確認する。

既存の対象PRで試す場合、通常CIの`Re-run jobs`だけでは`workflow_run: requested`は発生しない。
Dependabot側で新しいコミットを生成する更新・rebaseなどが必要になる。
すでに`Sync Dependabot Node version`の実行履歴がある場合は、記録されたPRのheadがまだ最新であれば、その同期workflow自体の再実行も利用できる。
対象PRがないときは次の更新を待つ。動作確認のために古いNodeへ戻す必要はない。

同期されないときは、同期workflowのSummaryを確認する。App未設定の警告なら上記の2設定値を、
トークン発行時の失敗ならAppのインストール先・権限・秘密鍵を確認する。
メジャー更新や追加コードを含むPRは対象外であり、設定を緩めず手動レビューする。

App設定前は警告とジョブのsummaryに設定手順を表示し、同期コミットは追加しない。既存CIの不一致検査は維持する。
トークンは対象リポジトリ・上記2権限だけに絞って発行し、ジョブ終了時にActionが失効させる。
App自体のContents権限はリポジトリ単位であり、ファイル単位の制限ではない。`.node-version`だけの変更は検証処理で制限する。
Appを無効化する場合はインストールまたは秘密鍵を削除し、手動同期に戻す。

自動同期のテストは`node --test scripts/dependabot-node-sync.test.mjs`で実行し、通常CIの`Security issue reporting tests`にも含める。
このテストはGitHub APIを模擬し、対象条件、変更範囲、major更新の拒否、同期済みの無変更、競合時の強制更新拒否を確認する。

### Actionsから手動でApp設定を確認する

対象のDependabot PRがなくても、`Check Node sync App configuration`で登録済みの設定を確認できる。
このworkflowの起動イベントは`workflow_dispatch`だけであり、push・PR・スケジュールでは起動しない。
main以外のブランチを選ぶとジョブをスキップする。既存の自動同期workflowは引き続き対象Dependabot PRだけを処理する。

1. workflowを含むPRをmainへマージする。
2. リポジトリのActionsで`Check Node sync App configuration`を選ぶ。
3. `Run workflow`を開き、Branchに`main`を選んで実行する。追加入力は不要。
4. 実行結果のSummaryを確認する。失敗時は失敗したstepも確認する。

チェックはClient IDとRSA秘密鍵の読み込み、自動同期と同じ`Contents: write`・`Pull requests: read`を指定したAppトークンの発行、
発行トークンがOmni_Moneyだけに限定されていること、固定コミットの`.node-version`とPR一覧の読み取りを確認する。
秘密鍵・トークンの内容はログやartifactへ出力せず、ジョブ終了時にはトークンを失効させる。

トークン発行の成功は指定権限の取得を確認するが、実際のコミット作成やブランチ保護ルールへの適合を検証するものではない。
同期コミットの追加とCIの再起動は、次の対象Dependabot PRで確認する。手動テストはコミット・PRを変更しない。

秘密鍵をクリップボードから検証ファイルへ戻す場合、復元コマンドを先にターミナルへ貼り付け、まだ実行しない状態にする。
その後で保管先から鍵をコピーし、ターミナルへ戻って実行する。鍵のコピー後にコマンドをコピーすると、クリップボードがコマンドへ置き換わる。

## Dependabotの検査ツールとグラフ描画ライブラリの更新

`gosec`、`govulncheck`、`actionlint` はそれぞれ専用のGoモジュールでCLIを `tool` 宣言に登録する。
Dependabotは同じ専用モジュール内のminor/patch更新をまとめて提案する。
通常CIは `go mod tidy -diff` と固定CLIの解決を確認し、セキュリティ検査では前者2つを、ワークフロー検査では `actionlint` を実行する。

`vue-chartjs` は単独の更新PRで扱い、通常の本番ビルドで配布物のimportを検証する。
他のフロントエンド依存の更新は、従来どおりDependabotのグループで扱う。

## マージ前の確認

### ネイティブOpenSSLの固定と更新

macOS/Windowsの静的OpenSSLは、それぞれ`build-openssl-darwin.sh`と
`build-openssl-windows.sh`の版・SHA-256を固定して公式release sourceからビルドする。
WindowsはMSYS2の最新OpenSSL packageを配布物へ取り込まない。
固定は再現性と取得物の整合性のためであり、既知の脆弱性への対応には固定値の更新が必要になる。

固定処理の変更とセキュリティ版への更新は別PRにする。更新PRでは次を同時に確認する。

- [公式配布物](https://openssl-library.org/source/)と[セキュリティ情報](https://openssl-library.org/news/vulnerabilities/)から、サポート中の系列の修正版と公式SHA-256を確認する。
- 両OSのbuild script、workflowのprefix/版情報、`licenses/native/components.json`、確認済みOSS版と生成一覧を更新する。ライセンス原文・NOTICEも取得元と照合する。
- Windows SQLCipher回帰テスト、macOS Intel/ARM64・Windowsの本体ビルド、暗号化self-test、配布物のリンクとOSS記録を通常PR CIで確認する。

Dependabotはshell script内のOpenSSL固定値を更新しない。固定後も公式のadvisoryと
サポート期限を確認し、修正版が公開されたら版とSHA-256を更新する。
静的リンク済みの配布物へ修正を適用するには、新しい固定版での再ビルドと配布が必要になる。

3.5系は2030年4月8日までのLTSである。4.xなど別系列への移行は、固定・patch更新とは別PRで扱い、
SQLCipher provider、API/ABI、各OSのリンク、暗号化DBの互換性、OSS記録を確認する。
サポート中のLTSを使用することと、修正版を適用せず古いpatchへ留めることは区別する。
[公式サポート方針](https://openssl-library.org/policies/releasestrat/)を参照する。

### DependabotのOSS情報の自動同期

DependabotはGo/npmの依存指定とlockを更新するが、Omni Money独自のOSS記録は生成しない。
例えばgo-sqlite3の更新だけでは`licenses/approved.json`の確認済み版が旧版に残り、
Vue/Viteの更新では本番バンドル一覧も変わる。jsdomのような配布しないテスト依存も依存用途一覧には記録する。

`Sync Dependabot OSS inventories`は、Go/npm更新PRのCI完了後にこの記録を同じPRへ同期する。
対象はOmni_Money内のDependabot作成・main向け・openなPRで、変更が依存ファイルとOSS生成物だけの場合に限る。
Goの最低版・toolchain・置換、npm scripts・engines・overrides、直接依存の追加削除、外部レジストリを伴う変更は手動レビューする。

自動同期では既に確認済みのライセンス原文・著作権・NOTICE・ライセンス種別・構成別の配布対象がすべて同一のときだけ、版と生成一覧を更新する。
配布する構成要素の追加削除、原文や対象構成の変更、ネイティブライブラリの版変更は自動承認せず、Actions Summaryに中止理由を表示する。
OS・CPU・Desktop/Server別の`targets`は維持し、構成別表示・配布物の判定に同じ記録を使う。テスト専用依存を表示対象へ追加しない。

変更するファイルは次の4件に限定する。更新が不要なファイルには変更を加えない。

- `frontend/bundled-dependencies.json`: 本番JavaScriptへ残るnpmパッケージ。
- `licenses/approved.json`: 原文と対象構成が変わらない既存OSSの版。
- `frontend/src/generated/thirdPartyNotices.json`: 表示と構成別配布の元データ。
- `licenses/dependency-usage.json`: 配布する依存・配布しない開発依存の用途一覧。

生成jobは固定mainのアプリ・スクリプトへ検証済みの依存指定だけを重ね、read-only権限で`npm ci --ignore-scripts`、本番ビルド、Go一覧取得を行う。
PR側のスクリプトや生成済みの承認を実行・採用しない。依存コードが動く生成jobにはApp秘密鍵を渡さず、共有キャッシュも使用しない。
別のwriter jobが同じworkflow runのJSON候補をデータとして読み、固定mainの承認原文・対象構成・PRのlockと最新headをAPIで再照合する。
検証したheadを親に4ファイルだけのコミットを作り、競合・更新があれば強制pushや別headへの再試行を行わない。

書き込みには既存Node同期Appの`NODE_SYNC_APP_CLIENT_ID`と`NODE_SYNC_APP_PRIVATE_KEY`を再利用する。App・権限・秘密鍵の追加設定は不要。
未設定ならSummaryに案内し、必須OSSチェックは引き続き失敗する。通常CIやマージ条件を緩和しない。
Appによる同期コミットから通常PR CIが新しく起動するため、取り込みにはその最新コミットの全必須チェック成功を使う。
同期済みならコミットを追加しない。自動マージ・PRコメント・Issue操作は行わない。

workflowをmainへマージした後に有効になる。既存の対象PRにも、CIを再実行して完了させれば`workflow_run: completed`から適用できる。
このworkflowはNode同期の`requested`と異なり、CI再実行後の完了も対象にする。古いheadのrun、キャンセルされたrunは処理しない。
Summaryの「手動レビュー」が出た場合は`docs/oss-licenses.md`の手順で該当変更を確認する。

`node --test scripts/dependabot-oss-sync.test.mjs`はGo・npm・テスト専用依存の同期、原文・構成変更の拒否、
fork・スクリプト変更・候補改ざん・競合・同期済みの無変更を検証する。通常CIでも必須実行する。

### パスキー関連変更の恒常的な回帰テスト

WebAuthn、CBOR、暗号ライブラリ、フロントエンドを更新するPRでも、通常のコード変更PRでも、
`Passkey regression tests`と既存の`Docker image build`でパスキーの契約を検証する。
テストはそのPRの`go.mod`・`go.sum`・frontend lockfileをそのまま使う。
CIによる依存版の上書きや、候補版・比較対象版の指定は行わない。依存更新のたびにworkflowを編集する必要はない。

- 固定資格情報によるP-256・RSA・Ed25519の登録・ログイン・再認証。固定データは更新対象のencoderで生成し直さない。
- PRF/server-custodyの両形式と、再認証の完了・署名カウンター・revision・ログイン時刻・再送拒否。
- SQLCipherの実DBに保存し、close/reopenした後の認証。CIはSQLCipher未導入によるスキップを許さない。
- CBORの重複キー・禁止タグ・不定長・切り詰め・余分な末尾データ・深さ超過と、不正な署名・challenge・origin・ユーザー確認の拒否。
- アプリケーションと同じ依存グラフを使った、固定中のWebAuthnモジュール自身の`protocol/...`・`webauthn`テスト。
- 既存のDocker E2Eで、実UI・Chromium仮想CTAP2認証器・実API・SQLCipher serverを通したPRFなし登録、discoverable login、再認証、CSRF更新、再送拒否。認証応答をmockしない。

必須チェック`Go tests`は、パスキーのGoテスト、frontendのunit/componentテスト、ブラウザE2Eを実行するDockerジョブに依存する。
いずれかの失敗・キャンセル・予期しないスキップを成功扱いにしない。
PRの最新コミットでこれらを含むCIが成功していることを、パスキー関連変更の取り込み判断に使用する。
固定データの形式と来歴は`backend/serverauth/testdata/README.md`を参照する。

各プロバイダー・物理デバイス・OS固有の画面は仮想認証器の検証対象に含まれない。
サポートするパスキーの契約を変更する場合は、その契約と回帰テストを同じPRで更新する。

### その他の依存更新

通常のCIに加え、Wails更新は4構成のdesktop buildとSQLCipher検証、WebAuthn更新はPRFなしの登録・discoverable login・再認証、server-custody envelope、旧形式移行、ユーザー列挙防止のテストを確認する。PRFを標準パスキーの必須条件へ戻さない。
フロントエンドのmajor更新はdesktopモードの画面・ストア操作とserver E2Eを確認する。

Docker release workflowはPR上で、本番と同じBuildx/build-push Actionによるamd64/arm64ビルド、SBOM/provenance生成を行う。レジストリへのpush、ログイン、署名公開は行わない。
本番のReleaseは「タグなしdigest push → スキャン → staging index → attestation → 検証 → release tag公開」の順で行い、検証前にversion/latestを公開しない。
Release経路の変更は、まず`workflow_dispatch`のリハーサル（release tagを公開しない同一チェーン）で検証してからVERSIONを更新する。
この検証は公開後のattestation検証の代替にはならない。attestation Action更新は、証明生成・検証まで別途確認する。

Dependabot PRに移行コードを追加した後は自動rebaseが停止するため、最新mainの取り込みは明示的に行う。
グループPRを分割するときは代替PRを用意し、更新を取りこぼさないよう元PRから追跡できる状態にして閉じる。
