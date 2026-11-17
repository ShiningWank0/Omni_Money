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

配布物の検証では、SQLCipherの実行版・暗号化・link、OSS一覧、Windows DLL制限を維持する。LinuxとWindowsのnative UI試験は配布binaryを使う。macOSのUI操作試験は追加しておらず、buildとSQLCipher self-testによる検証に限られる。開発用browser試験は、製品serverとは別のCI専用タグで実行し、詳細は`security-monitoring.md`に記載する。Issue #299を実動作の証跡なしに解決済みとはしない。

参照: [Wails 3.1.0の移行ガイド](https://github.com/wailsapp/wails/blob/v3.1.0/docs/mpress/content/migration/v2-to-v3.md)、[Go 1.27のmacOS要件](https://go.dev/doc/go1.27#darwin)。
