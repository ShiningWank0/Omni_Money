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
