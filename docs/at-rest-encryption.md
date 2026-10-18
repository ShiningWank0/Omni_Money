# 保存時暗号化volumeの運用contract

Omni Moneyのserver modeは、SQLite DB、WAL、snapshotを置くdata rootが外部の暗号化volumeであることを運用者が確認したattestationを必須にします。attestationがない、DB pathと一致しない、配置や権限が不安全な場合は、DBを開く前に起動を拒否します。

attestationは暗号化そのものでも暗号学的な証明でもありません。アプリからhostのLUKS key slot、ZFS native encryption、cloud block-volume keyを一律に検証できないためです。必ずhost側で暗号化を設定・検証してから作成してください。環境変数だけで保護済みと見なすことはできません。

`CONTROL_DB_PATH` と `VAULT_ROOT` は同じattested `data_root`の子directoryに置きます。`data_root`から各pathまでの既存componentにsymbolic link、通常directory以外のparent、group/otherから書込み可能なdirectoryを含めることはできません。まだ存在しないDBや子directoryは初回起動で作成できます。Dockerの通常のbind mountで`data_root`が`0755`でも、group/otherの書込みbitがなければ利用できます。control鍵とinitial-admin setup tokenはdata root外のsecret mountへ置きます。

attestation file自身と、filesystem rootからその親directoryまでの各component（symlinkがある場合は解決前と解決後の双方）は、rootまたはserver実行UIDが所有し、group/otherから書込み不可でなければなりません。`/tmp`などの共有書込みdirectory配下には置かず、server専用の設定directoryまたはread-only secret mountを使用してください。

## 対象

- server modeのcontrol DB、ユーザーvault、SQLite WAL/SHM、ユーザーごとのSQLCipher snapshot
- schema version 6のVault内にある取引保存受付、処理中・失敗した入力と画像、再送防止記録
- volumeの複製、host snapshot、外部backup
- serverから取得したCSVを保存する媒体

desktop modeはOSのFileVault、BitLocker、LUKS等を有効にしてください。Downloadsへ書き出すCSVも同じ暗号化volume上だけに保管し、共有先へ平文で複製しないでください。

## volumeを確認する

Linuxでは既存のLUKS2 mapperまたはZFS native encryption datasetをdata rootへmountします。破壊的な初期化コマンドはここには載せません。新規作成はOS/製品の公式手順に従い、対象deviceを二重確認してください。

```bash
findmnt -T /srv/omni-money
sudo cryptsetup status <mapper-name>
lsblk -o NAME,TYPE,FSTYPE,MOUNTPOINTS
# ZFSの場合
zfs get encryption,encryptionroot,keystatus <pool/dataset>
```

data rootの実体が暗号化volume上にあり、unlock keyがDB、snapshot、Compose file、repositoryとは別のsecret manager/recovery媒体にあることを確認します。host停止時にvolumeをlockし、raw deviceまたはbackupを別環境へ持ち出してもkeyなしでSQLite headerやCSV内容を読めないことも検証します。

## attestation

data rootの外にowner-writableな通常fileとして置きます。Composeはread-only secretとして`/run/secrets`へmountします。

```json
{
  "version": 1,
  "protection": "external-encrypted-volume",
  "provider": "luks2",
  "data_root": "/app/data",
  "key_id": "luks-prod-2026-01"
}
```

必須項目は上記の5項目です。`key_id`は非秘密の識別子で、raw key/passphraseを入れません。unknown/duplicate field、symlink、不安全な書込み権限は拒否されます。

旧設定の`verified_at`、`recovery_tested_at`、`next_rotation_at`も任意の運用記録として読み取れます。記載する場合はRFC 3339形式を使用します。これらの入力や更新は起動条件ではなく、確認日・復旧試験日が古い場合や鍵更新予定日を過ぎた場合も、これらの日付を理由に起動を拒否しません。新規導入で未実施の復旧試験日時を作る必要はありません。

```bash
# native serverを同じownerで動かす場合
chmod 600 /secure-config/omni-data-at-rest.json
# 固定UID 10001のComposeへbind mountする場合は非秘密documentとして
# root所有・read-onlyにする: chown root:root ... && chmod 444 ...
export OMNI_DATA_DIR=/srv/omni-money
export OMNI_AT_REST_ATTESTATION_FILE=/secure-config/omni-data-at-rest.json
docker compose up -d --build
```

Composeを使わないserver modeでは`DATA_AT_REST_MODE=external-encrypted-volume`と`DATA_AT_REST_ATTESTATION_FILE`も設定します。

## safe-update用host attestation

`DATA_AT_REST_ATTESTATION_FILE`はserver processが読むcontainer側のattestationであり、
safe-updateがrollback先を決めるtrust rootには使いません。host側のencrypted volume、
実際のbind source、固定checkpoint siblingをroot operatorが別のowner-only/read-only
fileへ記録します。

```json
{
  "version": 1,
  "protection": "external-encrypted-volume",
  "encrypted_volume_root": "/srv/omni-money",
  "data_root": "/srv/omni-money/data",
  "checkpoint_root": "/srv/omni-money/omni-money-update-checkpoints"
}
```

`OMNI_UPDATE_ATTESTATION_FILE`でComposeのhost-only extensionへ渡し、root所有・mode `0400`/`0440`/`0444`、
symlinkなし、書込み不可のparentを守ります。safe-updateはこの3 pathをresolved Composeの
host sourceと突合し、encrypted rootの境界、data/checkpointのcontained sibling、同じ
filesystem/device、固定checkpoint名を検証します。`OMNI_UPDATE_CHECKPOINT_DIR`の任意指定
は廃止しました。v1.1.0以前から移行する場合は、既存attestationの`data_root=/app/data`
をhost pathへ置き換えず、実mountのhost pathを調査してこのhost schemaを新規作成し、
restore試験後にsafe-updateを有効化してください。

