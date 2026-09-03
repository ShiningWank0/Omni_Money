# SQLCipher鍵の運用

サーバーモードはSQLCipher 4.18.0でcontrol databaseとユーザーごとのvault database、WAL、rollback journalを暗号化します。現在のmulti-user serverは既存の平文・旧単一ユーザーDBを自動では開きません。検証付き移行機能が完成するまで、旧DBを`CONTROL_DB_PATH`として指定しないでください。

この暗号化は、停止中のコンテナ、バックアップ、誤って公開されたデータファイルから内容を読まれることを防ぎます。アプリが解錠済みの間に同じホストのroot権限を奪ったプロセスや、入力監視・プロセスメモリ読取が可能なマルウェアまで防ぐものではありません。ホスト更新、最小権限、暗号化volume、オフホストの暗号化バックアップも継続してください。

## 初回設定

control database用に32 byteのランダム鍵を16進数またはBase64で秘密ファイルへ保存します。鍵を環境変数やComposeファイルへ直接書かないでください。この鍵はユーザーvaultのDEKを復号できません。各vaultのDEKはアプリが生成し、ユーザーのパスワードと回復コードで別々にwrapします。

```bash
umask 077
mkdir -p secrets
openssl rand -hex 32 > secrets/control-database.key
sudo chown root:10001 secrets/control-database.key
sudo chmod 440 secrets/control-database.key
```

Composeでは `.env` の `OMNI_CONTROL_DB_ENCRYPTION_KEY_FILE` をホスト側secretへ設定します。コンテナ内の標準mount先は `/run/secrets/omni_control_database_key` です。native Linuxのbind mountでは root所有・service group `10001`・`0440`（`root:10001`）にして、containerの固定service UID/GIDだけが読めるようにします。safe-updateはこのowner/modeとdevice/inode/link count/hashを固定します。

```yaml
services:
  omni-money:
    environment:
      CONTROL_DB_ENCRYPTION_KEY_FILE: /run/secrets/omni_control_database_key
    secrets:
      - omni_control_database_key
```

## バックアップと復旧

control鍵、暗号化control DB、暗号化vault、そして各ユーザーが保管する回復コードは役割が異なります。control鍵だけではvaultを復号できず、ユーザーがパスワードと回復コードの両方を失うと管理者も既存vaultを復元できません。これらをアクセス境界の異なる保管先へバックアップしてください。

復旧演習では、隔離環境へ暗号化DBと鍵を復元して起動し、SQLCipherのpage authenticationとSQLiteのintegrity checkが成功することを確認します。平文へ復号したコピーを運用バックアップとして残さないでください。

鍵rotationは、旧鍵で開いたデータを新鍵のDBへ `sqlcipher_export()` し、全テーブルと整合性を検証した後に置換する必要があります。自動rotationコマンドを提供するまでは、手作業で `PRAGMA rekey` を実行しないでください。中断時の復旧と鍵・DBバックアップの世代が不一致になる危険があります。
