# CI scannerの依存管理

gosecは`/scripts/securitytools/gosec/go.mod`と`go.sum`を使い、アプリのGo moduleから分離して構築する。通常の実行は`go run -mod=readonly -modfile=...`であり、上流の指定版とchecksumを使用する。scannerを新しいGoで構築するためだけにアプリの依存を変更しない。

gosec 2.30.0は上流のx/tools 0.52.0に揃える。従来の2.29.0にx/tools 0.50.0を追加していたexport-data互換対応は、この上流組合せに置き換える。`go.sum`は上流の選択版のchecksumとGo checksum databaseにあるgosec自身のchecksumから構成する。

標準名のmodule manifestを使用するため、Dependabotはこのdirectoryも毎週確認する。更新PRでは、scannerの実解析と既存JSON reportのエラー・findings判定をCIで確認する。判定条件の緩和や自動修正・AIによる修正処理は追加しない。

govulncheckも`/scripts/securitytools/govulncheck/go.mod`と`go.sum`で1.9.0に固定する。通常CIのJSON reportとrelease quality gateは、同じreadonlyのtool moduleを使用する。アプリのGo graphは解析対象のまま保持し、scannerの依存を製品へ追加しない。このdirectoryもDependabotの毎週確認対象とする。脆弱性DBは現在の公式DBを使用し、新しい検出結果や検査エラーを失敗として扱う。
