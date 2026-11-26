# CI・配布物の版と固定入力

確認日: 2026-11-17。main 2.5.5を基準に作成した固定・更新PRの一覧である。下表は採用済みという意味ではなく、各PRの適用先と変更内容を記録する。以後の正本は各lockとmanifestとする。

## 更新を優先したもの

| 優先 | 対象 | 更新内容 | PR・送り先 |
| --- | --- | --- | --- |
| 高 | SQLCipher | 4.18.0 → 4.20.0。実行時要求版・全OSのsource hash・OSS表示も更新 | [#335](https://github.com/ShiningWank0/Omni_Money/pull/335)、feat/update-source-version |
| 高 | Trivy | 0.70.0の取得物固定と、0.76.2への更新を分離 | [固定 #331](https://github.com/ShiningWank0/Omni_Money/pull/331)はmain、[更新 #336](https://github.com/ShiningWank0/Omni_Money/pull/336)はfeat/update-source-version |
| 高 | gosec | 2.29.0 → 2.30.0。専用module/sumとx/tools 0.52.0 | [#337](https://github.com/ShiningWank0/Omni_Money/pull/337)、feat/update-source-version |
| 高 | govulncheck | 1.6.0 → 1.9.0。CIとreleaseの呼出しを同じreadonly moduleへ揃える | [#338](https://github.com/ShiningWank0/Omni_Money/pull/338)、feat/update-source-version |
| 中 | Wails | 2.17.1 → 3.1.0。CLI、runtime、binding、native buildと検証を移行 | [#340](https://github.com/ShiningWank0/Omni_Money/pull/340)、feat/update-source-version |

版が古いという理由だけで、未修正の脆弱性があると断定しない。到達可能な脆弱性、上流修正、OS vendorの修正revision、アプリの互換性を併せて判断する。

## PR外で変動していた入力

| 対象 | 固定する内容 | 通常buildでの扱い | PR |
| --- | --- | --- | --- |
| Docker Engine / CLI / containerd / runc | 29.10.0の公式bundleとarch別SHA256 | 検証済みdaemonを専用contextで使用 | [#328](https://github.com/ShiningWank0/Omni_Money/pull/328) |
| Buildx / Compose | 0.39.0 / 5.7.2、公式binaryのarch別SHA256 | runner同梱版を選ばない | #328 |
| BuildKit / SBOM生成image | 0.35.0のindex digest / Syft image digest | 可変tagだけで選択しない | #328 |
| UbuntuのC・GTK・WebKitGTK・Python・Perl・Tcl・Playwright OS依存 | amd64/arm64の全体image、package一覧、取得deb・hash・repository metadata | aptを実行せず保存digestを使用 | [生成 #329](https://github.com/ShiningWank0/Omni_Money/pull/329)、[使用 #334](https://github.com/ShiningWank0/Omni_Money/pull/334) |
| Docker内のAlpine packageと推移的依存 | builder/runtimeそれぞれの全体image、apk・hash・metadata | apkによる解決・upgradeを通常buildから除く | #329、#334 |
| WindowsのMSYS2全体 | UCRT64 root archiveのOCI digestとSHA256、全package cache・一覧 | pacmanによるupdate/installを行わず全体復元 | #329、[使用 #332](https://github.com/ShiningWank0/Omni_Money/pull/332) |
| Xcode / SDK / macOS Tcl | 26.3 build 17C529 / SDK 26.2 / Tcl 9.1.0 source SHA256 | 別XcodeへのfallbackとHomebrew解決を行わない | [#330](https://github.com/ShiningWank0/Omni_Money/pull/330) |
| Windows CIのWebView2 | Fixed Version 157.0.4341.60 x64、CAB hash・OCI digest・Microsoft署名 | Wailsの明示optionで選び、実processの版とpathも照合 | [#333](https://github.com/ShiningWank0/Omni_Money/pull/333) |
| ORAS / 既存release操作のGitHub CLI | 1.4.0 / 2.105.0、公式binary SHA256 | runner同梱版やlatestを選ばない | #332、#334 |

これらの固定PRはmain向けである。OS環境の生成はCI内で行い、取得後はGHCRに保存する。期限付きActions artifactやcacheだけを保存先にしない。通常buildは固定済みの実体を使う。

## 保存環境で確認した実版

[生成run 44080314331](https://github.com/ShiningWank0/Omni_Money/actions/runs/44080314331)のamd64取得ログから確認した例である。環境全体の正確な内容は各archの保存image内の全件一覧で確認する。

| 環境 | package | 取得した版 |
| --- | --- | --- |
| Ubuntu 24.04 | OpenSSL / libssl-dev | 3.0.13-0ubuntu3.16 |
| Ubuntu 24.04 | WebKitGTK 4.1 | 2.52.6-0ubuntu0.24.04.1 |
| Ubuntu 24.04 | GTK3 | 3.24.41-4ubuntu1.3 |
| Ubuntu 24.04 | GCC 13 | 13.3.0-6ubuntu2~24.04.1 |
| Ubuntu 24.04 | Python 3.12 | 3.12.3-1ubuntu0.17 |
| Ubuntu 24.04 | Perl | 5.38.2-3.2ubuntu0.7 |
| Ubuntu 24.04 | Tcl 8.6 | 8.6.14+dfsg-1build1 |
| Alpine builder | OpenSSL開発package | 3.5.9-r0 |
| Alpine runtime | libcrypto3 | 3.5.9-r0 |
| Alpine builder | GCC / musl-dev | 15.2.0-r5 / 1.2.6-r3 |

UbuntuのOpenSSLはWindows/macOSのupstream 3.5.9とは別のvendor buildである。3.0系のupstream supportは終了しているが、Ubuntu packageの安全性はUbuntuの修正revisionと対象CVEで判断する。版番号の先頭だけで判定せず、[Ubuntuのsecurity policy](https://wiki.ubuntu.com/SecurityTeam/FAQ)とUSNも確認する。vendor packageからsource buildへ切り替える場合は、別の更新PRでlink・ABI・配布依存・暗号化・OSSを再検証する。

## 継続して注意するもの

| 対象 | 現状と更新方法 |
| --- | --- |
| Go / Node | 1.27.4 / 26.13.1。今回の対象では既に選択されている版を揃える。Goは最低要件と配布版を区別し、Docker・toolchain・.go-versionを検査する |
| npm依存・Playwright browser | npm ciでpackage-lock.jsonを使用する。package.jsonの範囲指定だけでは判定しない。browser revisionもlockに対応する版を使用する |
| Go依存 | go.mod/sum、scanner・Wails CLIの専用moduleを確認する。通常buildでtidyやlatest解決をしない |
| Actions | 外部Actionはfull SHA、repository-local Actionはcheckoutのcommit。Composite内の外部Actionも検査する |
| Trivy脆弱性DB | scanner binaryと区別し、検査時の新しいDBを使用してmetadata/hashを記録する。古いDBへ固定しない |
| 保存したOS環境 | 固定後もsecurity更新が必要。生成workflowで次の全体候補を作り、package差分・scanner・native/暗号化/E2E・OSSを確認してdigestを更新する |
| OpenSSL 3.5 | [2030-04-08までのLTS](https://openssl-library.org/policies/releasestrat/)。patch更新を継続し、4系移行は今回とは別PRで検証する |
| Ubuntu / Alpine | [Ubuntu 24.04の標準supportは2029-05まで](https://ubuntu.com/about/release-cycle)、[Alpine 3.24は2028-06-01まで](https://www.alpinelinux.org/releases/)。個々のpackageのsupport範囲も確認する |
| Xcode / Windows image | 固定したSDKやbinaryが利用可能かを確認する。指定物がなくなった場合は停止し、更新PRで選び直す |

GitHub-hosted runnerのOS/kernel全体、Windowsのsystem DLL、macOSのWKWebViewは、この構成では完全な固定対象ではない。開発端末の版を配布入力にしない一方、CI platform側の範囲は残る。同じ依存取得物を使うことと、配布binaryの全byteが一致することも区別する。

固定PRはmainへ、更新PRはfeat/update-source-versionへ統合し、その後main向けにまとめる。既存のレート制限に達していた回帰テストの修正は、独立したmain向け[#339](https://github.com/ShiningWank0/Omni_Money/pull/339)で扱う。VERSION・Release PRとは分離する。CI/CDの完了待ちや継続監視は、この作業に含めない。
