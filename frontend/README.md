# Omni Money Frontend

Vue 3（Composition API）、Vite、Pinia、Chart.jsで構成する共通UIです。DesktopはWails binding、serverはHTTP APIを使用します。モードと通信処理は [src/utils/api.js](src/utils/api.js)、状態管理は [src/store/index.js](src/store/index.js)、画面は [src/App.vue](src/App.vue) と [components](src/components) を参照してください。

## 開発・検証

Nodeの固定版は [../.node-version](../.node-version)、対応範囲とnpm scriptsは [package.json](package.json)、依存の確定版は [package-lock.json](package-lock.json) を正とします。以下はFrontend変更時に必要な範囲で実行します。

```bash
# リポジトリルートから
cd frontend
npm ci --ignore-scripts
npm run test:unit
npm run test:component
npm run build
```

`npm run dev` はloopbackでViteを起動します。Vite単独では認証・Vaultを扱うbackendは起動しません。Desktop起動は [固定Wails/SQLCipherの配布条件](../build/README.md) に従い、server/browser E2EはCIの [使い捨てserver harness](tests/e2e/run-server-e2e.sh) を使用します。harnessはDocker containerを起動するため、ローカル実行は明示的な依頼がある場合だけ行います。[playwright.config.js](playwright.config.js) は `E2E_BASE_URL` がない場合にE2Eをskipする設計です。本番URLを設定して実行しないでください。

## 認証・保存の契約

- serverのパスキーはメール不要のdiscoverable loginを使用します。登録・loginにアカウントpasswordやPRF出力を必須にしません。Desktopはlocal vaultのpassword/recovery/idle-lockを使用します。
- serverの取引作成・更新は保存操作ごとのUUIDと固定した本文を送信し、受付POSTの応答喪失では同じIDで限定再送します。同じ内容の別操作は登録できます。
- 通常の保存は取引反映までpollします。logoutは画面状態を直ちに消去し、未確認の受付とsession失効まで待って遷移します。画像処理・取引反映・snapshotの完了は待ちません。
- 受付前の入力はメモリだけで保持します。未確認のlogoutは保護画面で再試行し、再login後は本人のpending/failed通知を取得します。generationの照合により古い応答が私的画面を再表示しません。
- HTTP成功はendpointのschemaと受付stateを確認します。失敗した読取りを空データで置換せず、取引更新成功後の再読取り失敗と保存失敗を区別します。
- WebのCSV importはプレビューと、置換時の削除同意が必要です。画像原データは新規添付合計7 MiB（Desktopは20 MiB）までで、backendの形式・件数・容量検証も受けます。

詳細は [APIエラーと保存受付](../docs/api-errors.md)、[利用ガイド](../docs/how-to-use.md)、[server安全モデル](../docs/server-multi-vault.md) を参照してください。通信・保存・認証のunit testは [tests](tests)、モーダル・画面の回帰検証は [tests/component](tests/component)、server E2Eは [tests/e2e](tests/e2e) にあります。
