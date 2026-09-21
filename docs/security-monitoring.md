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

Slack/Webhook通知やIssue自動作成は設定していない。検査失敗と脆弱性検出は同義ではなく、取得先の障害やテスト失敗でもCIは失敗するため、通知から実行ログを確認する。

- [GitHub公式のワークフロー通知仕様](https://docs.github.com/en/actions/concepts/workflows-and-actions/notifications-for-workflow-runs)
- [通知設定](https://github.com/settings/notifications)

## パスキーログインの情報漏えい対策と限界

有効なメール形式について、アカウント不存在・無効化・パスキー未登録でも、公開login/beginは架空の候補を含む認証開始応答を返す。候補数は登録上限の10件に揃え、transport情報を省略する。架空IDとPRF saltは目的分離した永続秘密鍵と正規化メールから決定的に生成し、通常の問い合わせ時間差には100msの応答時間下限を設ける。秘密鍵は既存control DB鍵からHKDFで導出するため、追加設定・DB移行は不要。

架空候補はサーバー側の認証許可リストに入れず、架空アカウントのceremonyはfinishで拒否する。本物の署名、RP/origin、user verification、本人のcredential、PRFによるvault鍵の復号、期限、一回限りの利用を引き続き検証する。

この変更はHTTP成否・候補数・架空値の使い回し方からの直接的な判別を抑える。既存の非discoverableパスキーとの互換性のため、実credential IDとその長さ、PRF saltをブラウザーへ渡す方式は維持する。既知IDとの照合、認証器固有の形式・長さの統計的推測、登録変更前後やcontrol DB鍵のローテーション前後の比較、負荷時の時間差まで隠すものではない。完全なcredential情報非開示には、discoverable credentialを必須化するか、WebAuthn開始前の別認証が必要であり、既存パスキーの利用条件が変わる。

[WebAuthnのUsername Enumeration / Privacy leak via credential IDs](https://www.w3.org/TR/webauthn/#sctn-username-enumeration) に沿った互換性を維持する緩和策として扱う。

## Pangolinの更新運用

Pangolin自身は起動時にDB・設定のmigrationを行う。公式更新手順は固定バージョンを更新してComposeを再作成し、管理画面・公開先・トンネルの疎通を確認する流れ。Gerbil・Traefik・Badgerも更新内容に応じて確認する。Badgerの設定自動更新は標準構成の場合に限られ、適用されなくても明示的な失敗にならない場合がある。

VPS上での自動化は可能。更新検知と通知を自動化し、適用処理は排他制御、対象版の固定、DB/configの復旧点、起動と認証付き疎通の確認、失敗通知をまとめる。DB schemaが変わるため、コンテナの旧版への差し戻しだけでは復旧できないことがある。通知だけの導入と、無人での更新適用は別の運用設定として扱う。

[Pangolin公式更新手順](https://docs.pangolin.net/self-host/how-to-update)
