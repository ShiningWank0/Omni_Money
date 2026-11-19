# CI/CDの取得物を固定する

通常のアプリ検証と配布は、同じ取得物を使用する。更新はlockファイルと検証結果を変更するPRで行う。ActionのSHA固定と、そのActionが取得するツールの固定は別に扱う。

## Dockerツール

`build/locks/docker.json` がLinux amd64/arm64用の唯一の定義元である。`setup-fixed-docker` は公式配布URLとSHA256を照合してから実行し、Docker CLI/Engine、同梱containerd/runc、Buildx、Composeを導入する。Engineを起動するジョブではrunner標準daemonを停止し、専用socket・data root・exec rootを使用する。既存のhost bind mountとloopback公開portを使うE2Eの構成を維持する。

| 入力 | 固定方法 | 検証 |
| --- | --- | --- |
| Docker CLI / Engine / containerd / runc | 公式配布archiveのURLとSHA256 | CLIと起動したdaemonの版を照合 |
| Buildx / Compose | OS・arch別の公式binaryとSHA256 | 導入後の版を照合 |
| BuildKit | OCI indexのdigest | 全buildで`docker-container` driverを使用 |
| SBOM generator | OCI indexのdigest | releaseの`sbom`に明示 |

アプリ用Dockerfile内のOSパッケージとLinux/Windows/macOSのnativeビルド環境は別の固定対象であり、このDockerツールのlockだけで固定済みとは扱わない。GitHub-hosted runnerのkernelやホストOS全体も固定対象外である。

## 更新の確認

公式のrelease metadataとregistry manifestからURL・SHA256・digestを確認し、両archを同じPRで更新する。通常のsetupではlatest解決や更新処理を行わない。取得先の削除やhash不一致は失敗として扱い、別版へのfallbackを行わない。

Dockerツールの変更PRでは、CIのCompose検証・Docker実build・hardened runtime・server/browser E2Eと、releaseのamd64/arm64 buildを確認する。これらのbuildはCI/CD内で実行する。
