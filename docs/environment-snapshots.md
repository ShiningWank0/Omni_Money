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

環境採取時に libXi、libXtst、SQLite、HarfBuzz、D-Bus、util-linux、ICU、Cyrus SASL、libxslt、GStreamer base、Python 3.12 を各 CPU の Noble ツールチェーンでビルドする。前二者は Ubuntu アーカイブの修正済みソースリリース、残りは Noble のソースに上流の修正を適用する。入力の版とソース記述子の SHA-256 は `build/environments/noble-backports/sources.tsv`、パッチの出典は各ファイルの `Origin` に記録する。ビルド用ステージだけがソースを展開し、最終イメージには既存環境で使うバイナリパッケージだけを導入する。

採取では対象ソース由来の全導入パッケージの版、CPU、依存の整合、共有ライブラリのロード、SQLite のインメモリ全文検索、D-Bus セッションへの問い合わせ、マウント情報の取得を確認する。ソース記述子とパッチはイメージ内の `/opt/omni-environment/backport-sources/` に保存する。Noble の配布版が変わると採取が停止する。公式修正を確認したら対応する行と不要になったパッチを削除し、両 CPU の候補を取り直して固定先を更新する。

libxslt の CVE-2025-7425 に対する Ubuntu の緩和策は libxml2 の更新で提供される。Python の XML パーサーには Noble の libexpat に移植済みの 16 バイトのソルト API を使用する。採取時に両ライブラリの最低版、pyexpat の参照シンボル、通常の XML の読み取りも検証する。

TIFF には Noble の公式セキュリティ更新 4.5.1+git230720-4ubuntu2.6 以降を使う。dash は libc の fnmatch を使用し、S-Lang は端末の書式で未定義となる除算を検出するとその出力を破棄する。Duktape はコンパイル済みバイトコードの入出力を無効にする。libproxy が PAC に使用する JavaScript ソースの評価、通常のシェルパターン、端末書式、TIFF の共有ライブラリは採取時に確認する。

Noble の binutils 2.42 では DWARF リーダーに上流の4件の修正を適用する。採取時に通常の C ソースを DWARF 4/5 それぞれの形式でコンパイルし、readelf と objdump が型情報を読めることを検証する。CI で使わない Gprofng はビルド時に無効化する。採取時にプロファイラー本体と共有ライブラリの不在、C プログラムのリンクと実行、従来の gprof の起動を確認する。

Git にはリモート側の sideband 出力を表示用に変換する上流修正を適用し、通常の push でサーバーフックの進捗を確認する。GLib ではローカルファイル置換のフォールバックに排他的な新規作成を要求し、通常の作成・上書きを GIO で検証する。

GdkPixbuf に ICO のパレット境界、ICNS の入力境界、JPEG の ICC バッファ解放処理の修正を適用する。libsndfile では MPEG、IMA ADPCM、IRCAM、MAT4 の処理を更新する。採取時に ICO・JPEG の保存と読み込み、および通常の PCM WAV のフレーム読み込みを確認する。

Cairo は FreeType エラーを呼び出し元へ返し、借用したフォントの所有権を保ち、画像マスクへの行・区間書き込みを寸法に照合する。採取時に FreeType のフォントと Cairo を使用して文字と矩形を描画し、画素値と破棄後のフォントの有効性を確認する。
