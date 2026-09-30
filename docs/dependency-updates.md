# 依存更新のレビュー

Dependabotは週次で確認し、各エコシステムの通常更新PR上限は5件とする。
Go・npm・GitHub Actions・Dockerのminor/patch更新をそれぞれまとめ、major更新は個別PRにする。
脆弱性修正はエコシステム毎に別グループとしてminor/patchをまとめ、majorは個別PRにする。
更新対象を恒久的にignoreしない。

定期検査が作成するIssueも同じ方針で、同一パッケージの複数advisoryは1件へ集約する。

Wails、WebAuthn、DockerのGo/Nodeは通常グループから除外し、個別PRで扱う。
Goの1.x間、WebAuthnの0.x間はSemVerではminorでも、関連設定やAPI移行が必要になるためである。

## ビルド版の固定元

- Goの最低対応版は `go.mod` の `go`、CI/配布用固定版は `toolchain`。各workflowは `go-version-file: go.mod` を使う。
- NodeのCI/配布用固定版は `.node-version`。`frontend/package.json` の `engines` は対応範囲であり、固定版とは別にレビューする。
- Wails CLIは `go.mod` のWailsモジュールと同じ固定版を導入する。`latest` は使わない。
- DockerのGo/Nodeイメージは同じ版の `tag@sha256` を維持する。Alpineの系列変更は別途サポート範囲とSQLCipherの動作をレビューする。

DockerのGo/Node更新PRでは、新しいタグとdigestを確認してから次を実行し、関連ファイルを同じPRに含める。

```sh
node scripts/runtime-versions.mjs sync
node scripts/runtime-versions.mjs check
```

`sync` はDockerfileを基に `go.mod` のtoolchainと `.node-version` だけを同期する。
Go最低対応版、Node対応範囲、API利用コード、ドキュメントの意味を自動変更するものではない。
最低対応版より古いGoイメージ、digest未固定や曖昧なビルドstageは拒否する。
CIの整合性チェックとGoテストは、Dockerだけの更新による固定版の不一致を拒否する。

## マージ前の確認

通常のCIに加え、Wails更新は4構成のdesktop buildとSQLCipher検証、WebAuthn更新はPRFを含む登録・ログイン・ユーザー列挙防止のテストを確認する。
フロントエンドのmajor更新はdesktopモードの画面・ストア操作とserver E2Eを確認する。

Docker release workflowはPR上で、本番と同じBuildx/build-push Actionによるamd64/arm64ビルド、SBOM/provenance生成を行う。レジストリへのpush、ログイン、署名公開は行わない。
この検証は公開後のattestation検証の代替にはならない。attestation Action更新は、証明生成・検証まで別途確認する。

Dependabot PRに移行コードを追加した後は自動rebaseが停止するため、最新mainの取り込みは明示的に行う。
グループPRを分割するときは代替PRを用意し、更新を取りこぼさないよう元PRから追跡できる状態にして閉じる。
