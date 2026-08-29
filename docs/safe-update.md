# 安全な更新と限定ロールバック

Omni Money v1.1.0以降のledger/control DB migrationはversion管理され、1つのSQLite transaction内で実行されます。migrationや必須schema検証が失敗するとtransaction全体が破棄されるため、途中まで変更したDBを公開しません。新しいbinaryが対応versionより新しいDBを開こうとした場合もfail closedです。

Pangolin/TrueNASのDocker構成では、同梱の`scripts/safe-update.sh`を使います。このscriptは通常の`docker compose up`を置き換える更新用entry pointです。

## 保証する流れ

1. 現行containerがhealthyで、`/app/data`が実在するbind mountであることを確認します。
2. `latest`ではない固定tagまたはdigestを、稼働中のserviceを止める前にpullします。
3. 現行imageへrollback専用tagを付け、data容量の約2倍＋100 MiBの空きを確認します。
4. serviceを停止してからdata root全体をtar checkpointへ複写し、SHA-256を検証してdiskへ同期します。
5. candidate containerをingress networkから切り離したまま起動し、Docker healthcheckがhealthyになるまで待ちます。
6. healthyになった場合だけ`.env`の`OMNI_IMAGE`を更新し、元と同じ固定IPでPangolin networkへ接続します。

停止後から手順6完了前までにcommand、起動、migration、healthcheck、env更新、network接続のいずれかが失敗した場合だけ、自動rollbackが作動します。candidateを停止し、そのdata directoryをcheckpoint内へ退避してから、更新前checkpointと旧imageを復元します。復元した旧containerもingressから隔離した状態でhealthcheckを通してから接続します。

成功後に後から発生した通常のapplication errorを理由に自動rollbackするbackground処理はありません。成功した更新を無闇に巻き戻さないためです。成功時もcheckpointとrollback imageは自動削除しません。別backupと動作確認が完了した後、operatorが明示的にretireしてください。

## 必要条件

- host portをpublishしないbase `compose.yaml` のPangolin構成であること
- Omni Money containerが1つの専用ingress networkだけに接続され、固定IPを使用していること
- `.env`が通常fileでありsymlinkではないこと
- checkpoint先が暗号化され、live data directoryの外側にあること
- 実行userがDocker、data directory、`.env`へ必要な権限を持つこと
- control DB key、保存時暗号化volumeのkey/attestation、recovery codeは別の暗号化backupでも保護されていること

checkpointの既定位置はdata directoryの隣の`omni-money-update-checkpoints/`です。別Datasetを使う場合は`.env`で`OMNI_UPDATE_CHECKPOINT_DIR`を絶対pathに設定します。checkpointはSQLCipher DBを含みますが、保存先volume自体も暗号化してください。

## v1.1.0への更新例

repositoryと`.env`を配置したCompose project directoryで実行します。

```bash
chmod 700 scripts/safe-update.sh
./scripts/safe-update.sh ghcr.io/shiningwank0/omni_money:1.1.0
```

成功時は、保持したcheckpoint directoryとrollback image tagが表示されます。失敗時は終了codeが非zeroのままになり、rollbackの成否と復旧artifactの場所が表示されます。自動復元まで失敗した場合はserviceを隔離・停止したままにするため、退避dataやcheckpointを削除せず原因を確認してください。

ローカル開発用`compose.local.yaml`はhost portをpublishするため、このscriptは意図的に拒否します。開発dataでは通常の再buildを使い、本番相当の更新試験はhost portのない専用test deploymentで行います。
