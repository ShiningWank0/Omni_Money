# 公開サーバーの脆弱性検査

## CI/CDの検査範囲

`.github/workflows/ci.yml` は全PR、mainへのpush、毎週日曜03:30 UTC（日本時間12:30）のscheduleで実行する。GitHub側の負荷で定刻より遅れる場合がある。

| 検査 | 対象と失敗条件 |
| --- | --- |
| govulncheck | Go標準ライブラリ・依存関係の既知の脆弱性。呼び出し経路に到達する問題で失敗。Desktopとserverの両ビルド条件を検査 |
| gosec | コードの危険な実装パターン。medium以上で失敗。Desktopとserverの両ビルド条件を検査 |
| npm audit | frontendの依存関係。high以上で失敗 |
| Trivy | Omni MoneyイメージのOS・ライブラリ。修正版のあるHIGH/CRITICALで失敗。`ignore-unfixed: true`のため未修正の問題は除外。Docker Releaseでは各アーキテクチャを**タグなしのdigestとしてpushしてから**スキャンし、両方成功した後にのみrelease tagを公開する |

Goのgovulncheck/gosec等はPRと週次scheduleで実行し、main pushではGo checksジョブを省略する。各jobの条件は `ci.yml` を正とする。Docker Releaseは各アーキテクチャのTrivyを公開前に実行し、次の順序を守る: タグなしdigest push → アーキテクチャ毎のTrivyスキャン → staging index作成 → build provenance attestation付与 → `gh attestation verify`（署名workflow・source ref・GitHub-hosted runnerを固定）→ version/latestタグ公開。スキャン・attestation・検証のいずれかが失敗するとrelease tagは作られない。配備側は`scripts/resolve-image-digest.sh --verify --repo OWNER/REPO`でprovenance検証を必須とする。

`release-docker.yml`は`workflow_dispatch`で**リハーサル**実行できる。releaseと同じビルド・スキャン・attestation・検証を行い、version/latestタグは公開しない（`rehearsal-staging`タグのみ更新）。VERSION変更を伴う本番Releaseの前に、GitHub Actions画面のRun workflowで対象branchを選び、経路全体を検証する。CI成功は、未修正の既知問題や静的解析で分からない設計上の問題がないことを保証しない。Pangolin、Newt、Gerbil、Traefik、Badger、VPS/TrueNASそのものは別配備のため、このCIの検査対象ではない。

## 定期検査のIssue報告

定期実行では検査のJSON結果からIssueを自動作成する。GitHubが自動発行する`GITHUB_TOKEN`を使い、Issue報告ジョブだけに`issues: write`を与える。追加Secretは不要。PR・push・forkではIssue作成を実行しない。

### Issue作成のルール

- govulncheckは到達可能なsymbol、npmはhigh以上、Trivyは修正版のあるHIGH/CRITICALを対象にする。gosecのmedium以上は「静的解析」と明記し、既知CVEの検出と区別する。
- スキャナーと対象パッケージで同一性を判定し、同じパッケージの複数advisory（識別子）は1つのIssueへまとめて本文に併記する。gosecはルールとファイル・行で判定する。Desktop/serverで重なるGoの問題は1件にまとめ、検査条件を併記する。
- 対象、深刻度、検出版または影響範囲、修正版・更新候補、実行ログへのリンクを記録する。GoのDBが深刻度を提供しない場合はUNSPECIFIEDと明記する。
- このワークフローが自動作成した同じ問題のOpen Issueには、内容が変わったときだけコメントする。実行日・run IDの変化だけでは追記しない。既存本文や人間のコメントを上書きしない。手動で作成したIssueは自動照合の対象外。
- ClosedのIssueを再オープンしない。閉じた問題を再検出した場合は新しいIssueを作る。検出が消えても自動クローズはしないので、対応PR・人間の確認で閉じる。
- 通信障害、不完全なJSON、先行ステップ失敗、artifact欠落・ダウンロードや整合性検証の失敗は「検査エラー」のIssueにする。脆弱性がないという判定にはしない。一般テストの失敗はActionsの実行結果で確認する。
- Issue報告のAPIエラーもCIを失敗させる。1回の新規作成・追記は合計25件までとし、超過時は失敗を表示する。残件は次回定期実行またはその定期実行の再実行で処理する。

