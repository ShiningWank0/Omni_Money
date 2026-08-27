# Omni Money 利用ガイド（macOS / Docker / TrueNAS）

このガイドは、普段使う端末を Mac とし、Omni Money を次のいずれかの形で利用する手順を説明します。

| 利用形態 | 向いている用途 | アクセス方法 | ログイン |
| --- | --- | --- | --- |
| macOS デスクトップアプリ | 1 台の Mac だけで手軽に使う | `Omni Money.app` を起動 | 不要 |
| Colima 上の Docker | Mac 上でサーバーモードを試す | Safari などで `http://localhost:4000` へ接続 | 必要 |
| TrueNAS + Pangolin/Newt | 常時稼働させ、安全に複数端末から利用する | PangolinのHTTPS公開URLへ接続 | Pangolin + Omni Money |

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

### 2.2 Omni Money側のTOTPを任意で登録する

Omni Money側のTOTPはオプションです。`AUTH_TOTP_SECRET_FILE` を設定しなければ、パスワードだけでログインでき、TOTPの未設定を理由に起動失敗することもありません。Pangolin突破後の防御を一段増やしたい場合だけ、以下の手順でOmni Money専用TOTPを有効にしてください。有効化する場合も、PangolinとOmni Moneyで同じseedを使わないでください。

helperはセットアップキーと`otpauth://` URIを表示した後、認証アプリの現在の6桁コードを確認します。確認に成功するまで秘密ファイルは作成しません。`--out` は必須で、既存ファイルやシンボリックリンクは上書きされません。

```bash
mkdir -p "$HOME/.config/omni-money"
go run ./cmd/omni-totp \
  --out "$HOME/.config/omni-money/omni-money-totp.secret" \
  --issuer "Omni Money" \
  --account "admin"
```

Google Authenticator、1Password、Aegis等へ登録し、表示された確認欄へ現在コードを入力してください。成功するとmode `0600`でファイルを作成します。ターミナル出力を消去し、キー・URIをパスワードと同じ機密情報として保管してください。`AUTH_TOTP_SECRET_FILE` にはキーそのものではなく、秘密ファイルのパスだけを設定します。

サーバーを直接起動する場合:

```bash
export AUTH_PASSWORD_HASH='$2y$12$...'
export AUTH_TOTP_SECRET_FILE="$HOME/.config/omni-money/omni-money-totp.secret"
export AUTH_REQUIRE_TOTP=true
export ALLOWED_HOSTS='localhost:4000,127.0.0.1:4000'
go run -tags server ./server.go
```

Dockerではイメージ内のhelperを、サーバーと同じ非rootユーザーで実行する方法を推奨します。これにより、ホストで作ったmode `0600`ファイルをコンテナUIDが読めない問題を避けられます。

```bash
docker run --rm -it \
  --user "$(id -u):$(id -g)" \
  -v "$HOME/OmniMoneyServer/data:/app/data" \
  --entrypoint /app/omni-totp \
  ghcr.io/shiningwank0/omni_money:latest \
  --out /app/data/omni-money-totp.secret \
  --issuer "Omni Money" --account admin
```

`AUTH_TOTP_SECRET_FILE` を設定すると、新しいセッションへのログインでTOTPが必須になります。通常操作中に定期的なTOTP入力は要求しません。有効なセッション内でCSV入出力・スナップショット復元等の高影響操作を行うときは、現在のOmni Moneyパスワードだけを再確認します。無操作・絶対期限・サーバー再起動でセッションが失効した後は新しいログインになるため、TOTPを設定していればパスワードとTOTPの両方が必要です。未設定ならTOTPは無効で、パスワード認証だけを使用します。`AUTH_REQUIRE_TOTP=true` はTOTPを有効化するスイッチではなく、「この環境では設定漏れによる無効化を許さない」という起動時の安全確認です。TOTPを採用した本番環境では併用を推奨します。秘密ファイルをローテーションする場合は、サービスを停止してから新しいファイルを別名で生成し、所有者・`0600`・マウント先を確認して再起動してください。CLIは既存ファイルを上書きしません。

認証端末を失った場合の復旧はホスト管理権限を使います。まずPangolin Resourceを無効化してOmni Moneyを停止し、`AUTH_TOTP_SECRET_FILE`を空、`AUTH_REQUIRE_TOTP=false`にして一時的にpassword-onlyで再起動します。新しいseedを生成・確認して設定を戻し、全セッションとPangolin側セッションを失効させてからResourceを再度有効化してください。現在は回復コードを発行しません。ネイティブWindowsサーバーでは秘密ファイルDACLを安全に検証できないためTOTPはfail-closedします。WindowsホストではDocker版を利用してください。

