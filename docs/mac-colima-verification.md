# Mac + Colima で公開済み Docker イメージを隔離検証する

この手順は、公開済みの Omni Money 2.0.0 イメージを Mac で試験データだけを使って確認するためのものです。TrueNAS のデータ移行、Pangolin/TLS、TrueNAS の ACL、Linux 専用の `safe-update.sh` は別途検証します。通常の `compose.yaml` と `compose.local.yaml` だけで起動するとホストの `data` を書き込み可能で bind mount するため、この手順では使いません。

## 1. Mac 側の準備

1. FileVault が有効であること、検証ディレクトリが iCloud Drive や他の同期先に含まれないことを確認します。以下は空の `~/Desktop/test` を専用に使う例です。Desktop 同期が有効なら同期対象外の別ディレクトリを選びます。
2. 検証ディレクトリに既存データやシンボリックリンクを入れず、本番の鍵・DB・CSVをコピーしません。空きポート 4000 を確認します。
3. Docker Compose 2.24.4 以上を用意します。[Compose の `!override` 仕様](https://docs.docker.com/reference/compose-file/merge/#replace-value)が必要です。`docker compose version` で確認します。

[Colima の設定例](https://github.com/abiosoft/colima/blob/main/embedded/defaults/colima.yaml)を参照し、`colima start --profile omni-money-v2 --edit --activate=false` で専用プロファイルを作り、既存の設定項目を次の値にします。`mounts: []` はホームディレクトリ全体の書き込み可能な共有になり得るため、必ず **1 件の明示的なマウント**に置き換えます。`location` は実際の絶対パスへ変更し、`kubernetes` や `autoActivate` は元の設定箇所で編集して重複させません。

```yaml
autoActivate: false
kubernetes:
  enabled: false
mounts:
  - location: /Users/<mac-user>/Desktop/test
    writable: false
```

起動後は `colima status --profile omni-money-v2` と `docker context ls` で専用の Docker context を確認します。以下のコマンド例では、表示された context 名を `CONTEXT` に設定します。`docker context use` で普段の既定 context は変更しません。Colima の設定ファイル `~/.colima/omni-money-v2/colima.yaml` でも `mounts` が上記 1 件であることを確認します。

```bash
TEST_DIR="$HOME/Desktop/test"
CONTEXT=colima-omni-money-v2
REPO_DIR="$HOME/Desktop/Omni_Money"
```

context 名や repository の場所が違う場合は置き換えます。`docker context inspect "$CONTEXT"` が対象の Colima プロファイルを指すことも確認してください。

## 2. 公開済みイメージと秘密ファイル

[Docker Release workflow](../.github/workflows/release-docker.yml) の 2.0.0 実行結果から、公開された `ghcr.io/shiningwank0/omni_money@sha256:<64 桁>` を確認します。`latest` やタグだけを使わず、ワークフローの job summary と registry の digest が一致することを確認してください。手元で `--build` すると Dockerfile の既定バージョンは `dev` となり、公開成果物そのものの検証になりません。

次の二つのファイルは Mac 上の専用ディレクトリに初回だけ作ります。ブロック内の `set -eC` はエラー時に中止し、既存ファイルへの上書きを禁止します。既存の鍵を上書きすると DB を復号できなくなるため、再実行時は既存ファイルを残します。`sudo chown` は実行しません。秘密ファイルの中身をチャット、Issue、PR、ログへ貼らないでください。Mac 側の `0600` ファイルはコンテナの UID `10001` から読めるとは限らないため、後で Colima VM 内の専用 volume に取り込みます。

```bash
(
  set -eC
  umask 077
  test -d "$TEST_DIR"
  test ! -L "$TEST_DIR"
  test ! -L "$TEST_DIR/secrets"
  mkdir -p "$TEST_DIR/secrets"
  test ! -e "$TEST_DIR/secrets/control-database.key"
  test ! -L "$TEST_DIR/secrets/control-database.key"
  test ! -e "$TEST_DIR/secrets/initial-admin-setup.token"
  test ! -L "$TEST_DIR/secrets/initial-admin-setup.token"
  chmod 700 "$TEST_DIR/secrets"
  openssl rand -hex 32 > "$TEST_DIR/secrets/control-database.key"
  openssl rand -base64 48 | tr '+/' '-_' | tr -d '=\n' > "$TEST_DIR/secrets/initial-admin-setup.token"
  chmod 600 "$TEST_DIR/secrets/control-database.key" "$TEST_DIR/secrets/initial-admin-setup.token"
)
```

`$TEST_DIR/secrets/omni_data_at_rest.json` は [保存時暗号化 volume の運用 contract](at-rest-encryption.md) に従い、実際に確認した事実で作成します。`provider` は FileVault を確認した場合の例です。`verified_at` と `recovery_tested_at` は**実施した日時だけ**を UTC で記録します。復旧試験をしていなければ値を作らず、起動前に復旧手段と試験を確認してください。attestation は暗号化の自動検証ではありません。

```json
{
  "version": 1,
  "protection": "external-encrypted-volume",
  "provider": "filevault",
  "data_root": "/app/data",
  "key_id": "mac-colima-test-volume-2026",
  "verified_at": "<実際に確認した UTC 日時>",
  "recovery_tested_at": "<実際に復旧試験した UTC 日時>",
  "next_rotation_at": "<実際の更新予定の UTC 日時>"
}
```

この JSON は例をそのままコピーして使わず、保存後に `chmod 600 "$TEST_DIR/secrets/omni_data_at_rest.json"` とします。アプリは日時の有効期限やファイルの配置・権限を検証し、満たさなければ起動を拒否します。

## 3. Mac 専用 Compose 設定

`$TEST_DIR/compose.mac-test.yaml` を次の内容で作成します。これはリポジトリの base Compose に**最後に重ねる**ファイルです。DB と秘密ファイルを Colima VM 内の別々の Docker named volume に移し、Mac の読み取り専用共有には書きません。Compose のホストファイル由来の secret mount は外し、専用 volume の秘密ファイルだけを `/run/secrets` に読み取り専用で渡します。`build` を除去し、意図しない自動再起動も止めます。`/tmp` の tmpfs は base の volume 一覧を置き換えた後も維持します。

```yaml
services:
  omni-money:
    build: !reset null
    restart: "no"
    secrets: !override []
    volumes: !override
      - type: volume
        source: omni_mac_test_data
        target: /app/data
      - type: volume
        source: omni_mac_test_secrets
        target: /run/secrets
        read_only: true
      - type: tmpfs
        target: /tmp
        tmpfs:
          size: 128m
          mode: 0o1777
volumes:
  omni_mac_test_data: {}
  omni_mac_test_secrets:
    external: true
    name: omni-money-v2-test-secrets
```

`$TEST_DIR/compose.env` を次の形式で作成します。`<digest>` は実際の 64 桁の値へ置き換え、秘密そのものは書きません。`OMNI_DATA_DIR=/dev/null` は Mac 専用 overlay を指定し忘れた場合にホストのディレクトリを作らせず、起動を失敗させるための値です。ほかの `/dev/null` は base Compose の必須補間を満たすだけの値です。Mac 専用 overlay を省いた場合、秘密が通常ファイルではないため起動を拒否します。これらは実際の秘密や attestation の代用ではありません。この env file を TrueNAS へ持ち込まないでください。

```dotenv
OMNI_IMAGE=ghcr.io/shiningwank0/omni_money@sha256:<digest>
OMNI_DATA_DIR=/dev/null
OMNI_AT_REST_ATTESTATION_FILE=/dev/null
OMNI_UPDATE_ATTESTATION_FILE=/dev/null
OMNI_CONTROL_DB_ENCRYPTION_KEY_FILE=/dev/null
OMNI_INITIAL_ADMIN_SETUP_TOKEN_FILE=/dev/null
OMNI_WEB_PORT=4000
```

保存後に `chmod 600 "$TEST_DIR/compose.env" "$TEST_DIR/compose.mac-test.yaml"` を実行します。シェルに既存の `OMNI_*`、`COMPOSE_*`、`DOCKER_HOST` がある場合、Compose の補間・対象 context に影響し得るため、使用する値を事前に確認してください。解決済み設定や環境変数の全出力を公開しないでください。

初回だけ、公開イメージを取得して秘密ファイルを Colima VM 内に取り込みます。`IMAGE` は `compose.env` の `OMNI_IMAGE` と**同じ完全な digest**にします。取り込み用コンテナはネットワークなしで、Mac の秘密ディレクトリを読み取り専用でマウントします。書き込み先は Colima VM 内の専用 volume だけです。既に volume にファイルがあれば上書きせず失敗します。再実行する前に原因を確認してください。

```bash
IMAGE='ghcr.io/shiningwank0/omni_money@sha256:<digest>'
docker --context "$CONTEXT" pull "$IMAGE"
docker --context "$CONTEXT" volume create omni-money-v2-test-secrets
docker --context "$CONTEXT" run --rm --network none --read-only --user 0:0 \
  --entrypoint /bin/sh \
  --mount "type=bind,source=$TEST_DIR/secrets,target=/input,readonly" \
  --mount type=volume,source=omni-money-v2-test-secrets,target=/run/secrets \
  "$IMAGE" -euc '
    test -z "$(ls -A /run/secrets)"
    for name in control-database.key initial-admin-setup.token omni_data_at_rest.json; do
      test -f "/input/$name" && test ! -L "/input/$name"
    done
    cp /input/control-database.key /run/secrets/omni_control_database_key
    cp /input/initial-admin-setup.token /run/secrets/omni_initial_admin_setup_token
    cp /input/omni_data_at_rest.json /run/secrets/omni_data_at_rest_attestation.json
    chown 10001:10001 /run/secrets/*
    chmod 0400 /run/secrets/*
  '
```

この volume は Colima VM のディスク上に残ります。Mac 側の原本も `0600` のまま保管し、鍵を失わないようにしてください。読み取り権限のエラーが出た場合は、Mac 側の権限を緩めず停止して Colima の共有設定を確認します。attestation を更新する際は、新しい内容を確認したうえで VM 内の対応ファイルを更新し、所有者 `10001:10001` と mode `0400` を維持します。

## 4. 起動前の確認と初回起動

次はリポジトリのルートで実行します。初回は `--env-file` と四つの `-f` を明示し、**最後**に Mac 専用設定を指定します。Admin 作成後は bootstrap を外した三つの `-f` にします。`config` は起動せず、解決済みの設定を表示します。

```bash
cd "$REPO_DIR"
docker --context "$CONTEXT" compose \
  --env-file "$TEST_DIR/compose.env" \
  -f compose.yaml -f compose.bootstrap.yaml -f compose.local.yaml \
  -f "$TEST_DIR/compose.mac-test.yaml" config --format json
```

少なくとも次を確認し、違えば起動を中止します。

- `image` が公開された 2.0.0 の完全な digest で、`build` がない。
- `/app/data` が `type: volume`、`/run/secrets` が読み取り専用の `type: volume`、`/tmp` が `type: tmpfs`。Mac のパスを指す書き込み可能な bind mount がない。
- Web の公開先が **`127.0.0.1:4000` の一つだけ**。`4001` や `0.0.0.0` の公開がない。
- `pangolin_target` が `internal: true`、service がその network 一つだけに接続されている。
- `user: 10001:10001`、`read_only: true`、`cap_drop: [ALL]`、`no-new-privileges:true` が残っている。
- `secrets` に Mac のファイル由来の mount が残らず、初回だけ setup token の環境変数が設定される。control key と attestation の参照先は VM 内の `/run/secrets` である。

確認後、ビルドと再 pull を禁止して起動します。起動が失敗しても、Mac 側に `sudo chown` や `chmod 777` を行わず、原因を確認します。

```bash
docker --context "$CONTEXT" compose \
  --env-file "$TEST_DIR/compose.env" \
  -f compose.yaml -f compose.bootstrap.yaml -f compose.local.yaml \
  -f "$TEST_DIR/compose.mac-test.yaml" up -d --no-build --pull never
```

`http://localhost:4000/healthz` の応答を確認し、ブラウザでは `http://localhost:4000` を開きます。初回 Admin 作成には setup token が必要です。ブラウザで生成される recovery code は安全な場所に保管します。

Admin 作成後は bootstrap overlay を外した次の構成で再作成します。起動を確認してから、次のコマンドで VM 内の setup token だけを退役させます。対象 volume 名を二重確認し、control key と attestation は残します。Mac 側の `$TEST_DIR/secrets/initial-admin-setup.token` も不要になったことを確認して削除します。

```bash
docker --context "$CONTEXT" compose \
  --env-file "$TEST_DIR/compose.env" \
  -f compose.yaml -f compose.local.yaml \
  -f "$TEST_DIR/compose.mac-test.yaml" up -d --no-build --pull never --force-recreate
docker --context "$CONTEXT" run --rm --network none --read-only --user 0:0 \
  --entrypoint /bin/sh \
  --mount type=volume,source=omni-money-v2-test-secrets,target=/run/secrets \
  "$IMAGE" -euc 'test -f /run/secrets/omni_initial_admin_setup_token && rm /run/secrets/omni_initial_admin_setup_token'
```

## 5. 検証と終了

- `docker --context "$CONTEXT" ps` で公開先を確認し、同じ LAN の別端末から Mac の LAN アドレスの 4000 番に接続できないことを実測します。
- ブラウザの開発者ツールの Network で、アプリ操作中に意図しない外部ホストへの通信がないことを確認します。試験データを使い、ログやエラー応答に setup token、鍵、取引内容が出ないことを確認します。
- 試験用の二人のユーザーで、自分の家計簿だけを参照でき、Admin が他ユーザーの取引内容を閲覧できないことを確認します。
- この Mac の HTTP 検証は TrueNAS/Pangolin の TLS、公開 FQDN、ACL、更新・復旧手順の代わりにはなりません。TrueNAS に既存データがある場合は、暗号化済みの複製を使った隔離環境で更新経路を別途試します。

停止時は bootstrap を外した構成で `down` を使い、named volume は保持します。

```bash
docker --context "$CONTEXT" compose \
  --env-file "$TEST_DIR/compose.env" \
  -f compose.yaml -f compose.local.yaml \
  -f "$TEST_DIR/compose.mac-test.yaml" down
colima stop --profile omni-money-v2
```

`down -v`、`colima delete --data`、Mac 上の `sudo chown` はこの手順では使いません。再開時は bootstrap を外した構成を使います。検証用ファイルや volume の削除は、対象と復旧不要であることを確認してから個別に行います。
