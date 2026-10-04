# Omni Money Desktopビルド資材

Wails Desktopアプリのアイコン・OS設定・installerテンプレートを保持します。build設定の正は [../wails.json](../wails.json) と [Desktop release workflow](../.github/workflows/release-desktop.yml) です。

| Path | 用途 |
| --- | --- |
| `appicon.png` | 共通アプリアイコン |
| `darwin/Info.plist` | macOS配布アプリの設定 |
| `darwin/Info.dev.plist` | macOS開発アプリの設定 |
| `windows/icon.ico` | Windowsアイコン |
| `windows/wails.exe.manifest` | Windowsアプリのmanifest |
| `windows/info.json` | Windowsの製品・version metadata |
| `windows/installer/` | Windows installerテンプレート |
| `bin/` | build時の生成先（配布元ソースではない） |

## 配布条件

配布はmacOS Intel、macOS Apple Silicon、Windows amd64、Linux amd64の4構成です。Wails CLIは [go.mod](../go.mod) のモジュールと同じ固定版、Nodeは [.node-version](../.node-version)、SQLCipherは [scripts](../scripts) のOS別 `build-sqlcipher-*.sh` が固定する版を使用します。build tagsは `libsqlite3 sqlite_omit_load_extension`、CGO設定とOS別の静的リンク条件はworkflowを参照してください。通常SQLiteへのfallbackは許可しません。

workflowはbuildした成果物を起動して暗号化DB、wrong-key拒否、平文header/canary不在、load extension無効化を確認してから配布します。関連pathのPRではbuild検証だけを行い、mainのVERSIONまたはrelease workflow変更時にversion Releaseを公開します。

ルートの [VERSION](../VERSION) からGoの `main.version` とFrontendの `VITE_APP_VERSION` へ同じ版を埋め込みます。これらの資材を削除して既定テンプレートへ戻す手順は使用せず、製品名・権限・manifestとrelease設定の整合性を維持してください。

ローカルのSQLCipher source buildやOS別の重いbuildは明示的に依頼された場合だけ行い、正式な配布検証はCI/CDで行います。インストール・Vault作成・復旧は [利用ガイド](../docs/how-to-use.md)、鍵の境界は [SQLCipher鍵の運用](../docs/sqlcipher-key-operations.md) を参照してください。
