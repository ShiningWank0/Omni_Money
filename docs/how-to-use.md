# Omni Money 利用ガイド（macOS / Docker / TrueNAS）

このガイドは、普段使う端末を Mac とし、Omni Money を次のいずれかの形で利用する手順を説明します。

| 利用形態 | 向いている用途 | アクセス方法 | ログイン |
| --- | --- | --- | --- |
| macOS デスクトップアプリ | 1 台の Mac だけで手軽に使う | `Omni Money.app` を起動 | 不要 |
| Colima 上の Docker | Mac 上でサーバーモードを試す、同じ LAN 内で共有する | Safari などで `http://localhost:4000` または Mac の IP アドレスへ接続 | 必要 |
| TrueNAS Custom App | 常時稼働させ、複数端末から利用する | Safari などで `http://<TrueNASのIP>:4000` へ接続 | 必要 |

迷う場合は、1 台だけで使うならデスクトップアプリ、複数端末で同じ家計簿を使うなら TrueNAS を選びます。デスクトップ版とサーバー版は別々の SQLite データベースを使うため、自動同期はされません。

## 1. macOS デスクトップアプリとして使う

### 1.1 インストール

1. [GitHub Releases](https://github.com/ShiningWank0/Omni_Money/releases/latest) を開きます。
2. 最新リリースの `omni-money-macos-v<バージョン>.zip` をダウンロードします。
3. ZIP を展開し、`Omni Money.app` を `/Applications` に移動します。
4. Finder の「アプリケーション」で `Omni Money.app` を開きます。

現在の配布物は ad-hoc 署名であり、Apple の公証は行っていません。macOS に起動を止められた場合は、まず Finder でアプリを Control キーを押しながらクリックし、「開く」を選択します。それでも開けない場合に限り、公式 Releases から取得したファイルであることを確認してから、ターミナルで次を実行します。

```bash
xattr -cr "/Applications/Omni Money.app"
open "/Applications/Omni Money.app"
```

### 1.2 データの保存場所

デスクトップ版のデータは次の場所に保存されます。

```text
~/Library/Application Support/OmniMoney/
├── omni_money.db
└── snapshots/
```

アプリを削除しても、このフォルダを削除しない限り家計簿データは残ります。ファイルを手動でコピーする場合は、先に Omni Money を終了してください。

### 1.3 基本操作

#### 取引を登録する

1. 画面右上の `+` を押します。
2. 日付と、必要であれば時刻を入力します。
3. 「資金項目」に `現金`、`普通預金`、`クレジットカード` などを入力します。新しい名前を入力すると、その資金項目が作られます。
4. 収入または支出を選び、項目名、金額、任意のメモを入力します。
5. 必要に応じてタグやレシート画像を追加し、保存します。

資金項目は独立した設定画面で先に作るのではなく、取引を登録した時点で一覧に現れます。金額は正の数で入力し、増減は「収入」「支出」で指定します。

#### 表示する資金項目を切り替える

画面左上の資金項目名を押し、表示対象にチェックを付けます。選択した資金項目の取引と合計残高が表示されます。「全選択」「全解除」も利用できます。

#### 検索、編集、削除

- 検索欄では項目名とメモを検索できます。
- 日付見出しを押すと、新しい順と古い順を切り替えられます。
- 取引の行を押すと編集画面が開き、内容の更新または削除ができます。

#### メニューから使える機能

左上のメニューボタンから次の機能を利用できます。

- CSV バックアップと CSV インポート
- クレジットカード設定と銀行口座設定
- 残高推移グラフ
- タグ別分析
- スナップショット管理

クレジットカードとして設定した資金項目は、現在残高と残高推移の計算から除外されます。銀行口座設定は、カード利用取引と引き落とし取引を紐付ける候補の判定に使われます。

### 1.4 バックアップと復元

取引の追加、更新、削除などを行うと SQLite データベースのスナップショットが自動作成され、最新 30 件が保持されます。メニューの「スナップショット管理」から過去の状態へ戻せます。復元するとデータベース全体がその時点へ戻るため、対象日時を確認して実行してください。

CSV バックアップには取引データが含まれますが、画像、タグ、各種設定、取引の紐付けは含まれません。完全なバックアップには、アプリを終了した状態で `OmniMoney` フォルダ全体を別の場所へコピーしてください。

CSV インポートでは次の 2 方式を選べます。

- 追加: 既存の取引を残して CSV の取引を追加します。
- 置換: 既存の取引を削除して CSV の取引に置き換えます。

置換を実行する前に、CSV とデータベースの両方をバックアップしてください。

### 1.5 アップデート

1. Omni Money を終了します。
2. 念のため `~/Library/Application Support/OmniMoney` をバックアップします。
3. 最新の ZIP を Releases から取得し、`/Applications/Omni Money.app` を置き換えます。
4. アプリを起動し、取引と残高が表示されることを確認します。

## 2. Docker サーバーモード共通の準備

Docker 版では、ブラウザにログイン画面が表示されます。起動時にログインパスワードの bcrypt ハッシュ `AUTH_PASSWORD_HASH` が必須です。平文パスワードを環境変数や YAML に記録しないでください。

### 2.1 bcrypt ハッシュを作成する

Docker が利用できる Mac で次を実行します。パスワードは対話形式で入力され、ターミナルの履歴には残りません。

```bash
docker run --rm -it httpd:2.4-alpine htpasswd -nBC 12 omni
```

2 回入力すると、次の形式で表示されます。

```text
omni:$2y$12$...
```

`omni:` より後ろの `$2y$12$...` 全体が `AUTH_PASSWORD_HASH` です。これはログイン時に入力するパスワードそのものではなく、パスワードの検証に使うハッシュです。

### 2.2 Docker 版の公開範囲

Docker の `4000:4000` は、通常すべてのホスト側ネットワークインターフェースにポートを公開します。Mac だけから使う場合は `127.0.0.1:4000:4000` とし、LAN 内の別端末から使う場合だけ `4000:4000` を使います。

ルーターで TCP 4000 番をインターネットへ直接ポート転送しないでください。自宅外から使う場合は、VPN または HTTPS を終端するリバースプロキシを利用します。

通常の利用では `AI_API_TOKEN` を設定しません。未設定なら AI 用 API は拒否されます。

## 3. Mac の Colima で Docker 版を使う

### 3.1 Colima と Docker CLI をインストールする

[Homebrew](https://brew.sh/) が利用できる状態で次を実行します。

```bash
brew install colima docker
colima start --cpu 2 --memory 4 --disk 20
```

起動確認:

```bash
colima status
docker version
docker run --rm hello-world
```

Colima はバックグラウンドの Linux VM 内で Docker を動かします。以降の `docker` コマンドは、その Colima 環境へ接続します。

### 3.2 データ保存先と認証情報を準備する

```bash
mkdir -p "$HOME/OmniMoneyServer/data"
mkdir -p "$HOME/.config/omni-money"
```

前節で作成した bcrypt ハッシュを、現在のターミナルにだけ設定します。値全体をシングルクォートで囲むと、`$` がシェルに展開されません。

```bash
export AUTH_PASSWORD_HASH='$2y$12$ここを作成したハッシュに置き換える'
```

再起動や更新時に同じハッシュを使えるよう、次のファイルへ保存します。保存されるのは平文パスワードではなく bcrypt ハッシュですが、ファイルは共有しないでください。

```bash
printf 'AUTH_PASSWORD_HASH=%s\nSESSION_MAX_AGE_HOURS=24\n' "$AUTH_PASSWORD_HASH" \
  > "$HOME/.config/omni-money/server.env"
chmod 600 "$HOME/.config/omni-money/server.env"
```

### 3.3 コンテナを起動する

まずは Mac からだけアクセスできる設定で起動します。

```bash
docker pull ghcr.io/shiningwank0/omni_money:latest

docker run -d \
  --name omni-money \
  --restart unless-stopped \
  --user "$(id -u):$(id -g)" \
  --env-file "$HOME/.config/omni-money/server.env" \
  -e TZ=Asia/Tokyo \
  -e DB_PATH=/app/data/omni_money.db \
  -p 127.0.0.1:4000:4000 \
  -v "$HOME/OmniMoneyServer/data:/app/data" \
  --health-cmd='wget -qO- http://127.0.0.1:4000/api/auth/status >/dev/null || exit 1' \
  --health-interval=30s \
  --health-timeout=5s \
  --health-start-period=10s \
  --health-retries=3 \
  ghcr.io/shiningwank0/omni_money:latest
```

`--user` はコンテナを Mac のユーザーと同じ UID/GID で動かし、データフォルダへ root 権限なしで書き込めるようにする設定です。

状態とログを確認します。

```bash
docker ps --filter name=omni-money
docker logs --tail 100 omni-money
```

### 3.4 ブラウザからアクセスする

Mac の Safari で次を開きます。

```text
http://localhost:4000
```

ログイン画面で、bcrypt ハッシュを作るときに入力した元のパスワードを入力します。ハッシュ文字列は入力しません。

### 3.5 同じ LAN の別端末からアクセスする

一度コンテナを作り直し、ポート指定を `-p 4000:4000` に変更します。

```bash
docker stop omni-money
docker rm omni-money
```

前節の `docker run` を再実行し、次の行だけ変更します。

```text
-p 4000:4000
```

Mac の IP アドレスは「システム設定」>「ネットワーク」から確認できます。Wi-Fi が `en0` の環境では次のコマンドでも確認できます。

```bash
ipconfig getifaddr en0
```

別端末のブラウザから `http://<MacのIPアドレス>:4000` を開きます。接続できない場合は macOS のファイアウォール設定と、端末同士が同じ LAN にいることを確認してください。

### 3.6 停止、再開、更新

```bash
# 停止と再開
docker stop omni-money
docker start omni-money

# ログ確認
docker logs -f omni-money
```

`latest` を更新する場合は、新しいイメージを取得してコンテナを作り直します。`$HOME/OmniMoneyServer/data` はコンテナの外にあるため、コンテナを削除してもデータは残ります。

```bash
docker pull ghcr.io/shiningwank0/omni_money:latest
docker stop omni-money
docker rm omni-money
# 3.3 の docker run を再実行
```

運用を固定したい場合は `latest` の代わりに、Releases と同じバージョンタグ（例: `0.1.11`）を指定します。Colima 自体を停止するときは、先にコンテナを停止してから `colima stop` を実行します。

## 4. TrueNAS の Custom App として使う

この手順は Docker ベースの Apps を使用する TrueNAS SCALE / TrueNAS Community Edition 24.10 以降を対象にします。画面名は TrueNAS のバージョンによって多少異なる場合があります。

### 4.1 専用データセットを作る

1. TrueNAS の「Datasets」で、使用するプール配下に `apps/omni-money` などの専用データセットを作成します。
2. このガイドではデータセットのパスを `/mnt/tank/apps/omni-money` とします。`tank` は実際のプール名に置き換えます。
3. データセットの ACL に `apps` ユーザー/グループ（UID/GID 568）を追加し、読み取り、書き込み、ディレクトリ移動ができる権限を付与します。

アプリでは Custom User `568:568` を明示して、このデータセットへ非 root で書き込みます。権限エラーが起きても `chmod 777` や Privileged モードで回避せず、データセットの ACL を修正してください。

### 4.2 TrueNAS 用のハッシュ表記に変換する

2.1 で作成した bcrypt ハッシュ内のすべての `$` を `$$` にします。Docker Compose が `$` を変数展開に使うためです。

```text
変換前: $2y$12$abc...
変換後: $$2y$$12$$abc...
```

### 4.3 Custom App を YAML で登録する

1. TrueNAS の「Apps」>「Discover Apps」を開きます。
2. 画面のメニューから「Install via YAML」を選びます。
3. Application Name に `omni-money` を入力します。
4. 次の YAML を貼り付けます。
5. データセットパス、bcrypt ハッシュ、必要であればイメージタグを置き換えます。

```yaml
services:
  omni-money:
    image: ghcr.io/shiningwank0/omni_money:latest
    restart: unless-stopped
    user: "568:568"
    environment:
      TZ: Asia/Tokyo
      DB_PATH: /app/data/omni_money.db
      HOST_IP: 0.0.0.0
      PORT: "4000"
      SESSION_MAX_AGE_HOURS: "24"
      AUTH_PASSWORD_HASH: '$$2y$$12$$ここを変換済みハッシュの残りに置き換える'
    ports:
      - "4000:4000"
    volumes:
      - /mnt/tank/apps/omni-money:/app/data
    healthcheck:
      test:
        - CMD-SHELL
        - wget -qO- http://127.0.0.1:4000/api/auth/status >/dev/null || exit 1
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

6. 「Save」を押し、Installed Apps で `omni-money` が Running / Healthy になるまで待ちます。
7. 起動しない場合は、アプリの Logs で `AUTH_PASSWORD_HASH` と `/app/data` の権限エラーを確認します。

本番運用では `latest` ではなく `0.1.11` のようなバージョンタグを指定すると、再デプロイ時に意図せずバージョンが変わるのを防げます。

### 4.4 Mac から TrueNAS へアクセスする

TrueNAS の管理画面を開くときに使っている IP アドレスが、例えば `192.168.1.20` なら、Mac の Safari で次を開きます。

```text
http://192.168.1.20:4000
```

ログイン画面で、bcrypt ハッシュ作成時に入力した元のパスワードを入力します。同じ LAN 上の別端末も同じ URL を利用できます。

ポート 4000 が他のアプリと重複する場合は、YAML の左側だけを変更します。例えばホスト側を 14000 番にする場合は `"14000:4000"` とし、`http://<TrueNASのIP>:14000` へアクセスします。

### 4.5 TrueNAS でのバックアップと更新

データベースとアプリ内スナップショットは、指定したデータセットに保存されます。

```text
/mnt/tank/apps/omni-money/
├── omni_money.db
└── snapshots/
```

次の両方を設定することを推奨します。

- Omni Money のメニューから CSV バックアップを定期的にダウンロードする。
- TrueNAS の Data Protection で、この専用データセットに Periodic Snapshot Task を設定する。

イメージを更新するときは、先にデータセットのスナップショットを作成し、Custom App の YAML でイメージタグを変更して再デプロイします。データセットを削除しない限り、アプリを削除または再作成しても家計簿データは残ります。

## 5. Docker 起動後の使い方

ログイン後の取引登録、資金項目の切り替え、検索、CSV、グラフ、タグ、スナップショット操作はデスクトップ版と同じです。サーバー版ではメニューに「ログアウト」が追加されます。

認証には次の制御があります。

- 認証済みセッションの既定有効期間は 24 時間です。
- 同じアクセス元から 5 回連続でログインに失敗すると、15 分間ロックされます。
- コンテナを再起動すると、メモリ上のログインセッションは失われるため再ログインが必要です。

## 6. トラブルシューティング

| 症状 | 確認すること |
| --- | --- |
| コンテナがすぐ終了する | `AUTH_PASSWORD_HASH` が未設定、空、または環境ファイルを読み込めていない可能性があります。ログを確認します。 |
| 正しいパスワードでログインできない | ハッシュの先頭から末尾まで保存されているか確認します。TrueNAS YAML では `$` を `$$` にします。5 回失敗後は 15 分待ちます。 |
| `/app/data` のエラーで起動しない | Colima では `--user "$(id -u):$(id -g)"`、TrueNAS では `user: "568:568"` とデータセット ACL を確認します。 |
| `localhost:4000` を開けない | `colima status`、`docker ps`、`docker logs omni-money` の順に確認します。 |
| 別の Mac から接続できない | `127.0.0.1:4000:4000` はローカル限定です。LAN 利用では `4000:4000` にし、ファイアウォールと IP アドレスを確認します。 |
| TrueNAS でポートが使用中になる | YAML のホスト側ポートを `14000:4000` など未使用の番号へ変更します。 |
| CSV 復元後に画像やタグが戻らない | CSV は取引データ用です。完全復元には SQLite/データセットのバックアップまたはスナップショットを使います。 |
| macOS がアプリを開かない | 公式 Release から取得したことを確認し、1.1 の Control + クリックまたは `xattr` 手順を使います。 |

## 7. 外部公開するときの注意

ポート 4000 をインターネットへ直接公開する構成は推奨しません。外出先からアクセスする場合は、少なくとも次の構成にします。

1. TrueNAS または家庭内ネットワークへ VPN で接続し、LAN 内の URL を開く。
2. または、信頼できるリバースプロキシで HTTPS を終端し、Omni Money は内部ネットワークだけで待ち受ける。
3. 長く推測されにくいパスワードを使用し、ルーターの不要なポート転送を削除する。
4. データセットの定期スナップショットと別媒体へのバックアップを行う。

## 8. 参照資料

- [Colima Installation](https://github.com/abiosoft/colima/blob/main/docs/INSTALL.md)
- [Colima README](https://github.com/abiosoft/colima/blob/main/README.md)
- [Docker: Port publishing and mapping](https://docs.docker.com/engine/network/port-publishing/)
- [TrueNAS: Installing Custom Apps](https://apps.truenas.com/managing-apps/installing-custom-apps/)
- [TrueNAS: Custom App Screens](https://www.truenas.com/docs/scale/apps/installcustomappscreens/)
- [TrueNAS: App Storage](https://apps.truenas.com/getting-started/app-storage/)
