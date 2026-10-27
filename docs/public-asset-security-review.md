# 公開アイコン・静的ファイル配信の安全性調査

調査日: 2026-10-27。対象は Omni Money の production multi-user router、legacy router、公開ファイル、共通middleware、Docker配備設定です。`/`、`/login`、`/favicon.ico` へのアクセスが見られるという報告を受け、同じ配信経路にある `/assets/` も確認しました。

## 結論と確認できる範囲

アイコンへのHTTPアクセスから、画像のアップロード、外部URL取得、サーバー側のSVG/XML解析、コマンド実行へ進む処理はありません。現行faviconは配布物に含む固定SVGで、script、イベントハンドラー、foreignObject、外部参照、DOCTYPE/ENTITYは含みません。アイコン取得だけでコードが実行される経路や、認証済みAPIへ到達する経路は、今回のコード調査・再現テストでは確認していません。

ただし、従来の静的配信は `http.FileServer(http.Dir("frontend/dist"))` でした。この構成では、公開directory内に置かれたsymlinkが公開root外のファイルを指すと、その内容まで匿名で読み出せます。また、directory一覧や、誤って配置された隠しファイル等も配信対象になります。[Goのhttp.Dirの仕様](https://pkg.go.dev/net/http#Dir)にもこの性質が明記されています。

これは「匿名のリクエストだけでsymlinkを作成できる」という問題ではありません。実害には、ビルド成果物・配備設定への混入や公開rootの書換えなど、別の条件が必要です。利用者が提示した構成はroot filesystemがread-onlyで、永続dataとsecretは公開rootから分離されています。現在の本番環境にそのようなリンクが存在することや、侵入が成功したことは確認していません。

## 確認した境界

| 対象 | 調査結果 |
| --- | --- |
| `/`、`/login` | 固定のfrontend HTML配信。金融API・管理APIは別routeで認証を要求する |
| favicon | 固定ファイルの読み出し。queryやrequest bodyを画像として解析しない |
| パストラバーサル | `..`、percent encoding、encoded slash、backslash、APIを指す変形パスをテスト。公開root外の読み出しとAPI認証迂回を拒否する |
| symlink | 旧処理でroot外のテスト用ファイルがHTTP 200で読み出せることを再現。修正後は404 |
| directory・隠しファイル | 一覧、`.env`、隠しdirectory、debug text、source map、HTML/PHP等を公開対象外へ制限 |
| HTTP method | 静的配信はGET/HEADだけを許可。POST/PUT/PATCH/DELETEは配信handlerで405。実際のrouterでは先にCSRF等の制限が働く場合もある |
| MIMEとブラウザ | 配布ファイルの種類からMIMEを明示。共通のnosniff、CSP、frame制限を維持する |
| 通信・負荷 | 静的配信は外部へ通信しない。既存のIP単位レート制限とHTTPのread/write timeoutを通過する。分散した大量アクセスすべてを防ぐ保証ではない |
| 取引画像 | faviconとは別の認証済みAPIと画像検証処理を使う。今回、取引画像の機能や認証条件は変更しない |

## 修正

1. 両routerが同じ制限付きfrontend配信handlerを使用する。
2. HTMLは明示した入口だけ、assetは許可した種類の通常ファイルだけを配信する。未登録パスは404とする。
3. [os.OpenInRoot](https://pkg.go.dev/os#OpenInRoot)でファイルを開き、symlinkの解決も公開root内へ閉じ込める。事前の文字列検査だけに依存しない。
4. GET/HEAD、Range、条件付きGETを維持し、ファイルの書込み・実行機能は追加しない。

固定SVGはブラウザの画像として表示するものです。[SVGを画像として扱う場合のブラウザ制約](https://developer.mozilla.org/en-US/docs/Web/SVG/Guides/SVG_as_an_image)と、SVGを直接documentとして開く場合は区別します。将来、第三者のSVGアップロードやアイコンURLの取得・変換を追加する場合には、今回の結論をそのまま適用できません。

## 検証と制約

ローカルでは、秘密情報を含まない一時directoryと外部ファイルへのsymlinkを使って旧処理の読み出しを再現し、修正後の拒否、通常のHTML/JS/CSS/SVG、HEAD、Range、条件付きGET、production middleware下の認証・headerを検証します。関連テストは `go test ./backend/api ./backend/middleware` で実行できます。SQLCipherの配布構成と実サーバーE2EはCIで検証します。

PangolinおよびTrueNAS本体の実機検査、アクセスログや実際の通信内容の解析、侵入後の調査は行っていません。`/favicon.ico`等へのアクセス頻度・パスだけでは、通常のブラウザ取得、探索bot、攻撃payload、侵入成功の区別はできません。今回の修正は確認できた配信境界の問題に対するもので、侵入発生を断定するものではありません。
