# TrueNAS Custom Appのアイコン

対象: TrueNAS Community Edition 25.10.7（Goldeye）。Web版と同じfaviconを、TrueNASのインストール済みApps画面にも登録できます。

## 仕組みと対応範囲

この版のUIはアプリの `metadata.icon` を画像として表示します。Custom AppのCompose YAMLやDocker imageのlabelに `icon` を追記しても、ここは設定されません。公開の `app.update` APIにもicon更新項目はありません。

そのため、TrueNASが管理するアプリmetadataの `icon` だけを編集します。これは通常のCustom App設定画面から行う操作ではなく、TrueNASの内部metadata形式に依存する手動の補助設定です。25.10.7の実装を照合していますが、実機での表示は別途確認してください。OS更新・アプリ再作成等でmetadataが再生成された場合は再登録が必要になることがあります。

画像は外部URLではなく、既存の `frontend/src/assets/favicon.svg` と同じbytesをdata URIにして登録します。TrueNASの画面を開くたびにOmni Moneyや外部サイトへ画像を取得しに行く必要はありません。Pangolinの認証例外、公開API、Docker socketやTrueNAS管理領域のcontainer mountも不要です。

## 登録データの生成

リポジトリのcheckoutで実行します。Python 3の標準libraryのみを使います。

```bash
python3 scripts/truenas_icon_metadata.py
```

出力される `  icon: "data:image/svg+xml;base64,..."` の1行をコピーします。`...` は説明上の省略で、実際の出力を省略せず使用します。値だけが必要な場合は `--data-uri` を指定します。

ツールは固定のWebアイコンだけを読み、ネットワーク接続もファイル更新も行いません。script、イベントハンドラー、foreignObject、外部参照、DOCTYPE/ENTITY等を含むSVGは拒否します。

## TrueNAS側で一度だけ登録

以下はアプリ名が `omni-money` の例です。TrueNASのApps画面にある実際のインストール名が異なる場合は、その名前のdirectoryを使います。root権限のあるTrueNAS Shellで操作します。

1. `/mnt/.ix-apps/app_configs/omni-money/metadata.yaml` を確認し、`custom_app: true` で対象アプリのmetadataであることを確認します。編集前にコピーを保存します。既存のbackupは上書きしません。

   ```bash
   sudo cp -an /mnt/.ix-apps/app_configs/omni-money/metadata.yaml /mnt/.ix-apps/app_configs/omni-money/metadata.before-omni-icon.yaml
   sudoedit /mnt/.ix-apps/app_configs/omni-money/metadata.yaml
   ```

2. 既存の `metadata:` の内側へ、生成した `icon:` の1行を追加します。すでに `icon:` がある場合は値を置き換えます。重複したkeyや別の `metadata:` は作りません。

   ```yaml
   custom_app: true
   metadata:
     # 既存のname/version等の項目は保持
     icon: "data:image/svg+xml;base64,..."
   # その他の既存項目も保持
   ```

3. 次の内部jobでmetadata cacheを再生成し、完了後にブラウザのApps画面を再読み込みします。このjob自体はアプリの更新・停止・再起動を行いません。

   ```bash
   sudo midclt call -job app.metadata.generate
   ```

戻す場合は `metadata.icon` の値だけを編集前の値へ戻すか、追加した `icon:` だけを削除し、同じjobで再読み込みします。アプリ更新後に古いmetadata全体を上書きするとversion等も戻るため、backup全体での復元は避けます。`versions/` 内のComposeファイルやアプリdata Datasetを編集する必要はありません。

## 照合したTrueNAS 25.10.7の一次資料

- [UIのmetadata.icon表示](https://github.com/truenas/webui/blob/TS-25.10.7/src/app/pages/apps/components/installed-apps/app-info-card/app-info-card.component.html)
- [Custom App更新APIの入力項目](https://github.com/truenas/middleware/blob/TS-25.10.7/src/middlewared/middlewared/api/v25_10_0/app.py)
- [アプリmetadataのpath](https://github.com/truenas/middleware/blob/TS-25.10.7/src/middlewared/middlewared/plugins/apps/ix_apps/path.py)
- [metadata生成job](https://github.com/truenas/middleware/blob/TS-25.10.7/src/middlewared/middlewared/plugins/apps/metadata.py)
