# Trivyの固定と脆弱性DB

Trivy は上流のリリースコミットとソースの SHA256 を `build/locks/trivy-source.json` に固定する。`preserve-trivy.yml` は上流と同じ Linux ビルド設定とリポジトリの固定 Go を用い、amd64・arm64 のネイティブ環境で版とビルド情報を確認する。別ジョブがバイナリ、上流ライセンス、ビルド情報を GHCR に保存する。通常の CI とリリースは `build/locks/trivy.json` の OCI 取得物と SHA256 を照合して同じバイナリを使用する。固定入力検査は両 CPU のバイナリそのものを調べる。

脆弱性DBは検査ごとに新しいcache directoryへ取得する。scannerの固定とDBの更新を分け、新しい脆弱性を検出し続ける。検査後にscanner版、使用DBのmetadata、DBファイルのSHA256、binaryのSHA256とlockを`trivy-inputs-*` artifactに保存する。DBの取得に失敗した検査を正常扱いにしない。

通常CIでは従来どおりJSON reportを正規化して、修正可能なHIGH/CRITICAL findingsと検査エラーを失敗させる。releaseでは同じ条件を通ったarchitecture digestだけをindexへ進める。scanner版の更新はlockだけを変更する別PRとし、検査条件の緩和を同時に行わない。
