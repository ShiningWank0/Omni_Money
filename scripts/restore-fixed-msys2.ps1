$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($env:GITHUB_ACTIONS -ne 'true' -or -not $IsWindows -or $env:RUNNER_ARCH -ne 'X64') {
    throw 'This restore requires a GitHub Actions Windows x64 runner'
}
$lock = Get-Content -Raw (Join-Path $PSScriptRoot '../build/locks/msys2.json') | ConvertFrom-Json
if ($lock.schema -ne 1 -or $lock.artifact -notmatch '^ghcr.io/shiningwank0/omni_money-build-msys2@sha256:[a-f0-9]{64}$' -or $lock.archiveSHA256 -notmatch '^[a-f0-9]{64}$') {
    throw 'Invalid MSYS2 lock'
}
$work = Join-Path $env:RUNNER_TEMP 'omni-msys2-restore'
$root = 'C:\omni-msys2'
if (Test-Path $root) { throw 'MSYS2 destination already exists; refusing to mix environments' }
New-Item -ItemType Directory $work | Out-Null
$zip = Join-Path $work 'oras.zip'
Invoke-WebRequest -Uri $lock.oras.url -OutFile $zip
if ((Get-FileHash $zip -Algorithm SHA256).Hash.ToLowerInvariant() -ne $lock.oras.sha256) { throw 'ORAS checksum mismatch' }
Expand-Archive -LiteralPath $zip -DestinationPath (Join-Path $work 'oras')
$oras = Join-Path $work 'oras/oras.exe'
$env:DOCKER_CONFIG = Join-Path $work 'registry-config'
try {
    if (-not $env:GHCR_TOKEN) { throw 'A read-only GHCR token is required' }
    $env:GHCR_TOKEN | & $oras login ghcr.io -u $env:GITHUB_ACTOR --password-stdin
    if ($LASTEXITCODE -ne 0) { throw 'GHCR login failed' }
    & $oras pull --output (Join-Path $work 'snapshot') $lock.artifact
    if ($LASTEXITCODE -ne 0) { throw 'Fixed MSYS2 artifact download failed' }
} finally {
    Remove-Item -Recurse -Force $env:DOCKER_CONFIG -ErrorAction SilentlyContinue
    Remove-Item Env:DOCKER_CONFIG
}
$archive = Join-Path $work 'snapshot/msys64.tar.gz'
if ((Get-FileHash $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $lock.archiveSHA256) { throw 'MSYS2 archive checksum mismatch' }
New-Item -ItemType Directory $root | Out-Null
& "$env:SystemRoot/System32/tar.exe" -xzf $archive -C $root
if ($LASTEXITCODE -ne 0) { throw 'Fixed MSYS2 extraction failed' }
# Empty transient directories are intentionally absent from the archive.
# Recreate them before MSYS startup and native source builds.
@('tmp', 'var/tmp') | ForEach-Object {
    New-Item -ItemType Directory -Force (Join-Path $root $_) | Out-Null
}
$env:MSYSTEM = 'UCRT64'
$env:MSYS2_PATH_TYPE = 'inherit'
$env:CHERE_INVOKING = '1'
# A backslash in CC is consumed by the shell in OpenSSL's generated Makefile.
# Mixed Windows paths work both in native Go/PowerShell and in MSYS make.
$compilerRoot = $root.Replace('\', '/')
$env:CC = "$compilerRoot/ucrt64/bin/gcc.exe"
$env:CXX = "$compilerRoot/ucrt64/bin/g++.exe"
$bash = Join-Path $root 'usr/bin/bash.exe'
& $bash --login -e -o pipefail -c 'sha256sum --check --strict /opt/omni-environment/packages.sha256; diff -u /opt/omni-environment/packages.txt <(pacman -Q); test "$(gcc -dumpmachine)" = x86_64-w64-mingw32; tclsh <<< "puts [info patchlevel]"'
if ($LASTEXITCODE -ne 0) { throw 'Restored MSYS2 does not match the captured package set' }
$nativeTarget = & $env:CC -dumpmachine
if ($LASTEXITCODE -ne 0 -or $nativeTarget -ne 'x86_64-w64-mingw32') { throw 'Fixed compiler cannot be invoked from Windows' }
$compilerProbe = @'
make -s --no-print-directory -f - <<'MAKE'
.PHONY: compiler-probe
compiler-probe:
	@$(CC) -dumpmachine
	@$(CXX) -dumpmachine
MAKE
'@
$makeTargets = @(& $bash --login -e -o pipefail -c $compilerProbe)
if ($LASTEXITCODE -ne 0 -or $makeTargets.Count -ne 2 -or @($makeTargets | Where-Object { $_ -ne 'x86_64-w64-mingw32' }).Count -ne 0) {
    throw 'Fixed C/C++ compiler paths are not safe for MSYS make'
}
$bin = Join-Path $work 'bin'
New-Item -ItemType Directory $bin | Out-Null
Set-Content -Encoding ascii -LiteralPath (Join-Path $bin 'msys2.cmd') -Value "@echo off`r`n`"$bash`" --login -e -o pipefail %*`r`n"
@((Join-Path $root 'usr/bin'), (Join-Path $root 'ucrt64/bin'), $bin) | Out-File -Encoding utf8 -Append $env:GITHUB_PATH
@('MSYSTEM=UCRT64', 'MSYS2_PATH_TYPE=inherit', 'CHERE_INVOKING=1', "CC=$env:CC", "CXX=$env:CXX") | Out-File -Encoding utf8 -Append $env:GITHUB_ENV
Write-Output "Restored MSYS2 from $($lock.artifact)"
