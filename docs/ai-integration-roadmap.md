# AI連携・Discordレシート登録ロードマップ（廃止済みlegacy／将来設計）

## 1. 目的

これは現行機能の説明ではない。Desktop と multi-user server の両 production mode で AI は未提供であり、server は user/vault に束縛されない旧 AI environment variables を設定すると起動を拒否する。旧 package、CLI、route/schema は retired legacy と、将来の user-vault-bound design を検討する資料としてのみ残す。

将来設計でも、LLMの出力をそのままDBへ流さず、vault-bound application serviceの検証・scope・監査を通す。既存データの編集、削除、設定変更、CSV import、snapshot restore は許可しない。

## 2. 現在のproduction status

現在、AI listener、AI console、AI credentialは存在しない。公開Webにも専用listenerにもAI routeを登録せず、`/api/v1/ai/*` と `/api/ai-console/*` は 404 である。

| モード／項目 | 現在の状態 |
| --- | --- |
| Desktop / server のAI | 非提供（productionでは旧設定を拒否、AI routeは404） |
| dormant source | retired legacy。production capabilityや認証情報のsource of truthではない |
| Stage 4 | user-vault-bound AI capabilityとして planned/unshipped |

## 3. 将来の責務分離（未出荷）

```mermaid
flowchart LR
    D["Discord Adapter<br>別プロセス"] --> L["Local / Cloud LLM"]
    L --> D
    D --> M["AI Transaction Manager<br>内部専用"]
    M --> A["Omni Money Application Service"]
    A --> DB[(SQLite)]
```

- Discord AdapterはDiscordイベント、添付ファイル取得、再試行、結果返信を担当します。
- LLMは画像から構造化された取引候補を生成します。DBやOmni Moneyの認証情報は持ちません。
- AI Transaction Managerは候補値の検証、正規化、重複防止、監査を担当します。
- Omni Money Application ServiceだけがSQLiteを書き込みます。
- Discord Adapter、LLM、ManagerへSQLiteファイルをマウントしません。

クラウドLLMを使う場合も、クラウド側から自宅へ着信させません。ローカルのAdapterがクラウドAPIへ発信して構造化結果を受け取り、内部のManagerへ渡します。

## 4. AI Transaction Managerの将来API（未出荷）

すべてPOSTとし、パスと資格情報scopeを明示的に許可します。

| API案 | 用途 |
| --- | --- |
| `POST /api/v1/ai/context` | 口座、タグIDと階層、取引種別、タイムゾーン、日付範囲、画像制限、schema versionを返す |
| `POST /api/v1/ai/transactions/validate` | DBへ書き込まず、候補値の正規化結果とエラーを返す |
| `POST /api/v1/ai/transactions` | 検証済み取引を原子的に追加する |
| `POST /api/v1/ai/analysis` | 条件に一致する取引を分析する |

`context` はLLMが `現金`、`銀行口座` などの既存口座やタグを知るために使います。取引明細は返しません。新しい口座名をAIが自由に作ることは既定で許可せず、現在の候補から選ばせます。

## 5. 将来の入力検証ポリシー

AI入力はJSON Schemaに合っていても信用せず、サーバー側で再検証します。

### 将来実装する検証

- `account`、`date`、`item`、`type`、`amount`を必須とする
- 前後の空白を除去する
- `type` は `income` または `expense` のみ
- `amount` は正整数のみ
- `date` は `YYYY-MM-DD` のみ
- サーバーの今日を基準に、1年前から2日後までを許可する
- `time` を指定する場合は `HH:MM` とする
- タグIDの存在確認と重複排除を行い、未知IDを拒否する
- 画像のBase64、ファイル名、JPEG/PNG/GIF/WebPの宣言MIMEを検証する

### 次段階で追加する検証

- `account` はcontextが返した既存値だけを許可する
- 文字数、画像枚数、画像1枚と合計のバイト数に上限を設ける
- Base64を厳密に復号し、JPEG/PNG/GIF/WebPのmagic bytesと宣言MIMEを照合する
- 取引、画像、タグ、残高再計算を1つのDB transactionで実行する
- 画像やタグの1件でも失敗した場合は全体をrollbackする
- `Idempotency-Key` を必須にし、Discordの再送で同じ取引を重複登録しない

