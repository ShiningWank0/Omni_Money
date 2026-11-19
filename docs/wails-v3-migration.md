# Wails 3の固定入力とビルド

Wailsのアプリ本体とCLIは3.1.0へ揃える。CLIは`go install ...@latest`を使わず、`scripts/buildtools/wails/go.mod`と`go.sum`から`-mod=readonly`で構築する。Go 1.27のexport dataに対応するx/tools 0.52.0を選び、実CLIのGo版、Wails版、x/tools、x/netとmodule replacementの不在を照合する。

| 対象 | 管理方法 |
| --- | --- |
| Go | `.go-version`、rootのtoolchain、Docker取得stageを一致させる |
| Wails | rootとCLI専用moduleを同じstable版へ揃える |
| frontend runtime | 同じGo moduleが配信する`/wails/runtime.js`。別のnpm runtime版を取得しない |
| binding | `wails3 generate bindings -b -names`の実出力をcommitし、通常buildでは一時出力との全件・内容一致を確認する |
| Linux | 保存したGTK3/WebKitGTK 4.1環境と、公式対応の`gtk3`タグを使用する |
| macOS | 固定Xcode/SDKを使用する。既存Go 1.27が要求するmacOS 13.0をappメタデータとlink設定にも明示する |
| Windows | 固定MSYS2、source固定OpenSSL/SQLCipher、明示的に選択したCI用Fixed Version WebView2を使用する |

`Taskfile.yml`は独自の読み取り専用build処理を呼び出す。通常の配布buildでは`go mod tidy`やOS packageの解決を行わない。Windows resourceとmacOS iconは、同じ固定CLIで生成する。APP_VERSIONはfrontendとGo binaryの両方へ渡す。

bindingの互換adapterは、native hostが付けるHTML markerを確認してから初期化する。初期化を完了してからアプリ画面のmoduleを読み込む。bindingの欠落は初期化失敗とし、server APIへ切り替えない。server buildではViteの定数によってbindingのimportを除去する。生成したmodelやwrapperもserver配布物へ含めない。

候補の再生成は`.github/workflows/wails-v3-candidates.yml`の手動実行で行う。依存の解決、binding生成、OSS候補の収集はCI内で完結する。このworkflowは承認一覧を自動変更しない。新しいnoticeと配布対象を確認してから、Goのmanifest、binding、OSS表示・承認・usageを同じ更新PRへ反映する。

配布物の検証では、SQLCipherの実行版・暗号化・link、OSS一覧、Windows DLL制限を維持する。native検査の対象と限界を以下に示す。

| 対象 | 実際に起動するもの | 検証する動作 |
| --- | --- | --- |
| Linux | 配布archiveから展開した未変更binary | AT-SPIによる保管庫作成、OSS表示、実台帳の描画、再起動後の解錠、windowを閉じた正常終了 |
| Windows | 配布用binaryと固定WebView2 | 保管庫作成・再解錠、OSS表示、実Go IPCでの取引作成・更新・正確な残高・再起動後の保持・削除、ロック中の読取拒否、正常終了、browser例外なし |
| macOS Intel / arm64 | 署名を検証した配布ZIPから展開したapp | SQLCipher自己診断、実processが所有する通常windowの生成、生成後5秒間の生存とpanic/起動時例外なし、private profileの後片付け |

macOSのwindow検査は固定XcodeのSwiftでCoreGraphicsの所有PID・layer・寸法を確認する。別processのwindowやmenuだけでは合格にしない。CI以外では起動せず、HOMEもprivate領域へ切り替える。macOSの検査は起動smokeであり、保管庫などの全UI操作や正常quitを確認したとは扱わない。停止は試験の後片付けとして行う。

