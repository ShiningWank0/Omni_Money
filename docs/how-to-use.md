# Omni Money 利用ガイド（Desktop / Docker / TrueNAS）

このガイドは現在のmulti-user server構成を対象にしています。旧`AUTH_PASSWORD_HASH`、TOTP、`DB_PATH`、`DB_ENCRYPTION_KEY_FILE`、`compose.ai.yaml`を使う単一DB server手順は廃止され、設定されている場合は安全のため起動を拒否します。

## 1. Desktopアプリ

### インストールと起動

ReleasesからOS向けartifactを取得します。配布artifactは固定版SQLCipherを静的に組み込み、暗号化DBの作成、誤った鍵の拒否、平文headerとcanaryの不在、load extensionの無効化をCIでartifact自身に実行させてから公開します。

開発buildでも、通常のSQLiteを組み込むtagなしの `wails build` / `wails dev` は使わないでください。各OS向けの `scripts/build-sqlcipher-*.sh` でSQLCipherを作成し、release workflowと同じ `libsqlite3,sqlite_omit_load_extension` build tagとCGO設定で起動します。SQLCipherが不足・不正な場合、Desktopは平文DBへfallbackせず起動を拒否します。

Desktop版は単一ユーザー運用です。初回起動でローカルAdminのpasswordを設定し、1回だけ表示されるrecovery codeをpassword manager等へ保存します。取引データはOS標準のapplication data directory内のランダムなvault ID配下に、SQLCipher 4.18.0で暗号化して保存されます。

- macOS: `~/Library/Application Support/OmniMoney/vaults/<vault-id>/omni_money.db`
- Windows: `%APPDATA%/OmniMoney/vaults/<vault-id>/omni_money.db`
- Linux: `~/.local/share/OmniMoney/vaults/<vault-id>/omni_money.db`

旧versionのroot直下 `omni_money.db`がある場合は、password入力後に明示的な移行を行います。元DBとsnapshotを検証しながらSQLCipher vaultへ複写し、移行後のrecovery codeを保存したことを確認するまでvaultは利用できません。FileVault、BitLocker、LUKS等のfull-disk encryptionも、アプリの鍵を窃取できる別processや未暗号化の一時領域に対するdefense in depthとして有効にしてください。

### 基本操作

メイン画面から収入・支出を登録し、資金項目、検索、タグ、画像、取引リンクを管理できます。CSV出力前には平文であることを確認する警告が表示されます。画像、タグ階層・取引タグ、カード引落しリンク、ledger設定を含む完全バックアップはCSV v3で正規化して出力され、復元時に関連IDが安全に再採番されます（拡張データがない場合は旧v2形式）。旧v1/v2 CSVもインポートできます。replaceは全体を1つのtransactionで適用し、検証・画像処理・関連付けのどこで失敗しても元データへrollbackします。appendは既存の取引関連データ・ledger設定を保持し、CSV設定が既存値と競合する場合はatomicに中止します。既存リンクは自動削除されません。CSV入力はストリーミングraw CSVが512 MiB、解析済みテキストが64 MiBまでです。後方互換のWails/JSON文字列経路は64 MiBまでなので、完全バックアップにはDesktopのファイルダイアログまたはserverのraw CSV uploadを使用してください。必ず暗号化済みvolumeへ直接保存し、不要になったcopyを残さないでください。browserは保存完了や保存先の暗号化を検証できず、SSD上のfile削除も完全消去を保証しません。

## 2. Multi-user serverの安全モデル

serverは次の領域を分離します。

- control DB: user、role、session、invite、password reset、暗号化済みkey envelopeだけを保持するSQLCipher DB
- user vault: userごとの取引データを保持する独立SQLCipher DB
- control DB key: user vaultのDEKとは独立したowner-only secret
- recovery code: browserが生成し、userだけが保存するvault復旧用secret

Adminはuserの追加・無効化等を管理できますが、userのpasswordまたはrecovery codeなしにuser vaultの中身を復号できません。現在のserverでは、user vault境界にまだ結び付いていないAI APIとsnapshot restoreを無効化しています。

詳細は[server multi-vault security model](server-multi-vault.md)、[SQLCipher鍵の運用](sqlcipher-key-operations.md)、[保存時暗号化volumeの運用contract](at-rest-encryption.md)を参照してください。

## 3. Docker Composeでローカル確認

### 必要なもの

- Docker EngineとDocker Compose
- SQLCipherに加えて、host側data directoryを保護するLUKS2、ZFS native encryption等
- ownerだけが読めるcontrol keyとinitial Admin setup token
- [保存時暗号化volumeの運用contract](at-rest-encryption.md)に従った期限内のattestation

準備例です。attestation JSONは実際の暗号化volumeを検証して作成し、例だけをcopyしないでください。