## 復旧試験とrotation

本番data rootを直接変更せず、SQLCipher snapshot（同じvault DEKで暗号化されたciphertext）の複製を別の隔離volumeでunlockします。control DBとuser vaultを復元し、外部公開せず起動して`PRAGMA integrity_check`、現行schema migration、critical index/trigger、代表的な残高・画像・tag、user本人のrecovery codeによるvault復旧を確認します。snapshot restoreの演習では、破損・wrong-key・別user・symlink/hardlinkを拒否し、失敗後も元live DBをreopenできることを確認します。復旧試験はバックアップを実際に戻せるかを確認する運用作業です。実施記録を残す場合は、試験用copyを廃棄した実施時刻だけを記録します。任意の`recovery_tested_at`を利用しても構いませんが、記録の有無や経過日数でserverの起動は制限されません。

rotation前に現行keyで復旧できるbackupを作り、新key slot/versionを追加します。新keyで別sessionからunlockできることを確認してから`key_id`を更新し、recovery期間後に旧keyをretireします。次回予定は必要に応じて運用記録へ残します。key喪失時にOmni Moneyから復元する方法はありません。

## CSVと廃棄

CSVは確認済みの暗号化volumeへ直接保存します。email、共有folder、平文USBへ一時保存してから移動する運用は避けます。browser downloadやSSDに対するfile削除だけでは完全消去を保証できません。snapshot/CSVの保持期限後は暗号化backupを削除し、媒体廃棄時は対応するkey versionもretireしてcryptographic eraseを成立させます。

## Issue #62と自動テストの対応

コードが担う保護はCIで検証します。利用者の「確認・復旧試験済み」という日時の申告を、実装の正しさやテストの代わりにはしません。

| 検証対象 | 自動テストとCIでの実行 |
| --- | --- |
| DB・書き込み中のWAL・SHM・snapshotの権限、金融データの平文漏出防止 | `backend/database`の`TestSQLCipherLiveWALAndSnapshotStayEncryptedAndPrivate`が、WALに書き込みを残したまま実ファイルを検査し、DB/WAL/SHM/snapshotの`0600`とdirectoryの`0700`、snapshotからの読み戻しを検証する。LinuxのSQLCipher専用CIで必須実行。SHMは索引情報であり、SQLCipher暗号化の対象とは区別する。 |
| 鍵なし・別の鍵・破損した暗号文の拒否 | `backend/securedb`の`TestEncryptedDatabaseAndSnapshotFailClosed`、`backend/database`の`TestEncryptedSnapshotRestoreRejectsWrongKeyAndCorruption`。SQLCipher専用CIで必須実行。 |
| 鍵の保存・分離、パスワード／recovery変更と復旧 | `backend/keyenvelope`の認証・改ざん・context binding試験、`backend/secretfile`の鍵file検証に加え、`backend/vault`の`TestSQLCipherColdRestoreRecoversVaultWithRotatedCredentials`が実SQLCipher control DBとVaultを閉じて複製し、元fileを削除した後に復旧する。更新前のパスワード・recovery secretと誤ったcontrol鍵を拒否し、更新後のenvelopeから復旧した鍵で取引と残高を読み戻す。SQLCipher専用CIで必須実行。 |
| ユーザー分離、snapshot復元時の認可・session失効 | `backend/api`の`TestServerSnapshotsStayBoundToUserAndRestoreRevokesSessions`、Vault分離試験。実SQLCipherと本番routerを使うCIで必須実行。 |
| CSVの一時fileの非公開化・削除、exportの権限 | `backend/fileprivacy`の`TestCreatePrivateTempFileCreatesBoundedPrivateArtifact`、置換されたfileを削除しない試験、`backend/core`の`TestBackupToCSVDirectoryUsesSelectedDirectory`によるexportの`0600`検証、およびCSV import/exportの回帰試験を通常のGo CIで実行。browserで保存したdownload先の権限はOS/browserが管理する。 |
| backupの秘密鍵分離・権限・改ざん検出・保持世代の削除 | `scripts/backup-data-root_test.sh`が緩いumaskでも出力`0600`／directory`0700`であること、別配置のcontrol鍵がarchive/manifestに入らないこと、checksum不一致の拒否、`--keep`による世代削除と無関係なfileの保持を検証する。Docker CLIはmock、tar/checksum/展開・比較/世代削除は実処理。通常のCIで実行する。 |
| 日付申告なしの新規導入・既存設定 | `backend/atrest`が日付省略、古い日付・過去の更新予定・将来日付での受理と、従来のpath・所有者・権限・JSON検証を実行する。CIのDocker起動試験とserver E2Eも日付なしの設定を使う。 |

SQLCipher専用CIは`OMNI_REQUIRE_SQLCIPHER_TESTS=1`を指定します。SQLCipherが利用できない場合にintegration testをskipして成功扱いにせず、失敗にします。

host側のZFS/LUKS設定、実際のbackup媒体の故障、媒体上の完全消去までは、このCIから証明できません。これらはhostの設定・backup運用として扱い、試験日時の入力をアプリの起動条件にはしません。また、password/recovery envelopeの更新はVault DEKや外部volume鍵の更新とは別です。過去のbackupは当時の鍵・envelopeを保持するため、鍵の廃棄はbackupの保持期間も含めて設計してください。
