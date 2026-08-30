# 安全な更新と限定ロールバック

Pangolin/TrueNAS の production Compose project では、同梱の
scripts/safe-update.sh を version tag または digest と一緒に使います。更新処理は
Linux + GNU tar + Bash の host contract を要求し、条件を満たさない場合は停止処理を
開始せず fail closed します。

## 固定された入力と Compose contract

- script の場所を基準に repository の compose.yaml と固定 project name omni-money
  を選び、-f、--project-directory、--project-name をすべての Compose 呼び出しへ
  明示します。環境中の COMPOSE_* は除去します。
- OMNI_UPDATE_ENV_FILE（既定 .env）は shell source せず owner-only の private copy
  へ pin します。device、inode、link count、SHA-256を保持し、途中の差替えを拒否します。
- source Compose file と attestation も owner/mode、device、inode、link count、digest
  を検証します。Compose 自身が生成した resolved JSON を一度だけ private snapshot に
  保存し、以後の ps --all、up --no-start、candidate/rollback 作成はその snapshot
  （rollback は image だけを差し替えた派生 snapshot）へ bind します。source が途中で
  変わっても live YAML を再解釈しません。
- service は container name omni-money、root filesystem read-only、CapDrop=ALL、
  no-new-privileges、published port 0、Pangolin network 1つでなければ拒否します。
  mount は resolved Compose と Docker inspect を突合し、/app/data の read-write bind、
  /tmp の tmpfs、base Compose が宣言した read-only secret mount だけを許可します。
  追加 bind、secret、network、port は拒否します。
- up --no-start の直後は必ず compose ps --all -q を読み、IDがちょうど1つで state が
  created（停止）であることを確認します。candidate と rollback のどちらも、network
  を全て disconnect してから start します。

## encrypted volume と checkpoint

OMNI_UPDATE_CHECKPOINT_DIR は廃止しました。ユーザーが任意の path を env に書いて
rollback root に昇格できないよう、checkpoint は実際の host data bind source の親に
ある固定名だけへ導出します。

update 専用の host attestation は root-owned private/read-only file とし、既存の server
at-rest attestation（container の data_root=/app/data）とは別の schema です。Compose の
`x-omni-update-attestation-file` extension で resolved snapshot にだけ path を残し、
service secret/mount として app container へ渡しません。

    {
      "version": 1,
      "protection": "external-encrypted-volume",
      "encrypted_volume_root": "/srv/omni-money",
      "data_root": "/srv/omni-money/data",
      "checkpoint_root": "/srv/omni-money/omni-money-update-checkpoints"
    }

data_root は Compose が解決した実 bind source と完全一致し、encrypted_volume_root と
checkpoint_root は root-owned attestation に記録された host path と一致しなければなり
ません。3 path は encrypted root の境界内、data と checkpoint は contained sibling、
同じ filesystem/device でなければなりません。各 path の既存 component は symlink で
なく通常 directory、group/other writable でなく、危険な root や /tmp 等でないことを
検証します。data の leaf は固定 service UID/GID 10001:10001、mode 0700 です。host の
project/env/attestation owner contract と、container/data の UID/GID contract は混同
しません。

既存の DATA_AT_REST_ATTESTATION_FILE をそのまま update attestation として使わないで
ください。container path と host path は異なるため、migration時に上記3つの host path
を root operator が記録し、root-owned OMNI_UPDATE_ATTESTATION_FILE として配置します。
暗号化 key はこの file や repository と別の secret manager/recovery 媒体で管理します。
attestation は暗号学的証明ではないため、LUKS/ZFS 等の unlock、backup、restore 試験も
運用で行います。

## 状態機械と rollback

停止直前に rollback を arm します。以後の stop、archive、candidate create/start、
healthcheck、env 更新、network connect のどれかが失敗した場合、EXIT/INT/TERM trap
は次の順で処理します。

1. pinned container ID を直接 stop し、partial stop でも安全停止を再試行する。
2. checkpoint の archive/checksum と data root の device/inode/link count、owner/mode
   を再検証する。tar の絶対 path、..、control-character/escape 名、symlink、hardlink、
   device、FIFO は作成時・復元前に拒否する。
3. data を checkpoint へ atomic に退避して restore し、restore 後も全 tree と leaf の
   owner/mode を検証する。
4. 旧 image を tag した rollback snapshot から stopped/created container を作り、
   network なしで start・configured user・実 UID/GID・health を検証してから固定 IP へ
   reconnect する。reconnect 失敗時は rollback container も stop し、service を
   isolated/stopped のまま artifact を残す。
5. env/config の pin 差替えは fail closed とする。env は安全な通常 file なら original
   pin を復元できるが、Compose source 差替え時も live file は使わず pinned resolved
   snapshot で rollback する。終了 status は失敗のままなので operator が source を確認
   する。

成功後も checkpoint と rollback image は自動削除しません。別 backup と restore 試験を
完了してから operator が明示的に retire してください。latest、local overlay（host
port付き）、down -v は使えません。

## 実行例

    chmod 700 scripts/safe-update.sh
    ./scripts/safe-update.sh ghcr.io/shiningwank0/omni_money:1.1.0

実行前に、data directory、固定 attestation、base Compose の secret file、暗号化 volume
が production contract に一致していることを確認します。

## CI / mock state machine

scripts/safe-update_test.sh は Docker daemon を使わず、Linux では mock Docker/Compose
state machine で main transaction を実行します。success、candidate failure、pull/config
の停止前失敗、partial stop、INT/TERM、network disconnect/reconnect failure、rollback
failure、env/Compose swap、candidate の追加 port/network/mount/secret、ps --all omission
を検証します。archive の newline 名、symlink、hardlink も fail-close を確認します。
macOS 等では portable preflight だけを実行し、production 更新は Linux CI/host で行って
ください。
