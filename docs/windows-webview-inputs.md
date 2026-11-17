# Windows CIのFixed Version WebView2

Windowsのnative UI検証にはMicrosoft公式のFixed Version Runtimeを使用する。source URLとCAB SHA256を`webview2-source.json`に記録し、保存workflowがGHCRにOCI artifactとして保持する。通常のsetupは`webview2.json`の実digestから取得し、CAB hash、実行ファイル版、Microsoftの署名を照合する。

展開したCI専用directoryにrenderer sandbox用のread/execute ACLを付与し、`WEBVIEW2_BROWSER_EXECUTABLE_FOLDER`を設定する。Evergreen Runtimeへのfallbackを検証成功として扱わない。native試験では実WebViewのCDP版に加え、起動された`msedgewebview2.exe`が固定directory内にあることも確認する。

試験は配布用の同じexeを起動する。専用APPDATAとWebView profileで初期設定、OSS表示、暗号化vaultの解除、正常終了、再起動後の解除を行う。CDPはこの試験用processのloopback portだけに開く。秘密や回復コードを成果物に保存せず、試験後にprofileを削除する。製品の起動設定や配布appへのRuntime同梱は変更しない。

検証結果にはbinary SHA256、Runtime版、固定directoryからの起動確認、各roundの成否を保存する。CIではこの検証を配布archive作成より前に実行する。Windows OSそのもの、macOS WKWebView、server E2E用Playwrightとは別の入力である。

出典: [Microsoft公式Fixed Version配布](https://developer.microsoft.com/en-us/microsoft-edge/webview2/)、[公式配布・ACL手順](https://learn.microsoft.com/en-us/microsoft-edge/webview2/concepts/distribution)。
