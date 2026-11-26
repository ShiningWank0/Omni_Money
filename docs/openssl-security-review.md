# OpenSSL 3.5.9更新の確認（2026-11-17）

macOS/Windowsの静的libcryptoを3.5.8から3.5.9へ更新する。
取得元は[公式3.5.9 release](https://github.com/openssl/openssl/releases/tag/openssl-3.5.9)、
source archiveのSHA-256は`603f5602e2eef00d77fbd429d34dcd5822bb301757a1bc9cdb24c670f1eb859a`。
両OSのbuild script・workflow・OSS記録を同じ版へ更新する。
3.5.8と3.5.9の公式LICENSE.txtは同一であり、確認済みApache-2.0本文とも
既存のURL正規化を通して一致する。配布対象やライセンス条件の変更はない。

## 更新が必要な理由

固定値とチェックサムはビルドの再現性・取得物の整合性を担保するが、古い版の脆弱性を修正しない。
[3.5.9は2026年9月29日のセキュリティ修正版](https://openssl-library.org/news/openssl-3.5-notes/index.html)で、
Highを含む修正があるため、3.5.8での固定PRとセットで適用する。
3.5系は[2030年4月8日までサポートされるLTS](https://openssl-library.org/policies/releasestrat/)である。
Windowsが以前取得していた3.6系への固定や、未検証の4.x移行をこの更新の代わりにしない。

## 公開済みadvisoryの残件

[2026年11月10日のadvisory](https://openssl-library.org/news/secadv/20261110.txt)には、
3.5.9も対象に含まれるLowの問題が5件ある。
AES-GCM-SIVのAAD/IV処理、CMS_encryptのEC recipient処理、QUICのaddress検出とCRYPTO frame処理に関するものである。
修正先として記載された3.5.10は、確認時点の[公式配布一覧](https://openssl-library.org/source/)と
GitHub releasesでは未公開である。存在しないreleaseの版・SHAを設定しない。

本アプリでOpenSSLを使う[SQLCipher 4.20.0のprovider](https://github.com/sqlcipher/sqlcipher/blob/v4.20.0/src/crypto_openssl.c)は、
AES-256-CBC、HMAC、PBKDF2、RAND_bytesを使用し、上記AES-GCM-SIV/CMS/QUICのAPIを呼び出さない。
配布用builderは静的libcryptoだけを組み込み、libsslは組み込まない。
この確認は現在のproviderでの利用範囲に基づく影響評価であり、3.5.9の既知脆弱性がゼロという意味ではない。
3.5.10以降が公開されたら公式advisoryと取得物を再確認し、版・SHA-256・OSS記録を更新する。

## 検証と今後の移行

Windows SQLCipher回帰、macOS Intel/ARM64・Windows本体のビルド、配布物のSQLCipher self-test、
リンク依存とOSS記録の一致をPR CIで確認する。ローカルのfixtureテストを実機検証の代用としない。
静的リンクした既存配布物へ修正を適用するには、更新版での再ビルドと配布が必要になる。

4.x以降への移行は別PRで行う。SQLCipherとのAPI/ABI互換性、各OSの静的リンク、
既存の暗号化DBを使う回帰テスト、配布物検証、OSSの対象版・表示・原文をまとめてレビューする。

## 2026年11月26日の暫定対策

macOS と Windows の SQLCipher 用ビルドでは、未使用の CMS、AES-GCM-SIV、QUIC を OpenSSL の設定で無効にする。静的ライブラリを配布する前に、必要な AES-256-CBC のシンボルがあり、CMS 暗号化と AES-GCM-SIV の実装シンボルが含まれないことを確認する。MSYS2 のシステム OpenSSL は別の配布物として追跡する。公式修正版の版とハッシュを取り込んだ後、各対象を再検査する。
