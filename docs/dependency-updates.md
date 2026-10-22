# 依存更新のレビュー

Dependabotは週次で確認し、各エコシステムの通常更新PR上限は5件とする。
Go・npm・GitHub Actions・Dockerのminor/patch更新をそれぞれまとめ、major更新は個別PRにする。
脆弱性修正はエコシステム毎に別グループとしてminor/patchをまとめ、majorは個別PRにする。
更新対象を恒久的にignoreしない。

定期検査が作成するIssueも同じ方針で、同一パッケージの複数advisoryは1件へ集約する。

Wails、WebAuthn、DockerのGo/Nodeは通常グループから除外し、個別PRで扱う。
Goの1.x間、WebAuthnの0.x間はSemVerではminorでも、関連設定やAPI移行が必要になるためである。

## ビルド版の固定元

- Goの最低対応版は `go.mod` の `go`、CI/配布用固定版は `toolchain`。各workflowは `go-version-file: go.mod` を使う。
- NodeのCI/配布用固定版は `.node-version`。`frontend/package.json` の `engines` は対応範囲であり、固定版とは別にレビューする。
- Wails CLIは `go.mod` のWailsモジュールと同じ固定版を導入する。`latest` は使わない。
- DockerのGo/Nodeイメージは同じ版の `tag@sha256` を維持する。Alpineの系列変更は別途サポート範囲とSQLCipherの動作をレビューする。

DockerのGo/Node更新PRでは、新しいタグとdigestを確認してから次を実行し、関連ファイルを同じPRに含める。

```sh
node scripts/runtime-versions.mjs sync
node scripts/runtime-versions.mjs check
```

`sync` はDockerfileを基に `go.mod` のtoolchainと `.node-version` だけを同期する。
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
例えば`GO-2026-6809`はGo 1.26.10 / 1.27.3で修正されており、最低対応版を1.26.10、配布用固定版を1.27.3に設定する。
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

## マージ前の確認

通常のCIに加え、Wails更新は4構成のdesktop buildとSQLCipher検証、WebAuthn更新はPRFなしの登録・discoverable login・再認証、server-custody envelope、旧形式移行、ユーザー列挙防止のテストを確認する。PRFを標準パスキーの必須条件へ戻さない。
フロントエンドのmajor更新はdesktopモードの画面・ストア操作とserver E2Eを確認する。

Docker release workflowはPR上で、本番と同じBuildx/build-push Actionによるamd64/arm64ビルド、SBOM/provenance生成を行う。レジストリへのpush、ログイン、署名公開は行わない。
本番のReleaseは「タグなしdigest push → スキャン → staging index → attestation → 検証 → release tag公開」の順で行い、検証前にversion/latestを公開しない。
Release経路の変更は、まず`workflow_dispatch`のリハーサル（release tagを公開しない同一チェーン）で検証してからVERSIONを更新する。
この検証は公開後のattestation検証の代替にはならない。attestation Action更新は、証明生成・検証まで別途確認する。

Dependabot PRに移行コードを追加した後は自動rebaseが停止するため、最新mainの取り込みは明示的に行う。
グループPRを分割するときは代替PRを用意し、更新を取りこぼさないよう元PRから追跡できる状態にして閉じる。