### 2.3 Docker 版の公開範囲

Docker の `4000:4000` は、通常すべてのホスト側ネットワークインターフェースにポートを公開します。この指定は使用しません。Macだけから使う場合は`127.0.0.1:4000:4000`とし、別端末からはPangolinまたはVPN経由で接続します。サーバーは非loopback平文HTTPを既定で起動拒否します。

同梱イメージの既定ユーザーは固定UID/GID `10001:10001`です。ネイティブLinuxで`./data:/app/data`をbind mountする場合は、初回起動前にホスト側directoryを`10001:10001`所有・mode `0700`にします。TrueNASや明示的な`user:`を使う場合はそのUID/GIDだけに同等のACLを与えます。`chmod 777`やPrivileged modeは使いません。

ルーターで TCP 4000 番をインターネットへ直接ポート転送しないでください。自宅外から使う場合は、VPN または HTTPS を終端するリバースプロキシを利用します。

通常の利用では `AI_CREDENTIALS_FILE` を設定しません。未設定なら AI 専用リスナー自体が起動しません。

### 2.4 AI APIを利用する場合の境界

AI APIを有効にすると、公開Webの4000番とは別にAI専用の4001番が使われます。

- 通常起動ではAI専用リスナーは `127.0.0.1:4001` だけで待ち受けます。
- 公開Webの `/api/v1/ai/*` は利用できず、ログイン済みでも404になります。
- AI専用ポートには通常の家計簿API、ログインAPI、画面配信を登録しません。
- AI専用の2エンドポイントは、期限・scope・口座・許可タグID制約を持つBearer資格情報とPOSTを必須とします。
- 分析は既定で単一口座・最大30日の集計だけを返し、明細とメモには追加scopeが必要です。
- クラウドLLMへAIトークンを渡さず、ローカルの仲介プロセスが `127.0.0.1:4001` を呼び出します。

ローカルで資格情報を作成する例です。失効日時は発行開始から90日以内を指定し、口座名は実在するものへ置き換えます。

```bash
umask 077
mkdir -p secrets
go run ./cmd/ai-credential issue \
  --file secrets/ai_credentials.json \
  --id local-console \
  --expires-at '<RFC3339-within-90-days>' \
  --scope transactions:create \
  --scope analysis:summary \
  --scope analysis:transactions \
  --scope console:relay \
  --account '現金' \
  --tag-id 1 \
  --analysis-start-date '<YYYY-MM-DD>' \
  --analysis-end-date '<YYYY-MM-DD>' \
  --max-analysis-days 30 \
  --max-results 100 \
  --max-transactions-per-day 100 > secrets/ai_console_token
```

`--tag-id` は、その資格情報で取引へ付与したり分析条件に指定したりできる既存タグIDです。必要なタグだけを繰り返し指定してください。省略した場合はタグ操作を許可しません。

AIの取引追加は16〜128文字の `Idempotency-Key` ヘッダーを必須とします。タイムアウト等で結果が不明な場合は、同じ本文と同じkeyを再送してください。再送は同じ結果を返し、日次上限を重ねて消費しません。同じkeyを別の本文へ再利用すると409、`--max-transactions-per-day` で指定したUTC日次上限を超えると `Retry-After` 付きの429になります。raw keyはDBや監査ログへ保存されません。

Dockerでは資格情報、console token、CA、サーバー証明書、クライアント証明書をリポジトリ外に準備し、`docker compose -f compose.yaml -f compose.ai.yaml up -d --build` で起動します。コンテナ内の非ループバック待受はTLS 1.3とmTLSが揃わない限り起動を拒否し、ホスト側は `127.0.0.1:4001` に限定されます。証明書の検証名は既定で `omni-money-ai` です。

