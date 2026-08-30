# 安全な更新と限定ロールバック

Pangolin/TrueNAS の production Compose project では、同梱の
scripts/safe-update.sh を version tag または digest と一緒に使います。更新処理は
Linux + Bash 3.2以降 + GNU tar の host contract を要求し、条件を満たさない場合は停止処理を
開始せず fail closed します。

## 固定された入力と Compose contract

- compose.yaml の top-level `name: omni-money` を project identity の source of truth とし、
  script の場所を基準に repository の compose.yaml を選びます。safe-update は
  `-f`、`--project-directory`、`--project-name omni-money` をすべての Compose 呼び出しへ
  明示します。環境中の COMPOSE_* は除去します。
- OMNI_UPDATE_ENV_FILE（既定 .env）は shell source せず owner-only の private copy
  へ pin します。device、inode、link count、SHA-256を保持し、途中の差替えを拒否します。
  Composeのshell変数優先順位による差替えも避けるため、repositoryの`OMNI_*`補間変数は
  呼び出し前に除去し、選択したprivate env fileを唯一のCompose環境入力にします。
  dotenvは停止前に安全なsingle-line subsetへ限定し、`KEY=value`以外のcolon形式、assignment
  前後の空白、未終端/multiline quote、展開・escape構文を拒否します。
- image 引数は最後の path component に明示的な immutable version tag を持つか、完全な
  `@sha256:<64 hex>` digest でなければなりません。`registry:5000/image` は port を
  tag と誤認しないよう拒否し、`latest` も拒否します。
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
- service userはComposeで数値の`10001:10001`へ固定します。candidate image内の`id`や
  `/etc/passwd`は検証に使わず、Docker hostが保持する`Config.User`と空の`GroupAdd`を
  inspectして、named USERによるUID 0偽装やsupplementary group付与を拒否します。既存の
  containerが`User=omni`等のnamed userなら、safe-updateを使う前にこのCompose定義で一度
  計画停止・再作成し、数値userへ移行してください。
- container runtimeは`runc`へ固定し、ComposeのGPU request・device cgroup ruleを許可しません。
  Docker inspectでも`HostConfig.Runtime=runc`、空の`DeviceRequests`/`DeviceCgroupRules`を
  current、candidate、rollbackのruntime contractとして比較します。
- up --no-start の直後は必ず compose ps --all -q を読み、IDがちょうど1つで state が
  created（停止）であることを確認します。candidate と rollback のどちらも、network
  を全て disconnect してから start します。
- runtime contract は current の operator environment（値ではなくdigest）、entrypoint/cmd、
  healthcheck、mount/network/labelに加えて capability、device、PID/IPC namespace、resource
  limit、log設定まで pinします。`CapAdd`、device、共有namespace、危険なresource/log設定は
  candidate/rollbackとも拒否し、imageが更新してよい `VERSION` 等のimage-owned defaultだけを
  operator contractから除外します。
- Docker操作とlocal filesystemのattestationは同じhostでなければなりません。`DOCKER_HOST`、
  TLS/cert override、remote `DOCKER_CONTEXT`、Unix socketでないdefault context、及び
  `TAR_OPTIONS` は拒否します。
- 停止前に実Docker networkのID、name、internal flag、bridge driver、IPAM driver/subnetを
  inspectしてdurable recovery bundleへ固定します。candidate/rollbackを接続する直前にも
  同じ実体であることを再検証し、同名networkの差替えを拒否します。

## encrypted volume と checkpoint

OMNI_UPDATE_CHECKPOINT_DIR は廃止しました。ユーザーが任意の path を env に書いて
rollback root に昇格できないよう、checkpoint は実際の host data bind source の親に
ある固定名だけへ導出します。

update 専用の host attestation は root-owned private/read-only file とし、既存の server
at-rest attestation（container の data_root=/app/data）とは別の schema です。Compose の
`x-omni-update-attestation-file` extension で resolved snapshot にだけ path を残し、
相対pathはCompose project directory（このrepositoryのcompose.yamlの親）を基準に解決し、
`.env`の値だけで別rootへ向けることはできません。

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

archive/checksumの一時fileはpin directoryやrepositoryではなく、attestation済みcheckpoint
filesystem内のgeneration directoryへ0600で作成し、同じdirectoryからchecksum/archiveへ
exclusive renameします。checkpoint filesystemにはarchive、extract、failed-dataを保持できる
保守的な容量をrollback reservationとしてfallocateし、reservation自体もowner/mode、
device/inode/link countと正確な長さでjournalへ固定し、要求blockが実際に割り当てられたことを
確認します。大容量reservationの内容digestは計算しません（bytes自体に復旧価値がないためです）。

## 状態機械と rollback

停止直前に rollback を arm します。以後の stop、archive、candidate create/start、
healthcheck、env 更新、network connect のどれかが失敗した場合、EXIT/INT/TERM trap
は次の順で処理します。

