# Agent Workflow Notes

このリポジトリでAIエージェントが作業するときの運用メモ。

## Git

- `main` へ直接コミットしない。既存PRブランチまたは作業ブランチで進める。
- コミットは小さく分ける。1コミットにつき「何を直したか」「何を追加したか」が読み取れる粒度にする。
- PRレビュー対応、Issue対応、検証追加、ドキュメント更新は可能な範囲で別コミットにする。
- pushは作業単位が通った時点で行ってよい。mergeは人間の確認後に行う。

## GitHub

- PRレビューへ対応したら、PRに対応内容・検証結果・残リスクをコメントする。
- Issueを直した場合は、Issueにも対応PRと内容をコメントする。
- IssueをPRで閉じる場合は、PR本文に `Closes #<issue-number>` を明記する。

## Verification

- Go変更後は原則 `go test ./...` を実行する。
- サーバーモード変更後は `go build -tags server -o /private/tmp/omni_money_server ./server.go` を実行する。
- フロントエンド変更後は `frontend` で `npm run build` を実行する。
- UI変更は可能ならローカルブラウザで主要モーダルやメニューの表示崩れを確認する。

## Product Constraints

- legacy UIの見た目・操作感を尊重する。
- 銀行連携は行わず、CSV/importや手入力で自己完結する設計を維持する。
- 取引紐付けはクレジットカード支払いと銀行口座引き落としの照合用途に限定する。