資格情報の更新にはCLIの `rotate` または `revoke` を使います。通常ファイルを直接参照するサーバーでは、原子的な置換後に `SIGHUP` を送ると再読込し、失敗時は直前の有効な資格情報を維持します。Compose Secretsでは実行中コンテナが古いinodeを参照し続ける場合があるため、更新後に `docker compose -f compose.yaml -f compose.ai.yaml up -d --force-recreate omni-money` で再作成してください。生トークンは発行・rotation成功時の標準出力に一度だけ表示されるため、ターミナル履歴やログへ貼り付けないでください。

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
printf 'AUTH_PASSWORD_HASH=%s\nALLOWED_HOSTS=localhost:4000,127.0.0.1:4000\nALLOW_INSECURE_HTTP=true\nSESSION_MAX_AGE_HOURS=8\nSESSION_IDLE_TIMEOUT_MINUTES=15\nSESSION_REAUTH_MAX_AGE_MINUTES=5\nSESSION_MAX_CONCURRENT=3\n' "$AUTH_PASSWORD_HASH" \
  > "$HOME/.config/omni-money/server.env"
chmod 600 "$HOME/.config/omni-money/server.env"
```

### 3.3 コンテナを起動する

まずは Mac からだけアクセスできる設定で起動します。

同梱のCompose構成ではroot filesystemをread-onlyにし、`/app/data` だけを永続書き込み可能にします。`/tmp` は128 MiBのtmpfsで、CPU 2、memory 1 GiB、PID 256を既定上限にします。変更する場合は `.env` の `OMNI_CPU_LIMIT`、`OMNI_MEMORY_LIMIT`、`OMNI_PIDS_LIMIT`、`OMNI_TMPFS_SIZE` を設定し、画像・snapshot・CSVを含む動作確認を行ってください。

```bash
docker pull ghcr.io/shiningwank0/omni_money:latest

docker run -d \
  --name omni-money \
  --restart unless-stopped \
  --user "$(id -u):$(id -g)" \
  --env-file "$HOME/.config/omni-money/server.env" \
  -e TZ=Asia/Tokyo \
  -e DB_PATH=/app/data/omni_money.db \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m \
  --cap-drop ALL \
  --security-opt no-new-privileges \
  -p 127.0.0.1:4000:4000 \
  -v "$HOME/OmniMoneyServer/data:/app/data" \
  --health-cmd='wget -qO- http://127.0.0.1:4000/healthz >/dev/null || exit 1' \
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

### 3.5 別端末からアクセスする

`-p 4000:4000`へ変更して平文HTTPを全interfaceに公開しないでください。別端末からはVPN、または7章のPangolin/Newt専用network構成を使い、HTTPSの公開URLへ接続します。

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

TOTPを使う場合は、TrueNAS上のShell（または同じACLを持つ安全な管理端末）で秘密ファイルをデータセット直下へ新規作成します。Composeから見えるコンテナ内パスは `/app/data/omni-money-totp.secret` です。Pangolinのseedと同じファイルを指定しないでください。

```bash
docker run --rm -it --user 568:568 \
  -v /mnt/tank/apps/omni-money:/app/data \
  --entrypoint /app/omni-totp \
  ghcr.io/shiningwank0/omni_money:latest \
  --out /app/data/omni-money-totp.secret \
  --issuer "Omni Money" --account "admin"
```

TrueNASのACLで、ファイルを読むアプリUID/GID 568だけに読み取り権限を付与します。CLIは既存ファイルを上書きしないため、再生成時はサービスを停止し、新しい別名ファイルを作成してから設定を切り替えます。

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
      SESSION_MAX_AGE_HOURS: "8"
      SESSION_IDLE_TIMEOUT_MINUTES: "15"
      SESSION_REAUTH_MAX_AGE_MINUTES: "5"
      SESSION_MAX_CONCURRENT: "3"
      AUTH_PASSWORD_HASH: '$$2y$$12$$ここを変換済みハッシュの残りに置き換える'
      ALLOWED_HOSTS: "192.168.1.20:4000"
      ALLOW_INSECURE_HTTP: "true"
      # TOTPを使う場合だけ、次の2行を追加する。
      # AUTH_TOTP_SECRET_FILE: /app/data/omni-money-totp.secret
      # AUTH_REQUIRE_TOTP: "true"
    ports:
      # LAN内だけの例。実際のTrueNAS固定IPへ置換する。
      - "192.168.1.20:4000:4000"
    read_only: true
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    tmpfs:
      - /tmp:rw,noexec,nosuid,nodev,size=16m
    volumes:
      - /mnt/tank/apps/omni-money:/app/data
    healthcheck:
      test:
        - CMD-SHELL
        - wget -qO- http://127.0.0.1:4000/healthz >/dev/null || exit 1
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