1. rollback用journalを書き換える前にcandidate/currentのpinned container IDを直接stopし、
   partial stopでも安全停止を再試行する。candidateのingress接続が試行済みなら、停止後に
   pinned network IDからdisconnectしてnetwork 0を確認する。このisolation完了後だけ
   `rollback-stopped`をdurable journalへ記録する。journal更新が失敗した場合はdataへ触れず、
   lock/journal/recovery bundleを残してmanual recoveryへ移行する。
   rollback入口ではまずDocker CLI/socketとstop/disconnectに必要な最小parserだけを
   pinned identityで確認します。これによりtar/find等の復旧toolが途中で変わっていてもcontainer
   isolationを先に完了できます。isolation後、全toolchainを再検証し、失敗時はdata/envへ触れず
   last known-good pinを含む全artifactを保持します。
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

candidateをingressへ接続する直前にdurable journalを`uncertain`へ更新します。connectが一度でも
試行された、または結果を確定できない場合、candidateは停止・network切断後、そのdata treeを
`failed-candidate-data`へidentity付きで隔離します。この境界を越えた後はpre-update archiveを
自動restoreせず、archiveと隔離dataのmanual reconciliationを要求します。外部requestに応答した
candidateのwriteを自動rollbackで失うことを防ぐためです。

成功後も checkpoint と rollback image は自動削除しません。別 backup と restore 試験を
完了してから operator が明示的に retire してください。latest、local overlay（host
port付き）、down -v は使えません。

## 電源断と手動復旧

safe-update は root operator として実行します。Compose file、選択した env、host
attestation、2つの Compose secret source は、root または root が管理する親ディレクトリ
に置き、symlink ではない通常 file、指定された owner/mode、書込み不可の状態にします。
control keyは固定service GIDが読むため `root:10001`・`0440`、attestationは
`root:root`・`0444`を標準とします。control keyとattestationは同じpermissive matrixに
せず、sourceごとにこのowner/modeを厳密に要求します。
live data は固定 UID/GID `10001:10001`・mode `0700`、checkpoint root は root-owned
mode `0700` で、いずれも同じ暗号化 filesystem 上に置きます。例えば配置を変更した
場合は、先に `sudo chown`/`sudo chmod` と attestation の3 pathを更新し、dry-run相当の
preflight（安全更新テスト）を通してから実行します。実行例は次の通りです。

    sudo ./scripts/safe-update.sh ghcr.io/shiningwank0/omni_money:1.1.0

停止前に checkpoint directory の `recovery/` と `.safe-update-journal` へ、Compose
snapshot、env/attestationのprivate copy、secret source contract、current runtime
contract、rollback Compose snapshot、各copyのdevice/inode/link count/digest manifestを
atomic write し、各fileと親directoryを fsyncします。archive/checksum/reservationのidentity
もphase更新前に記録します。envの更新・復元は元envと同じ親directory上のprivate stagingから
fsync後にatomic renameします。journal phaseが `stopping` 以降で更新途中にSIGKILL・電源断が起きた場合も、
lockやjournalは自動削除しません。次回実行は `lock` または journal を検出して fail
closed します。これは古いlockを消して二重復旧する事故を防ぐためです。

復旧時は root operator が、journalのphaseと `recovery/` の全fileについて owner/mode、
device/inode/link count、SHA-256、attestation/data/checkpointの境界、Dockerの pinned
container ID/image/networkを照合します。`checkpoint/data.tar` と checksumを検証し、
必要なら既存の candidate/rollback を直接停止して checkpoint を展開し、data treeの
全entryが `10001:10001` と許可されたmodeであること、旧runtime contractとhealth/network
が一致することを確認してからserviceを再接続します。どれか1つでも一致しない場合は、
lock/journal/recovery bundleを削除せず、serviceを停止したまま管理者が調査します。
手動復旧が完了した後だけ、検証済みの pinned identity 内のlockとjournalを削除します。
`rm -rf` で project、data、checkpoint root全体を消去したり、journalだけを先に消去して
再実行したりしないでください。

## 実行例

    chmod 700 scripts/safe-update.sh
    sudo ./scripts/safe-update.sh ghcr.io/shiningwank0/omni_money:1.1.0

実行前に、data directory、固定 attestation、base Compose の secret file、暗号化 volume
が production contract に一致していることを確認します。

## CI / mock state machine

scripts/safe-update_test.sh は Docker daemon を使わず、Linux では mock Docker/Compose
state machine で main transaction を実行します。success、candidate failure、partial
Compose recreate（旧ID消失）、pull/config
の停止前失敗、partial stop、INT/TERM、network disconnect/reconnect failure、rollback
failure、env/Compose swap、candidateのdata改変、secret inode差替え、rollback tag改変、
追加 port/network/mount/secret、旧container IDのforce-recreate削除、stale lock/journalを
検証します。registry port/tag、relative attestation、remote Docker context/host、
TAR_OPTIONS、secret permissions、容量reservation、cross-filesystem staging、nested
directoryとhard-linked regular fileも確認します。archive の newline 名、symlink、hardlink、
FIFO、deviceも fail-closeを確認します。
macOS 等では portable preflight だけを実行し、production 更新は Linux CI/host で行って
ください。