macOSの後片付けでは起動したChildProcessのPIDだけへ停止を要求し、実際のprocess終了と所有windowの消失を確認してからprivate領域を削除する。OSが管理するWebKit XPC processを含み得るprocess group全体へのsignalは使わない。所有appのsignal権限エラー、停止期限超過、window残存は失敗にし、強制停止が必要だった場合も成功扱いにしない。

Windowsの取引検査は生成済みproduction bindingを介して実SQLCipher DBを操作する。mock APIやHTTP専用test endpointへ置き換えない。開発用browser試験は、製品serverとは別のCI専用タグで実行し、詳細は`security-monitoring.md`に記載する。Issue #299を実動作の証跡なしに解決済みとはしない。

SQLCipherの版をまたぐ形式互換性は別の必須CIで検査する。更新前のmain commit `0935595` に固定した4.18.0のchecksum検証付きrecipeで旧ライブラリを構築し、private領域へ暗号化DBを作る。そのDBを実4.20.0で読み書きし、4.18.0でも更新結果を再読込する。両probeは`PRAGMA cipher_version`で実リンク先を確認し、平文SQLiteでは合格にしない。fixture DBと既知の試験用鍵は配布せず、成功メタデータだけをartifactに保存する。アプリのschema移行・鍵操作は既存のGo/SQLCipher統合試験とnative保管庫検査でも確認する。

旧版形式の検査は今回の移行に限る一時CIであり、新バージョンの公開完了後に専用検査だけを別PRで撤去する。実施結果を残す条件と撤去対象は[Issue #356](https://github.com/ShiningWank0/Omni_Money/issues/356)で追跡する。実起動・実取引・通常のSQLCipher統合検査とVue再描画の回帰検査は維持する。

参照: [Wails 3.1.0の移行ガイド](https://github.com/wailsapp/wails/blob/v3.1.0/docs/mpress/content/migration/v2-to-v3.md)、[Go 1.27のmacOS要件](https://go.dev/doc/go1.27#darwin)。

CI専用browser hostの公開入口はHTTPSに限定する。各試験の一時証明書をNode clientはCAとして検証し、Chromiumはその公開鍵のSHA256だけを試験用profileで許可する。CookieはSecure/HttpOnly/SameSite Strictを要求し、平文HTTP・異なる証明書・異なるhostnameを拒否する。秘密鍵とprofileはartifactへ保存しない。通常のnative製品とserverの通信設定は変更しない。

配布用のgosec/govulncheckにはproduction/desktop/gtk3/SQLCipherのタグを明示し、同じ固定SQLCipherのheader/libraryを渡す。serverと手動dev診断の検査対象はそれぞれ別タグで維持する。

DesktopのApp serviceとそのvaultテストはnative/CI browserのbuild constraintで選択する。`server`だけのbuildにはWails applicationやGTKを取り込まない。Desktopテストをserver側へ流用せず、native側では引き続き実行する。生成bindingは`.gitattributes`でLFを指定し、Windowsのautocrlf設定にかかわらず生成物とのbyte比較を維持する。

Linux native UIの監督processはprivate D-Bus sessionの外で動く。初めにprivate XDG/TMPDIRを用意し、その環境でD-BusとUI試験を開始する。UI終了後にsessionとそのserviceを終了し、private領域内のmountが消えてからデータを削除する。Documents portalのFUSE mountが残る間は再帰削除しない。mount残存・削除失敗・UI失敗はすべて検証失敗とし、`session.json`と`result.json`へ証跡を残す。

Windows native UIの終了確認では、`Application.Quit()`がWebViewを破棄してCDP応答より先に接続を閉じる場合もある。終了要求前にアプリとpageが生存していることを確認し、この呼出しに対する既知の接続切断と実際のpage閉鎖が一致する場合だけ、応答の代わりとして扱う。所有するアプリprocessの15秒以内の終了・終了コード0・signalなしは常に必須とし、一般のJavaScript例外や通信異常は失敗にする。setup/reopen各回の終了結果を証跡に記録し、最後にprivate profileの消滅を確認する。
