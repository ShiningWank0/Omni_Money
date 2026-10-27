# 公開アイコン・静的ファイル配信の安全性調査

調査日: 2026-10-27。対象は Omni Money の production multi-user router、legacy router、公開ファイル、共通middleware、Docker配備設定です。`/`、`/login`、`/favicon.ico` へのアクセスが見られるという報告を受け、同じ配信経路にある `/assets/` も確認しました。

## 結論と確認できる範囲

アイコンへのHTTPアクセスから、画像のアップロード、外部URL取得、サーバー側のSVG/XML解析、コマンド実行へ進む処理はありません。現行faviconは配布物に含む固定SVGで、script、イベントハンドラー、foreignObject、外部参照、DOCTYPE/ENTITYは含みません。アイコン取得だけでコードが実行される経路や、認証済みAPIへ到達する経路は、今回のコード調査・再現テストでは確認していません。

ただし、従来の静的配信は `http.FileServer(http.Dir("frontend/dist"))` でした。この構成では、公開directory内に置かれたsymlinkが公開root外のファイルを指すと、その内容まで匿名で読み出せます。また、directory一覧や、誤って配置された隠しファイル等も配信対象になります。[Goのhttp.Dirの仕様](https://pkg.go.dev/net/http#Dir)にもこの性質が明記されています。

これは「匿名のリクエストだけでsymlinkを作成できる」という問題ではありません。実害には、ビルド成果物・配備設定への混入や公開rootの書換えなど、別の条件が必要です。利用者が提示した構成はroot filesystemがread-onlyで、永続dataとsecretは公開rootから分離されています。現在の本番環境にそのようなリンクが存在することや、侵入が成功したことは確認していません。

公開root内への相対リンクも検証しました。root外への移動だけを禁止する方式では、`assets/icon.svg` が `../.env` を指していると、root内に混入した隠しファイルを読み出せます。そのため、現在の修正ではroot自身・中間directory・最終fileのリンクを開く時点で拒否します。通常のHTML・画像URLやSVG内のfragment参照はOSのsymlinkとは別で、この制限の対象ではありません。

## 確認した境界

| 対象 | 調査結果 |
| --- | --- |
| `/`、`/login` | 固定のfrontend HTML配信。金融API・管理APIは別routeで認証を要求する |
| favicon | 固定ファイルの読み出し。queryやrequest bodyを画像として解析しない |
| パストラバーサル | `..`、percent encoding、encoded slash、backslash、APIを指す変形パスをテスト。公開root外の読み出しとAPI認証迂回を拒否する |
| symlink | root内の隠しファイルへの相対リンク、root外へのリンク、directory・root自体のリンクを拒否。正常ファイルとの差し替えも回帰テストで検証 |
| directory・隠しファイル | 一覧、`.env`、隠しdirectory、debug text、source map、HTML/PHP等を公開対象外へ制限 |
| HTTP method | 静的配信はGET/HEADだけを許可。POST/PUT/PATCH/DELETEは配信handlerで405。実際のrouterでは先にCSRF等の制限が働く場合もある |
| MIMEとブラウザ | 配布ファイルの種類からMIMEを明示。共通のnosniff、CSP、frame制限を維持する |
| 通信・負荷 | 静的配信は外部へ通信しない。既存のIP単位レート制限はAPIのみが対象で、静的配信には適用されない。HTTPのread/write timeoutは維持する。大量アクセスの可用性対策を完了したという意味ではない |
| 取引画像 | faviconとは別の認証済みAPIと画像検証処理を使う。今回、取引画像の機能や認証条件は変更しない |

## 修正

1. 両routerが同じ制限付きfrontend配信handlerを使用する。
2. HTMLは明示した入口だけ、assetは許可した種類の通常ファイルだけを配信する。対象外のパスは404とする。
3. `backend/publicassets.Open`で、固定したdirectory handleを基準に各componentを開く。Unixでは各段階の`openat`へ`O_NOFOLLOW`、Windowsでは相対handleとreparse拒否を適用する。事前の`Lstat`と後続の通常openを組み合わせる方式にはしない。operatorが選ぶ公開rootより上の親directoryは信頼するが、公開rootそのものと内部のcomponentはリンクを許可しない。
4. GET/HEAD、Range、条件付きGETを維持し、ファイルの書込み・実行機能は追加しない。

## 配布物への混入検査

Viteのbundle出力から、公開予定の相対pathとSHA-256の一覧を`frontend/public-assets-manifest.json`へ生成します。公開directoryの中に置かれた全fileをそのまま許可リストへ取り込む方式ではありません。この一覧は`dist`の外に保存し、HTTPでは配信しません。

ビルド時の実directoryと一覧を照合し、余分なfile/directory、隠しファイル、DB・鍵・backup・source mapなどの未許可形式、symlink、内容の差し替え・欠落を拒否します。許可された画像等に偽装された場合でも、認識可能なprivate key headerや平文SQLite headerは検出します。正当な追加assetはJS/CSS等からimportしてbundle出力に含める必要があります。未登録の`public/`コピーを暗黙に許可しません。

Dockerのruntime stageでは、すべてのCOPY後の最終RUNで同じ一覧を使って実際の`/app/frontend/dist`を再検査します。検査用Go binaryはBuildKitの一時bind mountで使い、runtimeへ常設しません。Node.jsもruntimeには追加しません。manifestはfrontend builderからのみコピーし、手元の古いmanifestはDocker contextに含めません。

この検査は任意の秘密情報を意味から完全判定するものではありません。許可したJS等へ開発者が直接埋め込んだ任意のtoken、正常に見える悪意あるbundle、侵害されたビルド環境とmanifestの同時改変まで保証しません。既存のsecret scanning・配布provenance・read-only配備・秘密情報の別mountも引き続き必要です。

固定SVGはブラウザの画像として表示するものです。[SVGを画像として扱う場合のブラウザ制約](https://developer.mozilla.org/en-US/docs/Web/SVG/Guides/SVG_as_an_image)と、SVGを直接documentとして開く場合は区別します。将来、第三者のSVGアップロードやアイコンURLの取得・変換を追加する場合には、今回の結論をそのまま適用できません。

## 検証と制約

ローカルでは、秘密情報を含まない一時directoryとリンクを使い、通常配信、リンク拒否、差し替え、混入file・偽装内容・hash差分を検証します。`npm test`と`npm run build`、`go test ./backend/publicassets ./backend/api ./backend/middleware`、`go run ./cmd/verify-public-assets frontend/dist frontend/public-assets-manifest.json`で確認できます。Windowsでも公開file境界のテストをCIで実行します。SQLCipherの配布構成、Docker内の最終検査、実サーバーE2EはCIで検証します。

PangolinおよびTrueNAS本体の実機検査、アクセスログや実際の通信内容の解析、侵入後の調査は行っていません。`/favicon.ico`等へのアクセス頻度・パスだけでは、通常のブラウザ取得、探索bot、攻撃payload、侵入成功の区別はできません。今回の修正は確認できた配信境界の問題に対するもので、侵入発生を断定するものではありません。
