# Omni Money 利用ガイド（Desktop / Docker / TrueNAS）

このガイドは現行のDesktopとmulti-user serverを対象にしています。旧`AUTH_PASSWORD_HASH`、TOTP、`DB_PATH`、`DB_ENCRYPTION_KEY_FILE`、`compose.ai.yaml`を使う単一DB server手順は廃止され、設定されている場合は安全のため起動を拒否します。

## 1. Desktopアプリ

### インストールと起動

[GitHub Releases](https://github.com/ShiningWank0/Omni_Money/releases)からOS向けartifactを取得します。配布artifactは固定版SQLCipherを静的に組み込み、暗号化DBの作成、誤った鍵の拒否、平文headerとcanaryの不在、load extensionの無効化をCIでartifact自身に実行させてから公開します。

開発buildでも通常SQLiteや未固定のDesktop CLIへ切り替えないでください。各OS向けの `scripts/build-sqlcipher-*.sh` でSQLCipherを作成し、release workflowと同じ `libsqlite3,sqlite_omit_load_extension` build tagとCGO設定で起動します。SQLCipherが不足・不正な場合、Desktopは平文DBへfallbackせず起動を拒否します。[SQLCipher鍵の運用](sqlcipher-key-operations.md)も参照してください。

Desktop版はroleを持たない単一local vault運用です。初回起動でpasswordを設定し、1回だけ表示されるrecovery codeをpassword manager等へ保存します。取引データはOS標準のapplication data directory内のランダムなvault ID配下に、SQLCipher 4.18.0で暗号化して保存されます。

- macOS: `~/Library/Application Support/OmniMoney/vaults/<vault-id>/omni_money.db`
- Windows: `%APPDATA%/OmniMoney/vaults/<vault-id>/omni_money.db`
- Linux: `$XDG_DATA_HOME/OmniMoney/vaults/<vault-id>/omni_money.db`（未設定時は `~/.local/share/OmniMoney`）

旧versionのroot直下 `omni_money.db`がある場合は、password入力後に明示的な移行を行います。元DBとsnapshotを検証しながらSQLCipher vaultへ複写し、移行後のrecovery codeを保存したことを確認するまでvaultは利用できません。FileVault、BitLocker、LUKS等のfull-disk encryptionも、アプリの鍵を窃取できる別processや未暗号化の一時領域に対するdefense in depthとして有効にしてください。

### 基本操作

メイン画面から収入・支出を登録し、資金項目、検索、タグ、画像、取引リンクを管理できます。CSV出力前には平文であることを確認する警告が表示されます。CSV出力は常にv3で、画像、タグ階層・取引タグ、カード引落しリンク、ledger設定を正規化して含み、復元時に関連IDが安全に再採番されます。旧v1/v2 CSVはappendでインポートできますが、旧クライアント向けのtransactions-only v2出力は明示的な互換APIに限られ、完全バックアップではありません。拡張データを表現できない旧形式のreplaceは安全のため拒否されます。完全置換にはCSV v3を使用してください。v3 exportの末尾manifestには全record typeの件数とcanonical digestが含まれ、replaceは公式完全ヘッダーとmanifestが一致しない入力をDB変更前に拒否します。CSV v3のreplaceは全体を1つのtransactionで適用し、検証・画像処理・関連付けのどこで失敗しても元データへrollbackします。appendは既存の取引関連データ・ledger設定を保持し、CSV設定が既存値と競合する場合はatomicに中止します。既存リンクは自動削除されません。CSV入力はストリーミングraw CSVが512 MiB、解析済みテキストが64 MiBまでです。後方互換のWails/JSON文字列経路は64 MiBまでなので、完全バックアップにはDesktopのファイルダイアログまたはserverのraw CSV uploadを使用してください。必ず暗号化済みvolumeへ直接保存し、不要になったcopyを残さないでください。browserは保存完了や保存先の暗号化を検証できず、SSD上のfile削除も完全消去を保証しません。

### サブスクの自動登録

支出取引の詳細にある「サブスクとして登録」から、月額・年額、元の通貨と金額、基準日、次回支払日、資金項目、タイムゾーン、タグを確認して保存します。元の手動取引は変更しません。初回は当日以降、かつ元の取引の翌周期以降を指定します。定期収入と、取引を起点にしない新規ルールは初期版の対象外です。

メニューの「サブスク管理」で編集・停止・再開できます。停止しても過去の取引を削除しません。再開は編集画面で次回支払日を指定して保存します。停止中の分を無断で補完しません。周期や日付を変更するときも、処理済みの支払いの翌周期以降を指定します。過去の取引だけを編集してもルールは変わりません。

利用開始時、利用中は約60秒ごと、画面復帰時に、ルールのタイムゾーンで当日までの未登録分を補います。未起動・ロック中の分も次回利用時に補完します。31日などがない月は月末に寄せ、次の月には元の基準日に戻します。年額の2月29日は平年の2月末に寄せます。作成した取引は通常の残高・収支に含みます。取引を削除した支払回は自動で作り直しません。

「取引を表示」ではそのルールから作った取引だけを検索できます。資金項目、項目名・メモ、開始・終了日を併用し、100件ずつ移動できます。「カレンダー」は前月・翌月ボタンと年月欄で過去・未来の月へ移動できます。登録済みの支払いからは既存取引を開き、未来の予定からはルールを開きます。予定の閲覧で取引や残高を増やしません。

カレンダーの実績は元の支払予定日に配置し、表示内容には編集後の取引を使います。対応する処理記録は取引の日付を編集しても移動させません。表示可能な最終年9998年の最後の支払いは確定し、その次の周期を扱えないルールは停止します。

「候補」は直近5,000件の対象支出から、同じ資金項目・項目名・円金額で月または年ごとに3回続くものを提案します。日付差は基準日の前後3日以内です。自動登録分、リンク済み取引、銀行口座に分類した資金項目、既存ルールと重なる支払いは除外します。「内容を確認」から保存するまで自動登録を開始しません。「今後表示しない」は同じ項目・金額・周期の候補を再表示しません。

#### 外貨のサブスク

JPYに加え、USD・EUR・GBP・AUD・CAD・CHF・CNY・HKD・SGDを指定できます。円は整数、外貨は小数2桁まで入力できます。[FrankfurterのECB provider](https://frankfurter.dev/#rates-by-provider)を通じ、支払予定日以前の直近7日以内に公表された日次参照レートで円換算し、1円単位に四捨五入します。将来のレートを先に確定しません。取得失敗や範囲外の円換算額では、その支払回を登録せず再試行します。

外部へ送信するのは通貨と日付だけで、資金項目名・サブスク名・金額・メモ・認証情報は送信しません。取引の詳細には元の外貨金額と適用レート・公表日を残します。日次参照値にはカード会社の手数料や独自レートを含まないため、実際の請求額に合わせる場合は通常の取引編集で円金額を修正してください。修正しても元の換算履歴は残します。

外部からの応答は、JSONの項目・型・重複・通貨・日付・数値の桁数・本文サイズまで検証します。数値を含む文字列もレートとして受け入れません。不正な応答では、その支払回の取引・残高・次回支払日を変えず、取得失敗として再試行します。形式も範囲も正しい偽のレートを見抜く照合機能はありません。[検証条件と限界](development/manage-subscription.md#外部レート応答の検証)を参照してください。

2026-11-13時点で[Frankfurter公開APIは無料・APIキー不要](https://frankfurter.dev/#faq)です。濫用防止の速度制限があり、将来の無償提供や可用性を保証するものではありません。[ECBデータの無料利用条件](https://www.ecb.europa.eu/services/using-our-site/disclaimer/html/index.en.html)に従い出典を表示し、Frankfurter経由のレートで円換算した値であることを示します。

#### CSVとスナップショット

CSV v3にはサブスクのルール、処理済み支払回、表示の確認状態、外貨換算履歴を含みません。CSV appendでは手動取引として取り込み、既存ルールを保持します。CSV replaceでは既存ルールを停止し、削除取引の生成元と削除タグの設定を切り離します。ルールを再開する場合は設定・タグ・次回支払日を確認してください。手動取引やCSV取引と同じ支払いかどうかは、名前・金額から自動で判断しません。

サブスクを含めて復元する場合はschema 7の暗号化スナップショットかwhole-data-root backupを使用してください。復元で時点が戻ると、その後の支払回記録も戻ります。復元した時点で処理済みの支払いは重複させず、未処理分を次回利用時に補完します。

### Web / ServerでのCSVインポート

1. CSVファイルと「追加」または「置換」を選び、「プレビュー」を実行します。この時点ではデータを変更しません。
2. 新規・重複候補・競合候補の件数を確認します。既存取引と口座・日付・項目・種別・保存金額が一致し、メモも同じ場合は重複候補、メモが異なる場合は競合候補です。不正なCSVはエラーとして表示されます。候補は既存取引との比較であり、CSV内だけの重複検出や自動スキップは行いません。追加を実行すると候補を含む全行を取り込みます。
3. 置換の場合は削除対象の件数を確認してから、削除への同意にチェックを入れます。「インポート実行」で全体を適用します。

ファイルやモードの変更後は再プレビューが必要です。プレビュー後に対象データやCSVが変わった場合はHTTP 409で適用を中止します。表示内容を確認し、再プレビュー（置換では再同意）してから実行してください。その他の適用エラー後も再プレビューが必要です。プレビューは適用成功を保証するものではなく、設定の競合などによって適用時に中止することがあります。

Desktopでは従来どおり「インポート実行」でOSのファイル選択ダイアログを開きます。Desktopのプレビュー表示は対象外です。

## 2. Multi-user serverの安全モデル

serverは次の領域を分離します。

- control DB: user、role、invite/reset tokenのdigest、password検証情報、暗号化済みkey envelopeを保持するSQLCipher DB
- user vault: userごとの取引データを保持する独立SQLCipher DB
- control DB key: user vaultのDEKとは独立したowner-only secret
- recovery code: browserが生成し、userだけが保存するvault復旧用secret
- session: サーバーのメモリで管理し、再起動で失効する。control DBに永続化しない

Adminはuserの追加・無効化等を管理できますが、アプリのAdmin権限では他user vaultの中身を復号できません。パスキー用のサーバー管理envelopeが存在する場合、control鍵とcontrol DBを持つサーバー運用者にはそのVault鍵を復号可能です。serverのsnapshot APIも本人のrequest leaseに束縛されるため、application Admin/APIでも他user vaultの平文を列挙・復号・復元できません。ただし同じservice UID、host root/operator、差し替え可能なbinary、process memoryはtrust boundary内です。手動APIに加え、`core.Service`経由のledger mutation成功後にuser vault単位の自動snapshotを非同期作成します。自動処理はburstをcoalesceして30世代と容量上限を維持しますが、時刻schedule・retention policy・失敗通知を管理する製品UIはありません。snapshot restore後は全sessionが失効し、再ログインが必要です。

詳細は[server multi-vault security model](server-multi-vault.md)、[SQLCipher鍵の運用](sqlcipher-key-operations.md)、[保存時暗号化volumeの運用contract](at-rest-encryption.md)を参照してください。

旧single-user serverからの移行は[移行runbook](single-user-migration.md)に従い、元データの保全と隔離環境での予行演習を行ってください。

AIはDesktop/serverのproductionで提供していません。user-vault-bound AIはStage 4のplanned/unshipped設計であり、旧AI packageや追加portを運用へ持ち込まないでください。

## 3. Docker Composeでローカル確認

この節は、運用者が明示的にローカル検証する場合の手順です。開発時のDocker build・server E2Eは原則CI/CDで行います。

Mac + Colimaで公開済みDockerイメージを検証する場合は、[Mac + Colima の隔離検証手順](mac-colima-verification.md)を使用してください。以下の`sudo chown`とbind mountの準備例はネイティブLinux向けです。macOSでそのまま実行しないでください。

### Linux の bind mount 手順

#### 必要なもの

- Docker EngineとDocker Compose
- SQLCipherに加えて、host側data directoryを保護するLUKS2、ZFS native encryption等
- ownerだけが読めるcontrol keyとinitial Admin setup token
- [保存時暗号化volumeの運用contract](at-rest-encryption.md)に従ったattestation（運用日付の入力は不要）

準備例です。attestation JSONは実際の暗号化volumeを検証して作成し、例だけをcopyしないでください。

```bash
cp .env.example .env
umask 077
mkdir -p data secrets
test -s secrets/control-database.key || openssl rand -hex 32 > secrets/control-database.key
test -s secrets/initial-admin-setup.token || \
  openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n' > secrets/initial-admin-setup.token
chmod 600 secrets/control-database.key secrets/initial-admin-setup.token
```

既存のcontrol鍵を上書きしないでください。再実行時は既存の秘密ファイルと `.env` を維持し、必要な設定だけを変更します。

`.env`で少なくとも次を実環境に合わせます。

- `OMNI_DATA_DIR`
- `OMNI_AT_REST_ATTESTATION_FILE`
- `OMNI_UPDATE_ATTESTATION_FILE`（safe-updateを使う場合のroot-owned host attestation）
- `OMNI_CONTROL_DB_ENCRYPTION_KEY_FILE`
- `OMNI_INITIAL_ADMIN_SETUP_TOKEN_FILE`
- `ALLOWED_HOSTS`
- `PASSKEY_RP_ID`（Pangolinの公開FQDN、scheme/portなし）
- `PASSKEY_ORIGINS`（例: `https://money.example.com`）

Linuxのdata directoryはcontainer UID/GID `10001:10001`だけが書けるようにします。次の`sudo chown`はLinuxホスト用で、macOS/Colimaには適用しません。

```bash
sudo chown 10001:10001 data secrets/initial-admin-setup.token
sudo chown root:10001 secrets/control-database.key
sudo chown root:root secrets/omni_data_at_rest.json
sudo chmod 700 data
sudo chmod 440 secrets/control-database.key
sudo chmod 400 secrets/initial-admin-setup.token
sudo chmod 444 secrets/omni_data_at_rest.json
docker compose -f compose.yaml -f compose.bootstrap.yaml -f compose.local.yaml up -d --build
```

このLinux向けローカルoverrideは`127.0.0.1:4000`だけへHTTPをpublishします。ブラウザで`http://localhost:4000`を開きます。Mac + Colimaでは[専用手順](mac-colima-verification.md)に従い、公開済みimageとVM内のnamed volumeを使います。

パスキーのRP IDにはIPアドレスを指定できません。ローカルでは`PASSKEY_RP_ID=localhost`と`PASSKEY_ORIGINS=http://localhost:4000`を使用してください。明示設定がなく、RP IDの候補がloopback IPの場合は`localhost`を既定値とします。明示したRP IDやoriginは自動変換しません。

### 最初のAdminを作る

初回画面で次を入力します。

1. `secrets/initial-admin-setup.token`の内容
2. Adminのemail、表示名、十分に長いpassword
3. browserが生成するrecovery codeの保存確認

recovery codeはpassword manager等へ保存してください。serverは平文recovery codeを保存しません。初回作成時はpassword/recovery codeでVault鍵を開きます。パスキー登録後にサーバー管理envelopeが存在する場合は、control鍵とcontrol DBを持つ運用者にもVault鍵の復号が可能です。アプリにAdminによる代行復旧APIはありません。

最初のAdminを作成したらbootstrap overlayを外してcontainerを再作成します。起動確認後、host側setup tokenを安全にretireできます。

```bash
docker compose -f compose.yaml -f compose.local.yaml up -d --force-recreate
```

ログイン後、Adminはメニューの「サーバーユーザー管理」から次を実行できます。

- userのemailとroleを指定して、24時間有効の一度だけ表示されるinvite tokenを作る
- active userへ15分有効のpassword reset tokenを作る
- 自分以外のactive userを無効化し、そのuserのsessionと開いているvaultを失効させる
- disabled userを再有効化する、または最後のactive Adminを残す制約の下でroleを変更する
- invite/reset tokenの非secret metadataを一覧し、未使用tokenを取り消す

invite/reset tokenはURLへ入れず、安全な別経路で本人へ渡してください。本人は `/login?mode=invite` または
`/login?mode=reset` を開いてtokenを手動で貼り付けます。招待されたuserは自分のpasswordとrecovery codeを作り、
password resetにはAdminのtokenに加えて本人だけが保持する既存recovery codeが必要です。Admin UIが扱うのはaccount状態だけで、
他userの取引、vault path、password、recovery code、暗号鍵を表示または復号する機能はありません。

### パスキーの登録とログイン

各userはログイン後の「パスキー設定」で、passwordを再入力せずにパスキーを登録できます。Bitwardenなどの標準パスキー保存先を利用でき、PRF出力がない場合もパスキーだけでログインできます。パスキー保存先のロック解除や本人確認が表示されることはありますが、Omni Moneyのアカウントpasswordの追加入力は要求しません。既にサーバーへ登録済みの旧形式のパスキーは、通常のpasswordログインか、ログイン済み状態で「パスキー設定」を開いたときに自動移行します。以前の登録失敗で保存先にだけ残ったパスキーは、改めて登録してください。Vault鍵はサーバー管理の専用鍵で暗号化して保存します。この方式ではcontrol鍵とcontrol DBを持つサーバー運用者には復号可能ですが、アプリのAdmin権限で他userのVaultを開くAPIはありません。登録後もpassword認証は無効にならず、login画面と重要操作の再認証でpasswordまたはpasskeyを選べます。passkeyはPangolin経由のHTTPS originに紐付くため、`PASSKEY_RP_ID`や公開FQDNを変更すると既存passkeyは使えなくなります。passwordとrecovery codeは引き続き安全に保管してください。

### 認証情報の更新・失効

「認証情報の管理」では、現在のpasswordを確認してpasswordまたはrecovery codeを更新できます。どちらもVaultのDEKは変えず、新しいenvelopeへatomicに置き換えます。serverでの更新後は全sessionと開いているVaultを失効させるため、再ログインが必要です。password変更時は既存passkeyを残すか全失効するかを明示的に選びます。「パスキー設定」で個別または一括失効した場合も全端末からログアウトします。「全端末からログアウト」はpasswordとpasskeyを変更せずsessionだけを終了します。

### 取引の保存とログアウト

取引エディターの新規作成・更新で「保存」を押すと、画像を含む入力をサーバーの暗号化保管庫へ先に受け付け、その後に取引一覧へ反映します。保存操作ごとにUUIDを生成し、再送では同じUUIDと同じ入力を使用します。同じ保存操作の再送は一度だけ反映されますが、別の操作で同じ内容の取引を登録することはできます。ログアウトを押すと私的な画面データを直ちに消去します。送信・受付確認とセッション失効まで待ち、画像処理やスナップショットの完了は待ちません。受付済みの処理はログアウト後も続き、再ログインして確認できます。サーバーが処理途中で再起動した場合は、そのユーザーの次回ログインで再開します。直後の再ログインは同じVaultを安全に再利用し、古い応答が新しい画面へ戻らないようにします。CSVインポートや取引削除等はこの非同期受付の対象外です。

通常の保存操作では取引反映まで待ってフォームを閉じます。ログアウト時にはその完了を待ちません。処理中の取引や保存に失敗した入力は、ログイン後の画面に通知します。失敗した入力と画像は、本人が通知の削除を確認するまで暗号化して保管します。通知には日付・資金項目・項目・金額・メモを表示し、画像本文は取得しません。通知削除は保管した入力・画像を削除しますが、再送を二重反映しないためのID/hash記録は残します。通信障害で受付を確認できない場合はログアウト完了として遷移せず、画面を保護して再試行を表示します。受付前の入力はブラウザのメモリにあるため、送信未確認のままリロードやタブを閉じる操作は避けてください。

通常HTTPリクエスト全体はBase64込みで10 MiBまで、画面の画像原データは合計7 MiBまでです。保存queue内部の上限は1入力32 MiB、未確認の失敗入力を含め32件/合計128 MiBであり、HTTP上限とは別です。混雑時は受付を拒否するため、入力を保持して再試行してください。

### 停止、再開、安全な更新

```bash
docker compose -f compose.yaml -f compose.local.yaml down
docker compose -f compose.yaml -f compose.local.yaml up -d
docker compose -f compose.yaml -f compose.local.yaml logs --tail=200 omni-money
```

`down -v`は使用しないでください。bind mountのdataを削除しなくても、control key、recovery code、暗号化volumeの復旧情報を失うと復号できません。

Pangolin/TrueNAS本番のversion更新は、通常の`up --build`ではなく、digest固定したimageを`./scripts/safe-update.sh`へ渡します。tagは可変のため受け付けません。digestは `./scripts/resolve-image-digest.sh --verify --repo ShiningWank0/Omni_Money <image:tag>` でprovenanceを検証して解決します。data migrationはtransactionalに実行され、scriptは停止後のoffline checkpointを検証してからcandidateをingressに接続します。candidateがhealthyになる前に失敗した場合だけ、旧dataと旧imageへrollbackします。要件と復旧時の挙動は[安全な更新と限定ロールバック](safe-update.md)を参照してください。

## 4. Pangolin / TrueNAS

base `compose.yaml`はPangolin/Newt用で、hostへportをpublishしません。初回だけ`compose.bootstrap.yaml`を重ね、Admin作成後はbaseだけで再作成します。Pangolin targetは`http://omni-money:4000`です。

- Omni MoneyとNewtだけを専用internal Docker networkへ参加させる
- Newtを固定IPへ割り当て、その`/32`だけを`TRUSTED_PROXIES`に設定する
- `ALLOWED_HOSTS`を公開FQDNと完全一致させる
- TLSはPangolin edgeで終端し、Omni Moneyを直接Internetへpublishしない
- TrueNAS datasetは暗号化し、container UID/GID `10001:10001`だけに書込みACLを与える
- Privileged mode、host network、`chmod 777`を使わない
- AI用port 4001や`compose.ai.yaml`を重ねない

TrueNAS Custom Appにはrepositoryの`compose.yaml`と同等のservice、secret、network制約を設定します。data root、control key、setup token、attestationは別々のmountとし、control keyやsetup tokenをdata root内へ置かないでください。

TrueNAS 25.10.7のApps画面へWeb版と同じアイコンを登録する場合は、[Custom Appアイコンの登録手順](truenas-app-icon.md)を参照してください。Compose YAMLとは別のmetadata設定です。

## 5. Backupと復旧試験

snapshot単体はDR setではありません。次の全てを別々の安全な場所へbackupします。

- control DB（`control/omni_control.db`）と独立したcontrol DB key
- 各user vault（`vaults/<vault-id>/ledger.db`）
- 各user vaultの暗号化snapshot
- 暗号化volumeのkey/recovery material、attestation、復旧・更新手順
- 各userが保持するrecovery code

CSV v3には保存queue（処理中・失敗入力と再送防止記録）を含みません。schema version 7のVault DBを含むsnapshot・whole-server backupにはこれらの記録も含まれます。過去のsnapshotへ復元すると、その後の取引と受付記録も巻き戻ります。サーバー再起動時のpendingは本人がVaultを解錠した後に再開します。

backup取得だけでは不十分です。本番とは隔離した環境で定期的に復旧し、control DB/key、vault/snapshot、volume recovery material、recovery codeが揃ってcontrol DBが開くこと、user本人のrecovery codeで対象vaultだけが開くこと、別userやAdminからは開けないことを確認します。元snapshotは変更しません。

server全体の冷間backupは `sudo bash scripts/backup-data-root.sh` で取得できます（service停止中のdata root全体tar、member/sha256/平文header検査、manifest付与、`--verify` モード付き）。取得手順、archiveと鍵materialの分離保管、restore手順、復元演習の詳細は [Disaster Recovery runbook](disaster-recovery.md) を参照してください。

## 6. よくある起動エラー

| 症状 | 確認箇所 |
| --- | --- |
| `CONTROL_DB_PATH is required` | 現行のserver環境変数を設定しているか |
| legacy settingの拒否 | `AUTH_PASSWORD_HASH`、TOTP、`DB_PATH`、`DB_ENCRYPTION_KEY_FILE`を削除する |
| AI settingの拒否 | `AI_*`環境変数と`compose.ai.yaml`を削除する |
| secret permission/ownerの拒否 | symlinkでない通常file、信頼できるowner、group/other非公開か |
| data-at-rest attestationの拒否 | data root、key ID、verification/restore/rotation時刻と有効期限を再確認する |
| Host拒否 | browserのHostと`ALLOWED_HOSTS`を完全一致させる |
| proxy/HTTPS拒否 | Newtの固定IP、`TRUSTED_PROXIES`、TLS終端、forwarded headersを確認する |
| setup画面が出ない | control DBに既に最初のAdminが存在しないか確認する。DBを消してやり直さない |

起動失敗時も、DBやsecretを削除して初期化し直さないでください。まずlogsと設定を確認し、必要ならbackup copyを使って隔離環境で復旧します。

## 7. 開発時の確認

変更範囲に応じてGoの関連package test、Frontendのunit/component test・buildを選びます。固定版と実行例は [Frontend README](../frontend/README.md)、配布条件は [Desktopビルド資材](../build/README.md)、作業時の許可・負荷管理は [AGENT_WORKFLOW.md](../AGENT_WORKFLOW.md) を参照してください。

server/Desktopの本番暗号化とDocker build・server/browser E2EはGitHub Actionsが固定SQLCipherと同じbuild tags/CGO設定で検証します。ローカルDockerやSQLCipher source buildは明示的に依頼された場合だけ実行します。ドキュメントだけの変更にコードbuildは不要です。
