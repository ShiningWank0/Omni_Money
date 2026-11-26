# macOSの固定ビルド入力

`build/locks/macos.json`にXcode version/build、macOS SDK version、Tcl source URL/SHA256を記録する。`setup-fixed-macos.sh`はGitHub ActionsのmacOSでのみ実行し、別Xcode・SDKへのfallbackやHomebrewによる取得を行わない。

現行runnerの両CPUで利用できるXcode 26.6（17F113）とSDK 26.5を固定する。Intel/Apple Siliconはそれぞれnative runnerで同じXcodeを使う。macOS framework、WKWebView、kernelを含むホストOS全体はGitHub-hosted runnerに依存し、この固定の範囲外である。

SQLCipherのコード生成に必要なのはTcl interpreterであるため、Tcl 9.1.0のcoreだけを、同梱libtommathを使って構築する。TkやHomebrewの追加extensionは導入しない。Tclはbuild時だけ使用し、配布appへのruntime依存は追加しない。OpenSSL、SQLCipher、GoのCGO、Wailsにも同じ`DEVELOPER_DIR`とSDKを使用する。

GitHub の macOS 26 ランナー一覧で、両 CPU の Xcode のビルド番号と SDK を照合する。Tcl の公式 source SHA256 は Homebrew の現行 formula とも照合する。Xcode や SDK の提供が終了したら更新 PR で次の組合せを選ぶ。[Intel ランナー](https://github.com/actions/runner-images/blob/main/images/macos/macos-26-Readme.md)・[Apple Silicon ランナー](https://github.com/actions/runner-images/blob/main/images/macos/macos-26-arm64-Readme.md)・[Tcl の公式配布](https://www.tcl-lang.org/software/tcltk/download.html)

CIでは取得物のhash、Tcl実行時のpatchlevel、外部Homebrew動的linkの不在を確認し、既存のIntel/Apple Silicon配布buildとnative SQLCipher probeで適合性を確認する。fixture testは異なるXcode build・SDKと改変archiveの拒否を検証する。