## 6. 将来の画像の受け渡し（未出荷）

将来のMVPで採用を検討する方式であり、現行production APIでは使えない。

1. Discord AdapterがDiscord CDNから画像を取得します。
2. Content-Type、ダウンロード上限、timeoutを確認します。
3. 必要であればAdapter専用の一時ディレクトリへ権限 `0600` で保存します。
4. Base64へ変換し、`images` 配列としてManagerへ送ります。
5. 成功・失敗にかかわらず一時ファイルを即時削除します。

Managerへ任意のファイルパスや任意URLを渡す方式は採用しません。パストラバーサルやSSRFの入口になるためです。Manager側で一時ファイルが必要な場合は `os.CreateTemp` と専用tmpfsを使い、TTL付き清掃処理を設けます。

Base64は元データより約33%大きくなるため、現在のHTTP body上限10MiBを踏まえて画像上限を決定します。大容量化が必要になった段階でmultipartまたは内部オブジェクト参照を検討します。

## 7. 将来のDiscord処理フロー（未出荷）

1. Discord Adapterがイベントを受信し、署名とbot tokenを検証します。
2. レシート添付を上限・timeout付きで取得します。
3. 選択されたローカルLLMまたはクラウドLLMへ画像を送り、構造化候補を生成します。
4. Managerのcontextと候補値を照合します。
5. validate APIでサーバー側検証を行います。
6. 信頼度が低い、日付や金額が曖昧、未知口座がある場合は登録せず人間確認待ちにします。
7. `discord:<message_id>:<attachment_id>` 形式のIdempotency-Keyで追加します。
8. Discordへ取引IDまたは要確認理由を返信します。

レシート内の文字列にはprompt injectionが含まれる可能性があります。LLMへ汎用的なtool実行権限を与えず、構造化出力だけを許可します。

## 8. 将来のUIからの操作（未出荷）

将来のサーバーモード「AI API操作」画面案は通常のセッション認証と user/vault binding を通す。現行serverにはこの画面もBearer tokenもlistenerも存在しない。

この管理画面と別プロセスAdapterは将来案であり、現行のproduction capabilityではない。

## 9. 将来のセキュリティと監査

- client別token、最小scope、rotation、revokeを維持し、90日以内に更新する
- 資格情報と接続元単位のrate limitを維持する
- credential ID、操作、接続元、mTLS証明書fingerprint、専用監査鍵とkey IDでHMAC化した口座参照、期間、明細種別、該当／返却件数、日時、許否、HTTP statusを構造化監査ログへ残す
- token、provider key、リクエスト本文、金額、メモ、Base64画像、レシート全文はログへ残さない
- クラウドLLMへ画像を送る場合はUIで明示し、利用者のopt-inを必須にする
- Manager障害時にDB直接書き込みへfallbackしない
- Dockerでは最終的にManagerをprivate networkのみに置き、host portsを公開しない

## 10. 段階的な実装計画（Stage 4 planned/unshipped）

1. AI分析の全フィルター整合と回帰テスト
2. AI専用の日付・必須項目検証と管理UI
3. context、validate、atomic create、Idempotency-Key、画像厳格検証
4. Discord Adapterを別プロセスとして追加し、人間確認フローを実装
5. AI Transaction Managerを別プロセス/別コンテナへ抽出し、private networkまたはUnix domain socketへ移行

## 11. 受入テスト

- 今日の1年前と2日後は登録でき、その1日外側は拒否される
- AIで必須項目不足、未知口座、未知タグ、壊れた画像を拒否する
- 同じIdempotency-Keyでは1件だけ登録される
- AIポートのPUT/DELETEは拒否される
- 公開WebポートのAIパスは利用できない
- AIポートの通常API、ログイン、静的ファイルは利用できない
- ManagerポートへLAN/インターネットから到達できない
- ブラウザbundle、localStorage、ログへ秘密情報が入らない
