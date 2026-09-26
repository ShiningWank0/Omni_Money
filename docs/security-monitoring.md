# 公開サーバーの脆弱性検査と通知

## CI/CDの検査範囲

`.github/workflows/ci.yml` は全PR、main/codex/updateへのpush、毎週日曜03:30 UTC（日本時間12:30）のscheduleで実行する。GitHub側の負荷で定刻より遅れる場合がある。

| 検査 | 対象と失敗条件 |
| --- | --- |
| govulncheck | Go標準ライブラリ・依存関係の既知の脆弱性。呼び出し経路に到達する問題で失敗。Desktopとserverの両ビルド条件を検査 |
| gosec | コードの危険な実装パターン。medium以上で失敗。Desktopとserverの両ビルド条件を検査 |
| npm audit | frontendの依存関係。high以上で失敗 |
| Trivy | ビルドしたOmni MoneyイメージのOS・ライブラリ。修正版のあるHIGH/CRITICALで失敗。`ignore-unfixed: true`のため未修正の問題は除外 |

Docker公開前にもこれらの検査を実行する。CI成功は、未修正の既知問題や静的解析で分からない設計上の問題がないことを保証しない。Pangolin、Newt、Gerbil、Traefik、Badger、VPS/TrueNASそのものは別配備のため、このCIの検査対象ではない。

## 現在の通知経路

失敗はリポジトリのActions画面と、GitHub Actionsの標準メール・Web通知に現れる。利用者のGitHub **Settings → Notifications → System → Actions** で通知先を有効にし、必要なら失敗時のみの通知を選択する。個人の通知設定はリポジトリから強制できない。

定期実行の通知先は、ワークフローの作成者、cron構文を最後に変更したユーザー、または無効化後に再有効化したユーザーに紐づく。2026-09-20の定期実行のactor/triggering_actorは `ShiningWank0` だった。今回の修正はcron構文を変更しない。

定期実行では検査のJSON結果からIssueを自動作成する。追加のSecret、PAT、SMTP設定は不要。GitHubが自動発行する`GITHUB_TOKEN`を使い、通知ジョブだけに`issues: write`を与える。PR・push・forkでIssue作成は実行しない。Slack/Webhook通知は設定していない。

