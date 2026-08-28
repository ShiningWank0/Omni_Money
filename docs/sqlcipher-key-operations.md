# SQLCipher鍵の運用

サーバーモードはSQLCipher 4.18.0でデータベース本体、WAL、rollback journal、スナップショットを暗号化します。既存の平文SQLiteデータベースは、初回起動時に暗号化コピーの整合性と全テーブルの内容を検証してから、同じディレクトリ内のatomic renameで置換します。

この暗号化は、停止中のコンテナ、バックアップ、誤って公開されたデータファイルから内容を読まれることを防ぎます。アプリが解錠済みの間に同じホストのroot権限を奪ったプロセスや、入力監視・プロセスメモリ読取が可能なマルウェアまで防ぐものではありません。ホスト更新、最小権限、暗号化volume、オフホストの暗号化バックアップも継続してください。

## 初回設定

32 byteのランダム鍵を16進数またはBase64で秘密ファイルへ保存します。鍵を環境変数やComposeファイルへ直接書かないでください。

```bash
umask 077
mkdir -p secrets
openssl rand -hex 32 > secrets/database.key
chmod 600 secrets/database.key
```

Composeでは `.env` の `DB_ENCRYPTION_KEY_FILE` をコンテナ内の読取専用mount先に設定します。標準値は `/run/secrets/omni_database_key` です。

```yaml
services:
  omni-money:
    environment:
      DB_ENCRYPTION_KEY_FILE: /run/secrets/omni_database_key
    volumes:
      - ./secrets/database.key:/run/secrets/omni_database_key:ro
```

## バックアップと復旧

鍵ファイルと暗号化DBは別々の保管先へバックアップしてください。片方だけでは復旧できません。鍵を紛失すると、アプリ運営者を含め誰も内容を復号できません。

復旧演習では、隔離環境へ暗号化DBと鍵を復元して起動し、SQLCipherのpage authenticationとSQLiteのintegrity checkが成功することを確認します。平文へ復号したコピーを運用バックアップとして残さないでください。

鍵rotationは、旧鍵で開いたデータを新鍵のDBへ `sqlcipher_export()` し、全テーブルと整合性を検証した後に置換する必要があります。自動rotationコマンドを提供するまでは、手作業で `PRAGMA rekey` を実行しないでください。中断時の復旧と鍵・DBバックアップの世代が不一致になる危険があります。

