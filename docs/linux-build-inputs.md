# Linuxのビルド・試験とDocker内のOSパッケージ

`build/locks/linux-environments.json`は保存済みのUbuntu、Alpine builder、Alpine runtimeのOCI index digestを管理する。すべてamd64/arm64のnative環境を含む。通常buildのworkflow/Dockerfileにある参照がlockと一致することを機械的に確認し、通常経路に`apt-get install/update`、`apk add/upgrade/update`やPlaywrightの`--with-deps`を戻した場合も拒否する。

## Ubuntu

LinuxのCI、配布build・quality gate、Wailsの開発診断は同じ保存imageで実行する。GTK/WebKitGTK、Xvfb、Python、C compiler、ブラウザのOS依存を含む推移的依存を固定し、開始時にdpkg全件一覧との一致を確認する。Go/Nodeは従来のexact version定義とlockを使う。Playwrightのbrowser revisionはnpm lockに対応するものを取得し、OSパッケージは追加解決しない。

jobはuid 1001の専用一般ユーザーで実行する。Docker daemonを扱うjobとnative WebKit sandboxを起動するjobだけ、隔離したGitHub-hosted VM内のjob containerに必要な権限を与える。WebKit試験をroot起動へ変えたり、WebKit sandboxを無効化したりしない。GitHubがjob containerを開始するhost側のDocker/kernelはrunner基盤として扱い、アプリをbuildするDocker Engine/Buildx/BuildKitとは分ける。

## Alpine

Dockerfileのbuilder/runtimeは保存imageを直接使用し、通常buildではpackageを更新しない。builder/runtimeのAlpine系列とlibcrypto3 package版の一致を確認する。SQLCipherのsource/checksum、既存の実link probeとruntime/暗号化E2Eを維持する。OpenSSLは保存したvendor packageのままであり、vendorのbackportを失うsource版への切替はこの固定と混在させない。

Goの公式digest固定stageはtoolchain取得用として残し、保存OS内のGo directoryを入れ替える。これによりGo更新とC/OSパッケージ環境の更新を別PRで行える。Nodeのfrontend stageも既存の公式digest固定imageを使用する。`GOTOOLCHAIN=local`で未指定のtoolchain取得を避ける。

配布imageのapk一覧とaports origin/commitを保存する既存のOSS処理は継続する。`.deb`/`.apk`の取得済みpayloadと署名付きrepository metadataは、先行の環境保存処理がGHCRへ保持する。GHCR読取にはjobの`packages: read`だけを使用し、保存jobのwrite権限と分ける。

## 配布操作のCLI

既存の配布jobで使うGitHub CLIもrunner同梱版に依存させず、`build/locks/gh.json`の公式binary＋SHA256を照合して導入する。新たなPR作成やmergeの自動処理は追加しない。

## 更新と検証

環境更新時だけ保存workflowを実行し、実digest・生成commit/run IDとpackage一覧差分を更新PRでレビューする。通常buildのcacheを消しても同じ入力を取得でき、削除やhash不一致時は別版へfallbackしない。

CIではamd64/arm64のDocker build、hardened runtime、SQLCipher fail-closed/passkey、Linux native WebView、server/browser E2E、OSS一覧を検証する。ホストOS/kernel、macOS framework、Windows OS全体は固定対象外である。