6. 「Save」を押し、Installed Apps で `omni-money` が Running / Healthy になるまで待ちます。
7. 起動しない場合は、アプリの Logs で `AUTH_PASSWORD_HASH` と `/app/data` の権限エラーを確認します。

本番運用では `latest` ではなく `0.1.11` のようなバージョンタグを指定すると、再デプロイ時に意図せずバージョンが変わるのを防げます。

このTrueNAS例はTOTPを既定で要求せず、AI用4001番も公開しません。これは固定LAN IPへ限定した移行用の平文HTTP例であり、インターネット公開には使用できません。Pangolin運用では`ports:`を削除し、7章の専用networkへ接続してください。

### 4.4 Mac から TrueNAS へアクセスする

TrueNAS の管理画面を開くときに使っている IP アドレスが、例えば `192.168.1.20` なら、Mac の Safari で次を開きます。

```text
http://192.168.1.20:4000
```

ログイン画面で、bcrypt ハッシュ作成時に入力した元のパスワードを入力します。同じ LAN 上の別端末も同じ URL を利用できます。

ポート4000が他のアプリと重複する場合は、publish先とHost allowlistを必ず同時に変更します。例えばホスト側を14000番にする場合は`"192.168.1.20:14000:4000"`、`ALLOWED_HOSTS: "192.168.1.20:14000"`とし、`http://192.168.1.20:14000`へアクセスします。

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

- 認証済みセッションの既定の絶対有効期間は8時間、無操作タイムアウトは15分です。同時セッションは1ユーザーあたり3件までで、ブラウザ画面も無操作時に自動ロックします。可視タブで実際に操作している間は低頻度のheartbeat（最大4分間隔、操作停止後の末尾送信）がサーバー側の無操作時刻も更新します。非表示タブからは送信せず、8時間の絶対期限は延長しません。
- 通常の取引・口座・残高・画像・タグの閲覧と編集では、ログイン後に認証情報を繰り返し要求しません。CSVバックアップ／インポート、手動スナップショット作成・復元、AI操作、全セッションログアウトだけが、直近5分以内のOmni Moneyパスワード再確認を要求します。
- TOTPは新しいセッションへのログイン時だけ要求します。有効なセッション内の高影響操作はパスワードだけで再確認し、無操作・絶対期限・再起動後の新しいログインでは、TOTPを設定していれば再びパスワードとTOTPの両方が必要です。
- 同じアクセス元から 5 回連続でログインに失敗すると、15 分間ロックされます。
- bcryptパスワードハッシュはコスト12〜16が必要です（低すぎるハッシュと、CPU枯渇を招く過大なハッシュは起動時に拒否します）。TOTPコードは30秒単位で検証され、同一プロセス内では受理済みタイムステップを再利用できません。
- コンテナを再起動すると、メモリ上のログインセッションは失われるため再ログインが必要です。

TOTPのリプレイ防止状態は現在のプロセスのメモリ上だけで管理されます。再起動後に過去のタイムステップを長期的に記録して照合する仕組みではないため、これは防御の限界です。TOTP秘密ファイルを持つ攻撃者、またはOmni Moneyのパスワードと認証アプリを同時に奪われた場合は、TOTPだけでは防げません。秘密ファイル・Pangolinのseed・パスワードを別々に管理し、侵害時はサービス停止、秘密のローテーション、Pangolin側のセッション失効を行ってください。

### 5.1 AI API操作画面

サーバーモードでは、メニューの「クレジットカード設定」の直下に「AI API操作」が表示されます。`AI_CREDENTIALS_FILE` または `AI_CONSOLE_TOKEN_FILE` が未設定の場合、送信時にAI専用APIが無効であることを表示します。

1. 「AI API操作」を開きます。
2. 「取引追加」または「分析」を選びます。
3. JSONリクエストを編集します。
4. 「AI専用入口へ送信」を押し、HTTP statusとJSONレスポンスを確認します。

ブラウザはAI用Bearer tokenを保持しません。通常Webのセッション認証を通過したリクエストを、サーバー側が固定されたAI専用リスナーへ転送します。任意URLへの転送はできません。

この画面はサーバーモード専用です。デスクトップ版では表示されません。また、LLM providerのAPIキーを入力する画面ではありません。ローカルLLM・クラウドLLMとの自動連携は、[AI連携ロードマップ](ai-integration-roadmap.md)に記載した別プロセスのAdapterから行います。

