# 公開サーバーの脆弱性検査

## CI/CDの検査範囲

`.github/workflows/ci.yml` は全PR、mainへのpush、毎日01:30・14:30 UTC（日本時間10:30・23:30）のschedule、および`workflow_dispatch`による手動実行に対応する。定期実行の設定はmainへの反映後に有効になる。GitHub側の負荷で定刻より遅れる場合がある。

修正後のmainで定期検査をすぐ確認するには、GitHubのActionsで`CI`を開き、`Run workflow`でbranchに`main`を選んで実行する。全検査、safe-update全件、レポート集約、Issue報告を実行する。ほかのbranchの手動実行では検査とartifact保存を行うが、Issue作成・追記は行わない。過去の実行の再実行は元のコミットを使うため、マージ済み修正の確認には新しい手動実行を使う。

| 検査 | 対象と失敗条件 |
| --- | --- |
| govulncheck | Go標準ライブラリ・依存関係の既知の脆弱性。呼び出し経路に到達する問題で失敗。Desktopとserverの両ビルド条件を検査 |
| gosec | コードの危険な実装パターン。medium以上で失敗。Desktopとserverの両ビルド条件を検査 |
| Parser fuzz | CSVのストリーム上限、認証JSONの重複キー、JPEG/PNG/GIF/WebPコンテナ境界。PRでは固定seed corpusを既存Go試験で実行。定期・手動では各120秒の入力変異試験を実行し、panic・不変条件違反・実行失敗で失敗 |
| npm audit | frontendの依存関係。high以上で失敗 |
| Trivy | Omni MoneyイメージのOS・ライブラリ。修正版のあるHIGH/CRITICALで失敗。`ignore-unfixed: true`のため未修正の問題は除外。Docker Releaseでは各アーキテクチャを**タグなしのdigestとしてpushしてから**スキャンし、両方成功した後にのみrelease tagを公開する |

Goのgovulncheck/gosec等はPR、定期schedule、手動実行で実行し、main pushではGo checksジョブを省略する。各jobの条件は `ci.yml` を正とする。Docker Releaseは各アーキテクチャのTrivyを公開前に実行し、次の順序を守る: タグなしdigest push → アーキテクチャ毎のTrivyスキャン → staging index作成 → build provenance attestation付与 → `gh attestation verify`（署名workflow・source ref・GitHub-hosted runnerを固定）→ version/latestタグ公開。スキャン・attestation・検証のいずれかが失敗するとrelease tagは作られない。配備側は`scripts/resolve-image-digest.sh --verify --repo OWNER/REPO`でprovenance検証を必須とする。

`ci.yml`の`workflow_dispatch`で`release_rehearsal`を有効にすると、共通OSSチェック後に`release-docker.yml`を**リハーサル**実行できる。releaseと同じビルド・スキャン・attestation・検証を行い、version/latestタグは公開しない（`rehearsal-staging`タグのみ更新）。VERSION変更を伴う本番Releaseの前に、GitHub ActionsのCI画面のRun workflowで対象branchを選び、`release_rehearsal`を有効にして、経路全体を検証する。CI成功は、未修正の既知問題や静的解析で分からない設計上の問題がないことを保証しない。Pangolin、Newt、Gerbil、Traefik、Badger、VPS/TrueNASそのものは別配備のため、このCIの検査対象ではない。

Parser fuzzは追加の定期・手動専用matrixで3対象を最大2job並列、各jobのfuzz workerを1に制限する。入力はCSV 16 KiB、JSON 8 KiB、画像コンテナ64 KiBを上限とし、DB・本番データ・外部サービスには接続しない。画像はコンテナ検証のみで、任意画像のピクセル展開は行わない。各対象の試験時間は120秒、失敗入力の縮小は10秒、Go試験全体は4分、子プロセスは6分、jobは10分で打ち切る。PR/pushではこのmatrixを省略するが、PRの既存`go test ./...`は固定seedを実行する。必須チェック名`Go tests`は定期・手動のfuzz成功、それ以外の省略を明示検査する。

失敗したfuzz入力は`parser-corpus-*` artifact、結果は既存の`security-*` artifactで7日間保存する。再現入力を公開Issue本文に転載せず、実行ログへのリンクのみ通知する。失敗corpusを取得して該当packageの`testdata/fuzz`へ配置した後、そのpackageで`go test -run='Fuzz対象名/ハッシュ'`を実行して再現し、修正時はcorpusを回帰試験として追加する。対象名の誤りで試験が実行されない場合を成功扱いしないよう、GoのJSONイベントで対象の開始・変異実行数・対象とpackage両方の成功を確認する。時間制限内のfuzz成功は、すべての入力について安全であることの証明ではない。

## 定期・手動検査のIssue報告

