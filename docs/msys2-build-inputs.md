# MSYS2の環境全体を固定して復元する

通常のWindows配布buildでは`setup-msys2`の最新版取得・`update: true`・追加package導入を行わない。`build/locks/msys2.json`に記録したOCI artifact digestとroot archive SHA256を照合して、空の専用directoryへ環境全体を復元する。

archiveは全体upgradeしたUCRT64環境であり、C compiler・C runtime・binutils・make・Tcl・Perl・curlと全推移的依存を含む。package cache、SHA256、pacmanのlocal/sync metadataも同じarchiveに含める。復元後にcache hashと`pacman -Q`全件の一致を確認する。部分的なpackage降格や別snapshotへの自動fallbackは行わない。

生成元は[保存workflowのrun 44076743431](https://github.com/ShiningWank0/Omni_Money/actions/runs/44076743431)、source commitはlockに記録する。通常buildにregistry書込権限は与えず、既存のread-only `packages: read`権限で取得する。OCI取得に用いるORASも公式binaryのSHA256で固定する。

OpenSSL 3.5.9とSQLCipher 4.18.0のsource/checksumはこの変更では更新しない。既存のnative archive/version probe、Windows SQLCipher lifecycle tests、配布DLL whitelistで復元環境を検証する。Windows OS全体はGitHub-hosted `windows-2025`に依存し、OS image全体の固定は行わない。
