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


Frontendは `ApiError` の `message/status/code/retryable` で失敗を扱います。HTTPエラー、通信失敗、成功応答のschema違反を区別します。
`expectJSON` はendpointの最小schemaを検証し、一覧で明示的に許容する旧Goのnull sliceだけを空配列に正規化します。
`expectVoid` は204またはendpointで定義したJSON成功応答を受理します。keepaliveは204のみ、CSVとAI relayはraw Responseのまま扱います。
429/502/503/504や通信障害のretryableは自動再送の許可ではありません。再認証428は従来どおり1回だけ再送します。
認証情報更新の4xx拒否だけをdefinitiveResponseとして扱い、5xx・通信断・壊れた成功応答は適用済みの可能性を残します。