```bash
cp .env.example .env
umask 077
mkdir -p data secrets
openssl rand -hex 32 > secrets/control-database.key
openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n' > secrets/initial-admin-setup.token
chmod 600 secrets/control-database.key secrets/initial-admin-setup.token
```

`.env`で少なくとも次を実環境に合わせます。

- `OMNI_DATA_DIR`
- `OMNI_AT_REST_ATTESTATION_FILE`
- `OMNI_CONTROL_DB_ENCRYPTION_KEY_FILE`
- `OMNI_INITIAL_ADMIN_SETUP_TOKEN_FILE`
- `ALLOWED_HOSTS`
- `PASSKEY_RP_ID`（Pangolinの公開FQDN、scheme/portなし）
- `PASSKEY_ORIGINS`（例: `https://money.example.com`）

data directoryはcontainer UID/GID `10001:10001`だけが書けるようにします。macOS/ColimaではDocker Desktop/Colimaのfile sharingとvolume ownershipの差を確認してください。

```bash
sudo chown 10001:10001 data secrets/control-database.key secrets/initial-admin-setup.token
sudo chown root:root secrets/omni_data_at_rest.json
sudo chmod 700 data
sudo chmod 400 secrets/control-database.key secrets/initial-admin-setup.token
sudo chmod 444 secrets/omni_data_at_rest.json
docker compose -f compose.yaml -f compose.bootstrap.yaml -f compose.local.yaml up -d --build
```

ローカルoverrideは`127.0.0.1:4000`だけへHTTPをpublishします。ブラウザで`http://localhost:4000`を開きます。

### 最初のAdminを作る

初回画面で次を入力します。

1. `secrets/initial-admin-setup.token`の内容
2. Adminのemail、表示名、十分に長いpassword
3. browserが生成するrecovery codeの保存確認

recovery codeはpassword manager等へ保存してください。serverは平文recovery codeを保存せず、Admin passwordやcontrol keyだけでuser vaultを復旧できません。

最初のAdminを作成したらbootstrap overlayを外してcontainerを再作成します。起動確認後、host側setup tokenを安全にretireできます。

```bash
docker compose -f compose.yaml -f compose.local.yaml up -d --force-recreate
```

ログイン後、Adminはメニューの「サーバーユーザー管理」から次を実行できます。

- userのemailとroleを指定して、24時間有効の一度だけ表示されるinvite tokenを作る
- active userへ15分有効のpassword reset tokenを作る
- 自分以外のactive userを無効化し、そのuserのsessionと開いているvaultを失効させる

invite/reset tokenはURLへ入れず、安全な別経路で本人へ渡してください。本人は `/login?mode=invite` または
`/login?mode=reset` を開いてtokenを手動で貼り付けます。招待されたuserは自分のpasswordとrecovery codeを作り、
password resetにはAdminのtokenに加えて本人だけが保持する既存recovery codeが必要です。Admin UIが扱うのはaccount状態だけで、
他userの取引、vault path、password、recovery code、暗号鍵を表示または復号する機能はありません。

各userはログイン後の「パスキー設定」で、現在のpasswordを確認してPRF対応パスキーを登録できます。登録後もpassword認証は無効にならず、login画面と重要操作の再認証でpasswordまたはpasskeyを選べます。passkeyはPangolin経由のHTTPS originに紐付くため、`PASSKEY_RP_ID`や公開FQDNを変更すると既存passkeyは使えなくなります。passwordとrecovery codeは引き続き安全に保管してください。

### 停止、再開、安全な更新

```bash
docker compose -f compose.yaml -f compose.local.yaml down
docker compose -f compose.yaml -f compose.local.yaml up -d
docker compose -f compose.yaml -f compose.local.yaml logs --tail=200 omni-money
```

`down -v`は使用しないでください。bind mountのdataを削除しなくても、control key、recovery code、暗号化volumeの復旧情報を失うと復号できません。

Pangolin/TrueNAS本番のversion更新は、通常の`up --build`ではなく`./scripts/safe-update.sh <固定image:version>`を使います。data migrationはtransactionalに実行され、scriptは停止後のoffline checkpointを検証してからcandidateをingressに接続します。candidateがhealthyになる前に失敗した場合だけ、旧dataと旧imageへrollbackします。要件と復旧時の挙動は[安全な更新と限定ロールバック](safe-update.md)を参照してください。

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

## 5. Backupと復旧試験

暗号化されたdataだけでなく、次を別々の安全な場所へbackupします。

- control DB key
- 暗号化volumeのkey/recovery material
- attestationと更新手順
- 各userが保持するrecovery code

backup取得だけでは不十分です。本番とは隔離した環境で定期的に復旧し、control DBが開くこと、user本人のrecovery codeで対象vaultだけが開くこと、別userやAdminからは開けないことを確認します。

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

```bash
go test ./...
go test -tags server .
cd frontend && npm run build
```

serverの実buildはrepositoryのDockerfileとGitHub ActionsがSQLCipher 4.18.0をsource buildして検証します。
