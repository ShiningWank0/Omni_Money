# Pangolinの更新メール通知（無料・既存リソース）

調査・再確認日: 2026-09-26。目的は新しいPangolinの公開をメールで知ること。更新の自動適用は行わない。公式latest releaseは[1.23.0](https://github.com/fosrl/pangolin/releases/tag/1.23.0)（2026-09-16公開）。同日の利用者申告ではVPSも1.23.0に更新済みで、他のサービスは安定稼働中。VPSへ直接接続しての確認は行っていない。

## 推奨: GitHub Releasesをメール購読する

新しいリリースの公開を知る用途は、既存GitHubアカウントの通知機能で満たせる。追加料金、Secret、VPSの常駐プログラム、メールサーバーは不要。GitHub自身が登録済みのメールアドレスへ送信する。

1. [GitHubの通知設定](https://github.com/settings/notifications)で、Watching（購読中のリポジトリ）のEmailを有効にする。通知先はGitHubで検証済みのメールアドレスを選ぶ。Web上でも読みたい場合はOn GitHubを有効にする。
2. [fosrl/pangolin](https://github.com/fosrl/pangolin)を開き、**Watch → Custom → Releases → Apply** を選ぶ。Starを付けるだけでは購読にならない。
3. [購読一覧](https://github.com/watching)でPangolinの購読を確認する。既に公開済みの版は[Releases](https://github.com/fosrl/pangolin/releases)で確認し、過去のリリースのメール再送は前提にしない。
4. 次回リリース時にメールの受信と迷惑メール振り分けを確認する。

これは「リリースが公開された」という通知であり、VPSにインストールされた版との比較や更新完了の確認はしない。安定版だけの厳密な条件指定、特定版からの更新可否の判定も別処理が必要。Gitタグやコンテナイメージの更新のみでGitHub Releaseが公開されない場合は検知対象外。

必要に応じて[Gerbil](https://github.com/fosrl/gerbil)、[Badger](https://github.com/fosrl/badger)、[Traefik](https://github.com/traefik/traefik)、[Newt](https://github.com/fosrl/newt)のReleasesも同じ方法で購読できる。Pangolinの更新時は各コンポーネントの組み合わせを公式手順で確認する。

## より細かな条件が必要な場合

| 方法 | 追加費用・リソース | 資格情報 | 判定できること |
| --- | --- | --- | --- |
| GitHub Releasesの購読 | 既存GitHubアカウントのみ | 追加不要 | 新リリースの公開 |
| 既存公開リポジトリのActionsで確認しIssue化 | 標準GitHub-hosted runnerは公開リポジトリで無料。短い日次ジョブで実装可能 | 自動発行のGITHUB_TOKEN | 安定版の選別・版ごとの重複防止。VPSの稼働版は別途入力が必要 |
| 既存VPSのsystemd timerと既存メールのSMTP送信 | VPS内の定期処理。SMTP送信が現在のメール契約内で使える場合だけ追加費用なし | 既存メール提供元の送信認証情報 | VPSの稼働版との比較・更新後の再通知抑制 |

### GitHub Actionsで安定版だけをIssue化する場合

1日1回、GitHub Releases APIから`fosrl/pangolin`の最新安定版を取得する。`draft`/`prerelease`を除き、リリースIDまたはtagを既存Issueに記録する。同じ版でOpen Issueがある場合は通知を増やさず、新版を検知したときにだけIssue化する。メールはGitHubのIssue通知で送る。PAT・SMTPは不要。

公開リポジトリの標準runnerを使い、large runnerや有料サービスは使わない。公開リポジトリは60日間活動がないとscheduleが停止する点と、スケジュールが遅延し得る点に注意する。単にリリースを知りたい場合は、定期処理が不要な上の購読方法が適している。

### VPSの実稼働版と比較する場合

既存VPSでsystemd timerを日次実行し、次の処理にする。

1. Pangolinの固定image tagまたは稼働版をローカルで取得する。`latest`やdigestだけで版を判別できない場合は、版の記録方法を先に決める。
2. 公開Releases APIをHTTPSで読み、新しい安定版が稼働版より大きいことをSemVerで確認する。HTTP失敗やJSON不正を「更新なし」にしない。
3. 同じ稼働版・通知対象版の組み合わせを送信済みか、ローカルの状態ファイルで確認する。
4. 既存メール提供元の認証付きSMTPをTLSで使い、稼働版・新しい版・公式リリースへのリンクを送信する。送信成功後にだけ状態ファイルを更新する。失敗はjournalへ記録し、次回再試行する。
5. 送信資格情報は権限を制限したファイルに保存し、ログや公開リポジトリに出さない。受信用SMTPポートをVPSへ開ける必要はない。

公開Releasesの取得にはGitHubトークンは不要で、未認証APIは通常60回/時の枠がある。1日1回の確認はこの範囲内。ただし同じIPからの他の利用と合算される。

使っているメール提供元が未確認のため、このSMTP方式の無料利用可否・必要な認証方式は現時点では確定できない。新しい有料メール契約を前提にはしない。資格情報を用意せずに済ませる場合はGitHubの購読方法を選ぶ。

コンテナイメージの更新まで監視する既成ツールには[Diun](https://crazymax.dev/diun/)があり、既存SMTPへのメール通知に対応する。ただし、イメージ監視とPangolinのGitHub Release通知は検知対象が異なる。今回の目的だけなら追加デーモンは不要。

## 今回の実施範囲

上記は調査と導入手順の記録。GitHubの個人通知設定、Pangolinリポジトリの購読、VPSの設定は変更していない。Omni Moneyの脆弱性Issue自動作成は別途[security-monitoring.md](security-monitoring.md)に記載している。

## 一次資料

- [GitHub: Releasesとリリース通知](https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases)
- [GitHub: メール通知とCustom購読の設定](https://docs.github.com/en/subscriptions-and-notifications/get-started/configuring-notifications)
- [GitHub: Releases API](https://docs.github.com/en/rest/releases/releases#get-the-latest-release)
- [GitHub: APIのレート制限](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)
- [GitHub: 公開リポジトリの標準runner](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
- [GitHub: 定期実行の自動無効化](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/disable-and-enable-workflows)
- [Pangolin: 公式更新手順](https://docs.pangolin.net/self-host/how-to-update)
- [Diun: SMTP通知](https://crazymax.dev/diun/notif/mail/)