このリポジトリのmainでの定期・手動実行では検査のJSON結果からIssueを自動作成する。GitHubが自動発行する`GITHUB_TOKEN`を使い、Issue報告ジョブだけに`issues: write`を与える。追加Secretは不要。PR・push・fork・main以外のbranchではIssue作成を実行しない。

### Issue作成のルール

- govulncheckは到達可能なsymbol、npmはhigh以上、Trivyは修正版のあるHIGH/CRITICALを対象にする。gosecのmedium以上は「静的解析」と明記し、既知CVEの検出と区別する。
- スキャナーと対象パッケージで同一性を判定し、同じパッケージの複数advisory（識別子）は1つのIssueへまとめて本文に併記する。gosecはルールとファイル・行で判定する。Desktop/serverで重なるGoの問題は1件にまとめ、検査条件を併記する。
- 対象、深刻度、検出版または影響範囲、修正版・更新候補、実行ログへのリンクを記録する。GoのDBが深刻度を提供しない場合はUNSPECIFIEDと明記する。
- このワークフローが自動作成した同じ問題のOpen Issueには、内容が変わったときだけコメントする。実行日・run IDの変化だけでは追記しない。既存本文や人間のコメントを上書きしない。手動で作成したIssueは自動照合の対象外。
- ClosedのIssueを再オープンしない。閉じた問題を再検出した場合は新しいIssueを作る。検出が消えても自動クローズはしないので、対応PR・人間の確認で閉じる。
- 通信障害、不完全なJSON、先行ステップ失敗、artifact欠落・ダウンロードや整合性検証の失敗は「検査エラー」のIssueにする。脆弱性がないという判定にはしない。Parser fuzzの失敗・未完了も「検査エラー」として通知する（既知CVEの検出とは区別する）。一般テストの失敗はActionsの実行結果で確認する。
- Issue報告のAPIエラーもCIを失敗させる。1回の新規作成・追記は合計25件までとし、超過時は失敗を表示する。残件は次回定期実行、新しい手動実行、またはその実行の再実行で処理する。

Issue報告ジョブは同じ実行・同じattemptのartifactだけを読む。過去のattemptの結果を混在させないため、再試行時は **Re-run all jobs** を選ぶ（失敗ジョブだけを再試行すると、そのattemptで未実行の検査が欠落扱いになる）。スキャナー側は読み取り権限のみで、Issue書き込み用トークンはスキャナーへ渡さない。Issue報告ジョブはレポートをデータとして解釈し、外部の説明文からコマンド・URLを実行しない。公開Issueへソースコード断片や生のstderrは転載しない。レポートの保存期間は7日。

同じbranchの定期・手動実行は同じconcurrency groupで直列化し、mainへのpushや新しい定期・手動実行で進行中の検査をキャンセルしない。公開リポジトリは60日間活動がないとscheduleが自動無効化されるため、Actions画面で有効状態を確認する。

ローカル検証は`node --test scripts/security-monitoring.test.mjs`。架空の検出結果と模擬GitHub APIを使用する。

## 依存関係の自動更新とリポジトリ設定

`.github/dependabot.yml` が、Go module、frontendのnpm、workflowのSHA pin、Dockerfileのbase image digestを毎週の更新PRとして提案する。既知脆弱性の検出（npm audit・govulncheck・Trivy）とは別の仕組みで、更新PRも通常のCIとレビューを通る。

次の3つはGitHubリポジトリの設定であり、CIやリポジトリ内のファイルでは強制できない。管理者が有効化し、GitHubのSettings/Actions画面で状態を確認する。

- **Dependabot alerts / security updates**: 既知脆弱性に対する修正PRを自動作成する
- **Secret scanning / push protection**: 誤ってcommitした鍵やtokenを検出し、push時に拒否する
- **Code scanning (CodeQL default setup)**: GoとJavaScriptのデータフロー解析を継続実行する

無効化されていてもCIは成功するため、定期実行のたびにこの節の設定が有効か確認する。

## Wailsの開発・ビルド用Echo

