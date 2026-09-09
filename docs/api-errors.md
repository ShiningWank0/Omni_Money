# APIエラーの契約

APIと認証・CSRF・proxy・upload middlewareは `application/json` と `Cache-Control: no-store` でエラーを返します。
基本形は `{"error":"表示用メッセージ","code":"機械判定用コード"}` です。HTTP statusが成否の根拠です。
既存の `login_required`、`recent_auth_required`、`csrf_rejected` はbooleanのまま保持します。
5xxは固定メッセージへ置換し、DBエラーやpathを返しません。未知の `/api/` は認証後にJSON 404となり、静的ファイルへfallbackしません。

codeは `invalid_request`、`authentication_required`、`forbidden`、`csrf_rejected`、`not_found`、
`method_not_allowed`、`conflict`、`payload_too_large`、`unsupported_media_type`、`recent_auth_required`、
`rate_limited`、`service_unavailable`、`internal_error`、その他の `request_failed` です。

JSON成功応答、204、CSVストリームは別の契約です。CSV本文をJSON middlewareでbufferしません。
認証情報更新・snapshot restoreの応答喪失では、エラーだから未実行と推定して再送してはいけません。
