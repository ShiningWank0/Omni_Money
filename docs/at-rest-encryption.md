# 保存時暗号化volumeの運用contract

Omni Moneyのserver modeは、SQLite DB、WAL、snapshotを置くdata rootが外部の暗号化volumeであることを運用者が確認したattestationを必須にします。attestationがない、古い、DB pathと一致しない、復旧試験やrotation予定が期限切れの場合は、DBを開く前に起動を拒否します。

attestationは暗号化そのものでも暗号学的な証明でもありません。アプリからhostのLUKS key slot、ZFS native encryption、cloud block-volume keyを一律に検証できないためです。必ずhost側で暗号化を設定・検証してから作成してください。環境変数だけで保護済みと見なすことはできません。

## 対象

- server modeの`DB_PATH`、SQLite WAL/SHM、同じdata root配下のsnapshot
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
  "key_id": "luks-prod-2026-01",
  "verified_at": "2026-08-28T00:00:00Z",
  "recovery_tested_at": "2026-08-28T00:00:00Z",
  "next_rotation_at": "2027-02-24T00:00:00Z"
}
```

`key_id`は非秘密の識別子で、raw key/passphraseを入れません。`verified_at`は31日以内、`recovery_tested_at`は185日以内、`next_rotation_at`は将来かつ400日以内が必要です。unknown/duplicate field、symlink、不安全な書込み権限は拒否されます。

```bash
chmod 600 /secure-config/omni-data-at-rest.json
export OMNI_DATA_DIR=/srv/omni-money
export OMNI_AT_REST_ATTESTATION_FILE=/secure-config/omni-data-at-rest.json
docker compose up -d --build
```

Composeを使わないserver modeでは`DATA_AT_REST_MODE=external-encrypted-volume`と`DATA_AT_REST_ATTESTATION_FILE`も設定します。

## 復旧試験とrotation

本番data rootを直接変更せず、暗号化backupの複製を別の隔離volumeでunlockします。DBとsnapshotを復元し、外部公開せず起動して`PRAGMA integrity_check`、代表的な残高・画像・tag、snapshot restoreを確認します。試験用copyを廃棄した実施時刻だけを`recovery_tested_at`へ記録します。

rotation前に現行keyで復旧できるbackupを作り、新key slot/versionを追加します。新keyで別sessionからunlockできることを確認してから`key_id`と`next_rotation_at`を更新し、recovery期間後に旧keyをretireします。key喪失時にOmni Moneyから復元する方法はありません。

## CSVと廃棄

CSVは確認済みの暗号化volumeへ直接保存します。email、共有folder、平文USBへ一時保存してから移動する運用は避けます。snapshot/CSVの保持期限後は暗号化backupを削除し、媒体廃棄時は対応するkey versionもretireしてcryptographic eraseを成立させます。