## 6. トラブルシューティング

| 症状 | 確認すること |
| --- | --- |
| コンテナがすぐ終了する | `AUTH_PASSWORD_HASH` が未設定、空、または環境ファイルを読み込めていない可能性があります。ログを確認します。 |
| 正しいパスワードでログインできない | ハッシュの先頭から末尾まで保存されているか確認します。TrueNAS YAML では `$` を `$$` にします。5 回失敗後は 15 分待ちます。 |
| `/app/data` のエラーで起動しない | Colima では `--user "$(id -u):$(id -g)"`、TrueNAS では `user: "568:568"` とデータセット ACL を確認します。 |
| `localhost:4000` を開けない | `colima status`、`docker ps`、`docker logs omni-money` の順に確認します。 |
| 別の Mac から接続できない | `127.0.0.1:4000:4000` はローカル限定です。全interfaceへ変更せず、PangolinまたはVPNを構成します。 |
| TrueNAS でポートが使用中になる | 固定Host IP側を`192.168.1.20:14000:4000`等へ変更し、`ALLOWED_HOSTS`も同じHost・portへ更新します。 |
| CSV 復元後に画像やタグが戻らない | CSV は取引データ用です。完全復元には SQLite/データセットのバックアップまたはスナップショットを使います。 |
| macOS がアプリを開かない | 公式 Release から取得したことを確認し、1.1 の Control + クリックまたは `xattr` 手順を使います。 |

## 7. 外部公開するときの注意

ポート 4000 をインターネットへ直接公開する構成は推奨しません。外出先からアクセスする場合は、少なくとも次の構成にします。

1. TrueNAS または家庭内ネットワークへ VPN で接続し、LAN 内の URL を開く。
2. または、信頼できるリバースプロキシで HTTPS を終端し、Omni Money は内部ネットワークだけで待ち受ける。
3. 長く推測されにくいパスワードを使用し、ルーターの不要なポート転送を削除する。
4. データセットの定期スナップショットと別媒体へのバックアップを行う。

リバースプロキシ経由で公開する場合は、ブラウザからPangolinまでのTLSを必須にし、Omni Money側で `FORCE_HTTPS=true`、実際の公開名だけを `ALLOWED_HOSTS`、最後にOmni Moneyへ接続するプロキシの固定IPまたは最小CIDRだけを `TRUSTED_PROXIES` に設定してください。Pangolinには公開FQDNの `Host` を維持させ、外部から届いた転送ヘッダーを破棄したうえで、単一の `X-Forwarded-Proto: https` と正規化済みの `X-Forwarded-For` を付けさせます。Omni Moneyは曖昧な複数値を拒否します。`CORS_ALLOWED_ORIGINS` は、別オリジンのフロントエンドを意図的に使う場合以外は空のままにします。`TRUSTED_PROXIES` を広いネットワークへ設定したり、外部由来の `X-Forwarded-*` をそのまま転送したりしないでください。

PangolinではPublic ResourceにPlatform SSOが標準で有効ですが、対象ユーザーまたはRoleを明示的に割り当ててください。Omni MoneyのResourceには `Allow / Bypass Auth` ルール、認証不要のShareable Link、長期Access Tokenを作らず、例外ルールが必要なら `Pass to Auth` または `Deny` を使います。`Allow` ルールは一致したリクエストについてPangolin認証を完全に省略する機能です。

NewtとOmni MoneyをDockerで動かす場合の最小露出構成は、両コンテナだけが参加する専用networkを作り、Pangolinのtargetを `http://omni-money:4000` のようなコンテナ名で手動設定し、Omni Moneyの `ports:` をホストへ公開しない形です。Docker自動検出が不要なら、NewtへDocker socketを渡さず、Docker Blueprintも無効にしてください。自動検出を使う場合、Docker socketは強い権限を持つため、可能なら許可APIを絞ったDocker Socket Proxyを使い、Newt側の `DOCKER_ENFORCE_NETWORK_VALIDATION=true` も有効にします。この検証機能は標準では `false` で、Newtとtargetが同じDocker network上にあることを検査します。Newtをhost network modeで動かすと、この検証は使えません。

同梱の`compose.yaml`はこの構成を実装済みです。`.env`へ公開FQDN（ポートが標準443ならホスト名だけ）を設定してOmni Moneyを起動します。