EchoのIPヘッダー偽装による回数制限回避（CVE-2026-118653）と、リバースプロキシによる偽の`X-Real-IP`転送（CVE-2026-118651）は、公式のv4.16.3で修正されている。[公式の修正説明](https://pkg.go.dev/github.com/labstack/echo/v4@v4.16.3#readme-client-ip-security-migration-since-v4-16-3)

Wails本体は公式版のまま、`go.mod`でEchoの修正版を指定する。Echoを使うWailsの開発用アプリには、このプロジェクトの依存指定が適用される。別途`go install .../cmd/wails@バージョン`で導入するCLI自体はプロジェクトの依存指定を使わないが、現行CLIにはEchoが含まれない。DesktopのCIでは導入したCLIの`go version -m`も読み、Wailsが`go.mod`の選択版と一致すること、独自置換がないことを確認する。将来CLIがEchoを含む場合は、その版もプロジェクトの選択版と一致しなければビルドへ進めない。

サーバーのHTTP処理はEchoを使わず、信頼するプロキシだけから転送情報を受け取り、処理後に転送ヘッダーを除去する。直接接続と信頼プロキシ経由の両方について、偽のIPを変えてもログインの回数制限を回避できないことを回帰テストで確認する。配布対象への実際の組み込みはOSSの6構成の依存検査で判定し、CLI依存を理由に配布用ライセンス画面へ追加しない。

`backend/middleware/echo_security_test.go`は選択されたEcho自体についても2件の挙動を検証する。テストだけでEchoのmiddlewareをimportするため、`go.mod`上のEchoは直接依存になるが、productionへの組み込みを意味しない。回数制限とプロキシの検証には記録用のHTTP request/responseと代替transportを使い、待受ポートは開かない。

2026年11月6日の追加検査では、Goの脆弱性DBはv4.16.3にもGO-2026-6872／GO-2026-6875を報告し、修正版を未登録としている。公式の修正説明と実際の回帰試験の結果を併せて判断し、スキャナーの検出を無視する設定は加えない。

CIの`Go checks`では、固定SQLCipherと`dev,desktop,webkit2_41`を使ってOmni Money本体もビルドし、Xvfb・D-Bus上で実際のWailsを起動する。`scripts/wails-dev-integration.mjs`はビルド情報から、選択されたWailsとEchoの組み込み版、devタグ、CGOを照合する。productionやbindings用バイナリでは成功しない。ローカル起動を防ぐため、Linux・GitHub Actions・仮想画面を必須にする。

実際のfrontend生成物のコピーをEchoから配信し、テスト固有の確認ファイルで起動したインスタンスを識別する。ChromiumからWailsの実WebSocketを通して`GetDesktopVaultStatus`を呼び、未設定・ロック状態の応答、Desktop初期画面とOSS画面の表示、`runtime.Quit`による正常終了を検証する。通信・Goバインディング・画面をモックしない。HOMEは変更せず、XDGのデータ・設定・キャッシュをCI専用の一時ディレクトリへ向ける。通常終了・失敗時ともプロセス群とブラウザを終了させ、一時データを削除する。応答と終了には期限を設け、失敗をskipや成功に変換しない。

`wails-echo-dev-integration` artifactにはビルド情報・アプリログ・通信数と終了／後片付けの判定結果を保存し、保管庫やブラウザプロファイルは含めない。この結合試験はLinuxのWails開発用HTTP/IPC経路を対象とし、各OSのネイティブ画面を操作する試験の代替ではない。

## パスキーログインの情報漏えい対策と限界

現行Frontendのパスキーログインは `/api/auth/passkeys/discover/begin` と `/finish` を使用する。メール入力なしのdiscoverable ceremonyを開始し、開始応答にuser別のcredential候補を含めない。選択されたcredentialの署名、challenge、RP/origin、user handle、user verification、期限、一回限りの利用を検証してから本人のVaultを開く。標準パスキーはserver-custody envelopeを使用し、PRF出力を要求しない。旧形式の互換処理と自動移行は [server安全モデル](server-multi-vault.md#passkey-authentication) を参照。

### メール指定の互換login API

有効なメール形式について、アカウント不存在・無効化・パスキー未登録でも、メール指定の公開login/beginは架空の候補を含む認証開始応答を返す。候補数は登録上限の10件に揃え、transport情報を省略する。架空IDとPRF saltは目的分離した永続秘密鍵と正規化メールから決定的に生成する。login/beginは関数全体に、login/finishは全ての失敗応答（架空ceremonyを含む）に100msの応答時間下限を設け、成功時のみ下限を適用しない。秘密鍵は既存control DB鍵からHKDFで導出するため、追加設定・DB移行は不要。

架空候補はサーバー側の認証許可リストに入れず、架空アカウントのceremonyはfinishで拒否する。本物の署名、challenge、RP/origin、user verification、本人のcredential、期限、一回限りの利用を引き続き検証する。検証後は標準パスキーのcustody envelopeを開き、旧PRF envelopeの互換経路ではPRF出力でVault鍵を開く。

この変更はHTTP成否・候補数・架空値の使い回し方からの直接的な判別を抑える。既存の非discoverableパスキーとの互換性のため、実credential IDとその長さ、PRF saltをブラウザーへ渡す方式は維持する。既知IDとの照合、認証器固有の形式・長さの統計的推測、登録変更前後やcontrol DB鍵のローテーション前後の比較、負荷時の時間差まで隠すものではない。この限界はメール指定の互換APIに対するもので、現行Frontendのdiscoverable開始応答にはuser別のallowCredentialsを含めない。

[WebAuthnのUsername Enumeration / Privacy leak via credential IDs](https://www.w3.org/TR/webauthn/#sctn-username-enumeration) に沿った互換性を維持する緩和策として扱う。
