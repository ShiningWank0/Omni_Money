# Agent Workflow Notes

このリポジトリでAIエージェントが作業するときの運用メモ。利用者の依頼・実行許可を優先する。現行の実装境界は [Agent.md](Agent.md)、利用・運用は [README](README.md) とそこから参照するガイドを確認する。

## Git

- `main` へ直接コミットしない。利用者が指定または作成を許可した既存PRブランチ・作業ブランチで進める。
- 作業開始時にbranchと未コミット変更を確認し、既存変更を上書きしない。ファイル作成・コード編集は利用者が承認した範囲で行う。
- コミットは変更の目的が読み取れる単位にする。PRレビュー対応、Issue対応、ドキュメント更新を必要に応じて分ける。
- push・PR作成は利用者の依頼に従う。mergeは利用者の明示的な指示がある場合だけ行う。
- GitHub連携でcommitを作成した場合は、その内容とローカルファイルが一致することを確認してcheckoutも同期する。未コミットの複製を残して後のpullを妨げない。

## GitHub

- PR・Issue・branch等の作成や操作はGitHub連携ツールのみを使用し、`gh`コマンドは使用しない。
- PR本文には最終的な変更内容、目的、実施した検証と未実施の検証を書く。会話の経緯ではなくレビューに必要な情報を記載する。
- PR・Issueへのコメント送信は、利用者が明示的に依頼した場合だけ行う。関連Issueを閉じる依頼があればPR本文に `Closes #<issue-number>` を記載する。
- CI/CDの完了監視は、利用者が依頼した場合だけ行う。

## Verification

- 変更に必要な検証を選び、実施した内容を報告する。ドキュメントだけならリンク・ソース照合・documentation contract・`git diff --check` を確認する。
- Go変更は関連packageのtestを行い、影響範囲に応じて広げる。SQLCipher/serverの本番設定はDockerfileとworkflowを正とし、通常SQLiteの結果を本番暗号化の検証として扱わない。
- Frontend変更は関連するunit/component testと `npm run build`、UI変更は可能ならブラウザで表示・操作を確認する。
- Docker build、コンテナ起動、SQLCipher source build、server/browser E2Eは原則CI/CDで行う。ローカル実行は利用者の明示的な依頼がある場合だけ行う。
- 負荷の高いbuildやrace testを無断で並列実行しない。失敗の調査では関連ログ・関連testから確認する。

## Product Constraints

- 現行Vue UIの見た目・操作感を尊重する。
- 銀行連携は行わず、CSV/importや手入力で自己完結する設計を維持する。
- 取引紐付けはクレジットカード支払いと銀行口座引き落としの照合用途に限定する。
- serverの取引保存は操作UUIDによる再送識別を維持し、同じ内容の別取引を禁止しない。logoutは受付確認とsession失効まで待ち、画像処理・取引反映・snapshot完了を待たない。