Issueのメール通知には、[通知設定](https://github.com/settings/notifications)のWatching/ParticipatingでEmailを有効にし、リポジトリの **Watch → Custom → Issues** を選ぶ。CI失敗メールは別にActionsの通知を有効にする。送信はGitHubが行うため、VPSのメールサーバーも送信用Secretも不要。個人のメール通知設定・実際の配送は、このリポジトリから検証・変更しない。

### Issue作成のルール

- govulncheckは到達可能なsymbol、npmはhigh以上、Trivyは修正版のあるHIGH/CRITICALを対象にする。gosecのmedium以上は「静的解析」と明記し、既知CVEの検出と区別する。
- スキャナー・識別子・対象パッケージ（gosecはファイルと行）で同一性を判定する。Desktop/serverで重なるGoの問題は1件にまとめ、検査条件を併記する。
- 対象、深刻度、検出版または影響範囲、修正版・更新候補、実行ログへのリンクを記録する。GoのDBが深刻度を提供しない場合はUNSPECIFIEDと明記する。
- このワークフローが自動作成した同じ問題のOpen Issueには、内容が変わったときだけコメントする。実行日・run IDの変化だけでは追記しない。既存本文や人間のコメントを上書きしない。手動で作成したIssueは自動照合の対象外。
- ClosedのIssueを再オープンしない。閉じた問題を再検出した場合は新しいIssueを作る。検出が消えても自動クローズはしないので、対応PR・人間の確認で閉じる。
- 通信障害、不完全なJSON、先行ステップ失敗、artifact欠落・ダウンロードや整合性検証の失敗は「検査エラー」のIssueにする。脆弱性がないという判定にはしない。一般テストの失敗はActions通知で確認する。
- 通知自体のAPIエラーもCIを失敗させる。1回の新規作成・追記は合計25件までとし、超過時は失敗を表示する。残件は次回定期実行またはその定期実行の再実行で処理する。

通知ジョブは同じ実行・同じattemptのartifactだけを読む。過去のattemptの結果を混在させないため、再試行時は **Re-run all jobs** を選ぶ（失敗ジョブだけを再試行すると、そのattemptで未実行の検査が欠落扱いになる）。スキャナー側は読み取り権限のみで、通知用トークンはスキャナーへ渡さない。通知ジョブはレポートをデータとして解釈し、外部の説明文からコマンド・URLを実行しない。公開Issueへソースコード断片や生のstderrは転載しない。レポートの保存期間は7日。

定期実行の同時処理は直列化し、mainへのpushで進行中の定期実行をキャンセルしない。GitHub側の障害・ジョブ強制終了・通知設定によってメールが届かない場合もある。公開リポジトリは60日間活動がないとscheduleが自動無効化されるため、Actions画面で有効状態を確認する。

- [GitHub公式のワークフロー通知仕様](https://docs.github.com/en/actions/concepts/workflows-and-actions/notifications-for-workflow-runs)
- [通知設定](https://github.com/settings/notifications)
- [GITHUB_TOKENとIssue作成権限](https://docs.github.com/en/actions/tutorials/authenticate-with-github_token)
- [Issue購読とメール通知](https://docs.github.com/en/subscriptions-and-notifications/get-started/configuring-notifications)
- [定期実行の自動無効化](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/disable-and-enable-workflows)

ローカル検証は`node --test scripts/security-monitoring.test.mjs`。架空の検出結果と模擬GitHub APIを使用し、テストのためのIssue作成やメール送信は行わない。

## パスキーログインの情報漏えい対策と限界

有効なメール形式について、アカウント不存在・無効化・パスキー未登録でも、公開login/beginは架空の候補を含む認証開始応答を返す。候補数は登録上限の10件に揃え、transport情報を省略する。架空IDとPRF saltは目的分離した永続秘密鍵と正規化メールから決定的に生成し、通常の問い合わせ時間差には100msの応答時間下限を設ける。秘密鍵は既存control DB鍵からHKDFで導出するため、追加設定・DB移行は不要。

架空候補はサーバー側の認証許可リストに入れず、架空アカウントのceremonyはfinishで拒否する。本物の署名、RP/origin、user verification、本人のcredential、PRFによるvault鍵の復号、期限、一回限りの利用を引き続き検証する。

この変更はHTTP成否・候補数・架空値の使い回し方からの直接的な判別を抑える。既存の非discoverableパスキーとの互換性のため、実credential IDとその長さ、PRF saltをブラウザーへ渡す方式は維持する。既知IDとの照合、認証器固有の形式・長さの統計的推測、登録変更前後やcontrol DB鍵のローテーション前後の比較、負荷時の時間差まで隠すものではない。完全なcredential情報非開示には、discoverable credentialを必須化するか、WebAuthn開始前の別認証が必要であり、既存パスキーの利用条件が変わる。

[WebAuthnのUsername Enumeration / Privacy leak via credential IDs](https://www.w3.org/TR/webauthn/#sctn-username-enumeration) に沿った互換性を維持する緩和策として扱う。

## Pangolinの更新運用

Pangolin自身は起動時にDB・設定のmigrationを行う。公式更新手順は固定バージョンを更新してComposeを再作成し、管理画面・公開先・トンネルの疎通を確認する流れ。Gerbil・Traefik・Badgerも更新内容に応じて確認する。Badgerの設定自動更新は標準構成の場合に限られ、適用されなくても明示的な失敗にならない場合がある。

VPS上での自動化は可能。更新検知と通知を自動化し、適用処理は排他制御、対象版の固定、DB/configの復旧点、起動と認証付き疎通の確認、失敗通知をまとめる。DB schemaが変わるため、コンテナの旧版への差し戻しだけでは復旧できないことがある。通知だけの導入と、無人での更新適用は別の運用設定として扱う。

[Pangolin公式更新手順](https://docs.pangolin.net/self-host/how-to-update)

無料で更新通知を受け取る方法は[Pangolinの更新メール通知](pangolin-update-notifications.md)を参照。