Issue報告ジョブは同じ実行・同じattemptのartifactだけを読む。過去のattemptの結果を混在させないため、再試行時は **Re-run all jobs** を選ぶ（失敗ジョブだけを再試行すると、そのattemptで未実行の検査が欠落扱いになる）。スキャナー側は読み取り権限のみで、Issue書き込み用トークンはスキャナーへ渡さない。Issue報告ジョブはレポートをデータとして解釈し、外部の説明文からコマンド・URLを実行しない。公開Issueへソースコード断片や生のstderrは転載しない。レポートの保存期間は7日。

定期実行の同時処理は直列化し、mainへのpushで進行中の定期実行をキャンセルしない。公開リポジトリは60日間活動がないとscheduleが自動無効化されるため、Actions画面で有効状態を確認する。

ローカル検証は`node --test scripts/security-monitoring.test.mjs`。架空の検出結果と模擬GitHub APIを使用する。

## 依存関係の自動更新とリポジトリ設定

`.github/dependabot.yml` が、Go module、frontendのnpm、workflowのSHA pin、Dockerfileのbase image digestを毎週の更新PRとして提案する。既知脆弱性の検出（npm audit・govulncheck・Trivy）とは別の仕組みで、更新PRも通常のCIとレビューを通る。

次の3つはGitHubリポジトリの設定であり、CIやリポジトリ内のファイルでは強制できない。管理者が有効化し、GitHubのSettings/Actions画面で状態を確認する。

- **Dependabot alerts / security updates**: 既知脆弱性に対する修正PRを自動作成する
- **Secret scanning / push protection**: 誤ってcommitした鍵やtokenを検出し、push時に拒否する
- **Code scanning (CodeQL default setup)**: GoとJavaScriptのデータフロー解析を継続実行する

無効化されていてもCIは成功するため、定期実行のたびにこの節の設定が有効か確認する。

## パスキーログインの情報漏えい対策と限界

現行Frontendのパスキーログインは `/api/auth/passkeys/discover/begin` と `/finish` を使用する。メール入力なしのdiscoverable ceremonyを開始し、開始応答にuser別のcredential候補を含めない。選択されたcredentialの署名、challenge、RP/origin、user handle、user verification、期限、一回限りの利用を検証してから本人のVaultを開く。標準パスキーはserver-custody envelopeを使用し、PRF出力を要求しない。旧形式の互換処理と自動移行は [server安全モデル](server-multi-vault.md#passkey-authentication) を参照。

### メール指定の互換login API

有効なメール形式について、アカウント不存在・無効化・パスキー未登録でも、メール指定の公開login/beginは架空の候補を含む認証開始応答を返す。候補数は登録上限の10件に揃え、transport情報を省略する。架空IDとPRF saltは目的分離した永続秘密鍵と正規化メールから決定的に生成する。login/beginは関数全体に、login/finishは全ての失敗応答（架空ceremonyを含む）に100msの応答時間下限を設け、成功時のみ下限を適用しない。秘密鍵は既存control DB鍵からHKDFで導出するため、追加設定・DB移行は不要。

架空候補はサーバー側の認証許可リストに入れず、架空アカウントのceremonyはfinishで拒否する。本物の署名、challenge、RP/origin、user verification、本人のcredential、期限、一回限りの利用を引き続き検証する。検証後は標準パスキーのcustody envelopeを開き、旧PRF envelopeの互換経路ではPRF出力でVault鍵を開く。

この変更はHTTP成否・候補数・架空値の使い回し方からの直接的な判別を抑える。既存の非discoverableパスキーとの互換性のため、実credential IDとその長さ、PRF saltをブラウザーへ渡す方式は維持する。既知IDとの照合、認証器固有の形式・長さの統計的推測、登録変更前後やcontrol DB鍵のローテーション前後の比較、負荷時の時間差まで隠すものではない。この限界はメール指定の互換APIに対するもので、現行Frontendのdiscoverable開始応答にはuser別のallowCredentialsを含めない。

[WebAuthnのUsername Enumeration / Privacy leak via credential IDs](https://www.w3.org/TR/webauthn/#sctn-username-enumeration) に沿った互換性を維持する緩和策として扱う。
