# Disaster Recovery（whole-server backup / restore runbook）

Issue #152 の縮小実装として、service停止中のwhole data-root冷間archive、整合性検証、
off-host保管runbook、復元演習手順を提供する。online incremental backup engine、
admin restore HTTP API、DEK escrow、独自remote uploaderは意図的に実装しない
（docs/server-multi-vault.md の脅威モデルを参照）。

## 復旧対象（DR set）

archiveに含まれるもの（`scripts/backup-data-root.sh` がdata root全体を取得）:

| 対象 | path（data root相対） | 内容 |
| --- | --- | --- |
| control DB | `control/omni_control.db` | user registry、credential envelope、vault metadata（SQLCipher ciphertext） |
| 各user vault | `vaults/<vault-id>/ledger.db` | 財務ledger本体（各userのDEKで暗号化） |
| 各vaultのsnapshot | `vaults/<vault-id>/snapshots/*.db` | vault DEKで暗号化された時点復元用ciphertext |

archiveに**含まれない**もの（契約上data root外に置かれる。別途、archiveとは異なる
保管先へ複製すること）:

- control DB key file（`root:10001` mode `0440`。漏洩するとcontrol DBが復号される）
- data-at-rest attestation file（`root:root` mode `0444`）
- initial admin setup token file（bootstrap後は不要）
- 各userのrecovery code（server側に存在しない。user各自が保管）

## 保管のtrust model

- archiveは**SQLCipher ciphertextのまま**扱う。application adminもuser vaultの平文を
  復号できない。ただしcontrol DB key fileと同一の場所へ平文同梱することは禁止
- archive本体と鍵material（control key・attestation）は**異なるtrust domain**へ保管する
  （例: archiveは暗号化off-host storage A、鍵materialはpassword manager/別媒体B）
- host rootを持つ攻撃者や稼働中serverのmemoryからは保護できない。DRはat-rest
  保管と運用分離の対策であり、compromised hostへの対策ではない
- user vault単位の復旧（key loss）は不可逆。recovery codeが失われたvaultは
  key materialが揃っても平文化できない

## RPO / RTO

- **RPO**: 本scriptは手動・cron実行。最後の成功したgenerationからの経過時間がRPO。
  cron併用時は最低でも週次を推奨
- **RTO**: 復元演習の所要時間を毎回記録し、実測値で管理する（下記「復元演習」）

## backup取得

```bash
sudo bash scripts/backup-data-root.sh            # data root直上 omni-money-backups/ へ
sudo bash scripts/backup-data-root.sh --dest /mnt/encrypted-backups/omni-money --keep 8
sudo bash scripts/backup-data-root.sh --no-start # 停止したまま維持（メンテナンス併用時）
```

動作:

1. Compose projectの `omni-money` service containerを一意に解決する（複数/ゼロは中断）
2. data root契約を検証する（`10001:10001`・mode 0700・symlink/hardlink/特殊file拒否・
   group/other書込み拒否・nested mount拒否）
3. 容量preflight後、`docker stop --time 30` で停止し、停止後にもう一度treeを検証する
4. `tar --one-file-system --numeric-owner -cpf` で取得し、member名/型を検証して
   （symlink・device・FIFO等はfail closed）、atomic renameで `data.tar` を公開、
   `data.tar.sha256` sidecarを作成し `sha256sum --check` で自己検証する
5. **平文SQLite header検査**: archive内のcontrol DBと全ledger.dbが
   `SQLite format 3` で始まらないことを確認する（暗号化契約の逸脱をfail closed）
6. `manifest.json`（archive hash・source identity・vault inventory・外部素材一覧）を
   作成し、`--verify` 相当の自己検証を通したgenerationだけを成果物とみなす
7. containerを再起動し90秒以内のhealthyを確認する（失敗時は明示的に報告。
   `--no-start` 指定時は停止を維持する）
8. どの時点で中断しても、停止中のcontainerはEXIT trapが自動再起動を試みる

`manifest.json` が存在するgenerationだけが「完了・検証済み」とみなせる。中断時は
`data.tar` のみが残り、`manifest.json` が無いgenerationは使わないこと。

## archive検証

serviceへ触れずに既存generationを検証する:

```bash
bash scripts/backup-data-root.sh --verify /mnt/encrypted-backups/omni-money/20260908T120000Z
```

checksum照合、member検証、平文header検査、manifestのhash照合、
manifestに列挙されたvaultのledger存在確認を行い、`verification OK` を出力する。

## restore手順（operator runbook）

restoreは自動化しない。破壊的な置換は必ず手順書に従い手動で行う:

1. 稼働中serverを `docker compose stop`（`down -v` は禁止）で停止し、既存data rootを
   `mv` で退避する（削除しない）
2. 退避と同じpathで新data rootを `mkdir -m 0700` し、`chown 10001:10001` する
3. 復元対象generationの `data.tar.sha256` でchecksumを検証した上で
   `tar --numeric-owner -xpf data.tar -C <新data root>` する
4. control DB key fileとattestationを、compose secretの正しいhost pathへ復元する
   （owner/modeは [利用ガイド](how-to-use.md) と [at-rest encryption](at-rest-encryption.md) 参照）
5. `docker compose up -d` で起動し `/healthz` とadmin loginを確認する。
   起動時のschema migrationはserverが自動適用する
6. 各userがrecovery codeで各自vaultだけへaccessできること、application adminから
   他user vaultを開けないことを確認する

## 復元演習（restore drill）

最低でも半に一度、以下を隔離環境（本番hostではない隔離machine/VM）で実施する:

1. 最新generationと鍵materialのコピーを隔離環境へ持込み、`--verify` を通す
2. 上記restore手順を隔離data rootに対して実施する
3. server起動後、既知の最新取引が閲覧できること、admin consoleが機能することを
   確認する。user本人でloginし、`PRAGMA integrity_check` 相当の健全性は
   snapshot一覧とledger閲覧で確認する（平文化はしない）
4. 意図的に壊したarchive（1byte改変、鍵不一致、manifest削除）が `--verify` と
   restoreでfail closedになることも確認する
5. 所要時間（RTO実測）と問題点を記録する。鍵・path・財務内容はlogに残さない

## 実装が意図的に避けているもの

- online/incremental backup engine（冷間archiveのみ）
- admin restore HTTP API / admin escrow DEK（restoreはhost operator boundary）
- 独自remote uploader（off-host転送はoperatorが管理する暗号化storageで行う）
- manifest署名（sha256＋member検証＋平文header検査が本boundaryでの整合性検証。
  署名は新たな鍵管理を生むため見送る）
