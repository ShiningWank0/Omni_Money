# APIエラーの契約

APIと認証・CSRF・proxy・upload middlewareは `application/json` と `Cache-Control: no-store` でエラーを返します。
基本形は `{"error":"表示用メッセージ","code":"機械判定用コード"}` です。HTTP statusが成否の根拠です。
既存の `login_required`、`recent_auth_required`、`csrf_rejected` はbooleanのまま保持し、`retry_after_seconds` は数値のまま保持します。
予期しない5xxは固定メッセージへ置換し、DBエラーやpathを返しません。利用者向けと明示された既知の5xx文言（AI無効、snapshot利用不可、安全に開けない等）はそのまま返します。未知の `/api/` は認証後にJSON 404となり、静的ファイルへfallbackしません。
`/healthz` は死活監視用の `{"status":"ok"|"unavailable"}` を返し、このerror envelopeの対象外です。

codeは `invalid_request`、`authentication_required`、`forbidden`、`csrf_rejected`、`not_found`、
`method_not_allowed`、`conflict`、`payload_too_large`、`unsupported_media_type`、`recent_auth_required`、
`rate_limited`、`service_unavailable`、`internal_error`、その他の `request_failed` です。

JSON成功応答、204、CSVストリームは別の契約です。CSV本文をJSON middlewareでbufferしません。
認証情報更新・snapshot restoreの応答喪失では、エラーだから未実行と推定して再送してはいけません。


Frontendは `ApiError` の `message/status/code/retryable` で失敗を扱います。HTTPエラー、通信失敗、成功応答のschema違反を区別します。
`expectJSON` はendpointの最小schemaを検証し、一覧で明示的に許容する旧Goのnull sliceだけを空配列に正規化します。
`expectVoid` は204またはendpointで定義したJSON成功応答を受理します。keepaliveは204のみ、CSVとAI relayはraw Responseのまま扱います。
429/502/503/504や通信障害のretryableだけを根拠に、任意の変更要求を自動再送してはいけません。通常APIの再認証428は1回だけ再送します。取引保存受付は以下のUUID契約により限定した再送を行います。
認証情報更新の4xx拒否だけをdefinitiveResponseとして扱い、5xx・通信断・壊れた成功応答は適用済みの可能性を残します。


## サーバーの取引保存受付

取引エディターの作成・更新は次のAPIを使用します。DesktopのWails呼出し、CSVインポート、取引削除などには適用しません。

| Endpoint | 成功応答と意味 |
| --- | --- |
| `POST /api/transaction-saves` | 202。`user_id`、操作UUIDの `request_id`、新規作成では0の `target_id`、`transaction` を受け取り、入力を本人のSQLCipher Vaultへ永続化した受付を返す |
| `GET /api/transaction-saves/{request_id}` | 200。本人Vaultの受付状態を返す。存在しなければ404 |
| `GET /api/transaction-saves` | 200。`{"saves":[...]}` として本人のpending・未削除のfailed入力の通知を返す。画像本文は返さない |
| `DELETE /api/transaction-saves/{request_id}` | 200 `{"success":true}`。failed入力を削除する。pending・completedや対象がない場合は404。ID/hash記録は保持する |

受付は `request_id` と `state` を持ちます。`pending` は処理待ち、`completed` は取引反映済みで `transaction` を含み、`failed` は反映失敗で `error` を含みます。202や200だけで取引反映済みとは判断せず、必ずstateを確認します。取引反映とcompleted記録は同じSQL transactionで確定します。

UUIDは保存操作ごとに生成し、再送中は本文も固定します。同じID・同じ内容は既存の受付を返し、同じID・異なる内容は409 `conflict`、別IDの同じ内容は独立した取引として扱います。IDは本人Vault内に限定されます。`user_id` と認証済みsessionの本人が一致しない場合は403で拒否し、別アカウントへの再ログインで古い入力を送信しません。

通常HTTP body上限はBase64込み10 MiB、Frontendの新規画像原データは合計7 MiBです。queue内部の1入力32 MiB、保持入力32件/合計128 MiBの上限はHTTP制限を緩和しません。queue満杯は429、ID競合は409、受付storageの一時障害は503、既知の入力不正は400です。大きすぎるbodyも拒否しますが、decoderでの拒否は400となるため413だけで判定しないでください。エラー応答が失われる可能性も含めて受付を確認します。

Frontendの受付POSTは通信失敗、壊れた成功応答、retryableなエラーで同じUUID・同じ本文を1回再送します。403では認証状態を取得し、同一userであることを確認してCSRF tokenを更新し、1回再送します。別userへの切替え・未認証なら中止します。受付POSTは通常APIの再認証ダイアログ・自動遷移を行いません。

通常保存は受付後のstateをpollして反映を待ちます。logoutは未確認の受付POSTを確認してからsessionを失効させ、取引反映・画像処理・snapshotの完了は待ちません。確認できない受付やlogout応答は保護画面で再試行します。受付前の入力はブラウザのメモリのみで、永続offline outboxはありません。受け付けたpendingはprocess再起動後、本人の次回Vault unlockで再開します。

`save_failed`、`save_receipt_unconfirmed`、`save_account_mismatch`、`save_request_conflict`、`session_invalidated` はFrontend側の `ApiError.code` です。上記HTTP error envelopeのcodeと区別してください。受付済みのfailed入力は本人が通知を削除するまで画像込みで暗号化保管され、通知削除後もID/hashは再送防止のため残ります。

## Consumer UI

- Settings modals await the save callback before reporting success or closing. Failed saves/clears retain the selection; pending saves block repeat submissions and closing.
- Transaction mutations retain drafts on rejection and block duplicate save/delete calls. A completed write closes the form before refreshing the ledger; a failed refresh is a read error, not a claim that the write failed. Server transaction-save receipts use the bounded UUID retry protocol above; other mutations do not gain an automatic retry permission.
- Store reads retain the last successful data and expose a visible error with an explicit retry action. A successful empty list is distinct from a failed read. Tag summaries show separate loading/error/empty states.
- Reset invalidates every outstanding store read. App-level generations prevent late mutations or modal reads from reopening private UI or starting follow-up fetches after session expiry/unmount. Desktop hydration still propagates failures to the vault lock gate.
- Regression coverage includes pending/failed settings saves and clears, transaction failures and duplicate submission, all five store read paths after reset, initial hydration failure/retry, stale tag summaries, and the existing credential/snapshot fail-closed suites. Server/browser E2E injects transaction/settings 503 responses before retrying against the disposable server.