```dotenv
ALLOWED_HOSTS=money.example.com
TRUSTED_PROXIES=172.30.240.3/32
```

```bash
docker compose up -d --build
# 空でなければ安全境界が崩れているため、設定を直す。
docker compose port omni-money 4000
```

Newtが別Composeにある場合、そのserviceに次をマージします。Newtは通常のegress可能networkを維持しつつ、Omni Moneyのexternal networkへ固定IPで参加します。

```yaml
services:
  newt:
    environment:
      DOCKER_ENFORCE_NETWORK_VALIDATION: "true"
    networks:
      default: {}
      pangolin_target:
        ipv4_address: 172.30.240.3

networks:
  pangolin_target:
    external: true
    name: omni-money-pangolin
```

PangolinのResource targetを`http://omni-money:4000`にし、公開Hostを維持して単一の`X-Forwarded-Proto: https`を渡すことを確認します。既存Docker networkと`172.30.240.0/28`が衝突する場合は、`OMNI_PANGOLIN_SUBNET`、`OMNI_MONEY_IP`、Newtの固定IP、`TRUSTED_PROXIES`を同じ未使用subnet内へまとめて変更します。`TRUSTED_PROXIES`はNewtの正確な`/32`のままにしてください。

Pangolinの公開TLSとNewt tunnelはインターネット区間を保護しますが、targetをHTTPにした場合、NewtからOmni Moneyまでの専用Docker network内はHTTPです。そのローカルnetworkも信頼できない場合は、Omni Moneyの `TLS_CERT_FILE` / `TLS_KEY_FILE` とPangolin側のHTTPS target・証明書検証を構成してください。`FORCE_HTTPS=true` はブラウザ側schemeとSecure Cookieを強制する設定であり、内部target通信そのものを暗号化する機能ではありません。

Pangolinの認証は外側のアクセス制御、Omni Moneyのパスワードは内側の独立した認証境界です。Omni Money側のTOTPを任意で有効にした場合は、さらに独立した要素が加わります。Pangolinの認証を突破されても、Omni Moneyの認証を省略できる構成にはなりません。ただし、Omni Money自体を直接インターネットへ公開すること、有効化した両方のseedを同じ場所へ保存すること、Omni Moneyの認証情報をログやLLMへ渡すことは避けてください。

## 8. 参照資料

- [Colima Installation](https://github.com/abiosoft/colima/blob/main/docs/INSTALL.md)
- [Colima README](https://github.com/abiosoft/colima/blob/main/README.md)
- [Docker: Port publishing and mapping](https://docs.docker.com/engine/network/port-publishing/)
- [Pangolin: Public Resource Authentication](https://docs.pangolin.net/manage/resources/public/authentication)
- [Pangolin: Access Control Rules](https://docs.pangolin.net/manage/access-control/rules)
- [Pangolin: Configure Sites / Docker Network Validation](https://docs.pangolin.net/manage/sites/configure-site)
- [Pangolin: Multi-Factor Authentication](https://docs.pangolin.net/manage/access-control/mfa)
- [TrueNAS: Installing Custom Apps](https://apps.truenas.com/managing-apps/installing-custom-apps/)
- [TrueNAS: Custom App Screens](https://www.truenas.com/docs/scale/apps/installcustomappscreens/)
- [TrueNAS: App Storage](https://apps.truenas.com/getting-started/app-storage/)

## 9. 開発用Dockerテスト環境の確認と削除

以下は手動で実行する運用コマンドです。このガイド追加時にはコンテナの停止・削除を自動実行していません。

現在の状態、公開ポート、ヘルスチェックを確認します。

```bash
docker --context colima compose ps
docker --context colima port omni-money
docker --context colima inspect --format '{{.State.Health.Status}}' omni-money
docker --context colima logs --tail 100 omni-money
```

テスト用Composeコンテナとネットワークを終了・削除します。bind mountしたデータはこのコマンドだけでは削除されません。

```bash
docker --context colima compose down
```

今回のテストで `/tmp/omni-pr54-compose-data` を使用した場合、内容が不要であることを確認してから利用者自身で削除します。

```bash
rm -rf /tmp/omni-pr54-compose-data
```

他のコンテナを使っていない場合だけ、必要に応じてColimaを停止します。

```bash
colima stop
```

`docker compose down -v` は名前付きvolumeも削除するため、データ保持が必要な環境では使用しないでください。
