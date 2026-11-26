# ビルド環境の保存と通常ビルドへの適用

`capture-build-environments.yml` は環境更新時だけ使う生成処理である。Linux amd64/arm64のUbuntu、Alpine builder、Alpine runtimeと、WindowsのMSYS2 UCRT64全体を保存する。通常アプリbuildに毎回パッケージ解決をさせる目的ではない。

## 保存する内容

| 環境 | 実体 | 再確認用の情報 |
| --- | --- | --- |
| Ubuntu | GTK/WebKitGTK、Xvfb、Python、C toolchain、ブラウザのOS依存を含むimage | dpkg全件一覧、取得したdeb、debのSHA256、aptの署名付きindex |
| Alpine builder | 現行Go baseとC/Tcl/OpenSSL開発環境 | apk全件DB、取得したapk、SHA256、repository情報 |
| Alpine runtime | 現行Alpine baseとCA証明書/tzdata/libcrypto3 | 同上 |
| Windows | 全体upgrade後のMSYS2 root、UCRT64 toolchain | pacman全件一覧、全導入packageのcacheとSHA256、repository metadata |

baseイメージに元から含まれるファイルも、固定baseのOCI layerと保存imageの双方に残る。新規に取得した依存だけでなく、最終的に導入された推移的依存も一覧と保存image/archiveに含まれる。

生成jobはregistry書込権限を持たない。別jobが成果物をデータとしてGHCRへ保存する。書込jobでは生成した環境内のプログラムを実行しない。Windowsの全root archiveもOCI artifactとして保存する。Actions artifactはjob間の転送用であり、永久保存先はGHCRである。

## 通常ビルドへ適用する条件

publisherが出力する`candidate-lock-*`には実際のimage/artifact digest、archive SHA256、生成source commit/run IDが入る。これをレビューし、消費側のlockと復元処理を追加してから通常buildを切り替える。可変tagのままでの適用、未生成digestの仮置き、旧環境へ戻す自動fallbackは行わない。

切替PRでは、Alpine builder/runtimeのlibcrypto ABI・package版整合、SQLCipher実probe、OSS package一覧、WindowsのPE/native probeとDLL同梱制限、Linux native UIとserver/browser E2EをCIで確認する。macOS Tcl/XcodeとWindows Fixed Version WebView2はそれぞれ別の取得物として扱う。

更新時はこの生成処理を再実行し、候補digestと差分を別PRでレビューする。GHCRのbuild用packageを自動削除するretention設定は設けない。通常buildのcacheを消しても同じdigestから復元できるようにする。

## Ubuntu 24.04 のセキュリティ修正

環境採取時に libXi、libXtst、SQLite、HarfBuzz、D-Bus、util-linux、Git を各 CPU の Noble ツールチェーンでビルドする。前二者は Ubuntu アーカイブの修正済みソースリリース、残りは Noble のソースに上流の修正を適用する。入力の版とソース記述子の SHA-256 は `build/environments/noble-backports/sources.tsv`、パッチの出典は各ファイルの `Origin` に記録する。ビルド用ステージだけがソースを展開し、最終イメージには既存環境で使うバイナリパッケージだけを導入する。

採取では対象ソース由来の全導入パッケージの版、CPU、依存の整合、共有ライブラリのロード、SQLite のインメモリ全文検索、D-Bus セッションへの問い合わせ、マウント情報の取得を確認する。ソース記述子とパッチはイメージ内の `/opt/omni-environment/backport-sources/` に保存する。Noble の配布版が変わると採取が停止する。公式修正を確認したら対応する行と不要になったパッチを削除し、両 CPU の候補を取り直して固定先を更新する。

Git の側帯メッセージは通常のローカル push で表示を確認する。binutils は、DWARF の4件を修正した Noble 公式版 `2.42-4ubuntu2.11` 以降を、同じソース由来の全導入パッケージに要求する。通常の C ソースから生成した DWARF 4 と 5 の型情報を両 CPU で readelf と objdump で確認する。

## Docker ビルドで使用する BuildKit

`capture-build-environments.yml` の `buildkit_only` を有効にすると、amd64・arm64 のネイティブ環境でデーモンと4つの CNI プラグインを再構築する。上流イメージ、ソースのコミットとチェックサム、Go の版、依存の修正は `build/environments/buildkit.Dockerfile` に固定する。各 CPU でバージョン応答と依存情報を確認し、別ジョブでマルチアーキテクチャのインデックスを保存する。Docker の固定ビルダーと脆弱性検査は `build/locks/docker.json` の同じダイジェストを使用する。採取ログは同ファイルの `buildkitCapture.runId` から確認できる。
