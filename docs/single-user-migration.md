# 旧single-user serverからmulti-vaultへの移行

Issue #148は、既存のDesktop移行とCSV v3を使うoperator runbookに範囲を限定します。
新しいmigration CLI、serverへの旧DB直接注入、独自journal、管理者によるDEK取得は追加しません。
元のserverを停止・保全し、複製上で変換して、新しく認証設定した本人のvaultに取り込みます。
これにより旧認証設定の復活と誤ったuserへのDB割当てを避けます。無停止移行や自動再開は提供しません。

## 対応する入力と中止条件

- 完全ヘッダーと最終manifestを持つCSV v3を出力できる旧環境は、その環境の隔離した複製で出力します。
- CSV v1/v2は取引のみです。画像・タグ・リンク・設定を保持する完全移行には使用しません。
- 平文SQLiteの旧DBだけがある場合は、停止済みの複製を現行Desktopの明示的legacy migrationで開き、CSV v3を出力します。Desktopが認識・検証できるschemaだけが対象です。`user_version`だけで互換性を判断せず、未知のschemaや新しすぎるschemaは中止します。
- 旧暗号化DBはDesktopの平文legacy migrationへ渡せません。元の鍵と対応版で隔離復元してv3出力できる場合だけ進めます。復号鍵不明、破損、整合性違反、未知の差分では中止し、元DBを修復・上書きしません。
- symlink/hardlinkや特殊fileを入力に使いません。WAL/journalが残るDBを単体でコピーしません。DBとsidecarを含む停止時点の一式を保全します。複製側で元versionによる正常終了・checkpointを済ませてから次段へ進めます。

## 事前準備と記録

1. 旧serverへの書込みとAI連携を停止します。旧imageのdigest/version、schema、暗号化方式、DB・sidecar・snapshot・設定のinventoryとSHA-256を記録します。元一式を暗号化された別媒体にも複製し、以後は読取り専用で保全します。
2. FileVault/BitLocker/LUKS等の暗号化済み領域に、他userが読めない独立した作業場所を用意します。POSIXではdirectory 0700/file 0600、Windowsでは本人だけのACLを確認します。CSVとDesktop作業用複製もこの領域に置きます。
3. 元一式・作業用複製・変換後vault・CSV・import用一時画像・新snapshotを同時に保持できる空き容量を実測します。圧縮後のサイズだけで判断しません。現行CSVはwire 512 MiB、解析済みテキスト64 MiBなどの上限があり、上限超過時の分割による完全移行は本手順の対象外です。空き容量や上限が確認できない場合は進めません。
4. 対応版の読取り専用検証でintegrity、foreign key、schemaと件数を確認します。元DB上でmigrationやcheckpointを実行しません。schema変更が必要な検証は複製側だけで行います。
5. 記録には工程・成否・version・件数・hashだけを残します。password、recovery code、鍵、取引内容、画像やCSV本文をlogやIssueに貼り付けません。hashも公開せず作業記録として保管します。

## 隔離環境での予行演習（dry-run）

1. 元環境と同じversionを外部公開しない複製環境で起動し、対応していればCSV v3を出力します。旧認証・AI設定を現行serverへ持ち込みません。
2. 平文旧DBのDesktop経由変換では、既存Desktopデータのない専用OSアカウントを使用します。そのアカウントのapplication data directoryに作業用複製だけを配置し、現行Desktopの移行案内に従います。元データをこのdirectoryへ移動してはいけません。Desktop移行は作業用複製の平文を整理・削除し、journalで中断復旧します。passwordとrecovery code保存確認を完了してCSV v3を出力します。
3. 別のdata rootと新しいcontrol keyを持つ現行serverを起動します。新Adminのsetup、移行先userの招待・password・recovery code保存を行います。共有旧ledgerを誰が所有するかを先に決め、移行する本人でloginします。Adminが他人のvaultへ代理注入する手順はありません。
4. 空の移行先vaultへCSV v3を「置換」で取り込みます。プレビュー機能が利用できるversionでは分類・削除対象を確認してから同意します。途中エラーでは全体がrollbackします。appendで再試行すると重複するため使用しません。
5. 取引・金額・残高・メモ、画像内容、タグ階層と紐付け、カード/銀行リンク、2種類のledger設定を照合します。IDは再採番されるためIDそのものでは比較しません。別userとAdminのsessionから移行先取引が見えないことも確認します。
6. 新vaultのsnapshotを作成し、隔離環境で復元を試します。restore後は再loginが必要です。旧snapshotは新DEKで開けないため新vaultへ直接コピーせず、旧環境の保全セットに残します。

## 切替え・中断・rollback

予行演習の合格後も旧serverは停止したままにし、最終保全セットから同じ手順を実施します。
新serverの検証完了までは通常利用を開始しません。認証設定・旧session・TOTP・AI credentialは移しません。
新環境には新しいpassword/recovery codeを用意し、旧token・旧sessionを無効化します。

作業記録に「保全済み／変換済み／取込検証済み／切替済み」の工程を記録します。
これは自動resume journalではありません。中断時は新旧両serverを停止し、最後に検証できた保全セットへ戻ります。
Desktop変換の中断だけは既存journalと同じpasswordによる再開手順を使います。
importの応答が不明な場合は先に対象vaultを確認し、無条件にappendしません。再試行は通常利用前の専用移行先で全体replaceします。
新環境利用開始後のrollbackは新規取引を失う可能性があるため、自動切戻しはしません。

元DB・旧snapshot・旧imageと鍵の保全期間を決め、新環境の復元試験と照合が終わるまで削除しません。
新serverではcontrol DB/key、各user vault/snapshot、recovery materialを別々に保全します。
[利用ガイド](how-to-use.md#5-backupと復旧試験)と[serverの安全モデル](server-multi-vault.md#snapshot-restore-drill)も参照してください。

## 検証範囲

- `backend/core/single_user_migration_test.go`: 停止済み原本のhash不変、読取り専用複製からv3出力、別ledgerへの拡張データ移行、replace再実行、認証・AI設定の非移行。
- `backend/database/schema_migration_test.go`: 複数旧schema、archive sidecar、未知schemaの拒否。
- `backend/desktopaccount/migration_test.go`: journal、中断再開、置換された入力、権限とリンク等の拒否。暗号化境界は同packageのSQLCipher integration testで検証します。
- `backend/api/server_csv_boundary_integration_test.go`: 本人vaultへの束縛、別userからの分離、CSVによるID再採番。

これらはoperatorの停電・媒体故障・容量計画まで保証しません。実際の元version・platformで上記予行演習を行い、結果を記録してから切り替えてください。
