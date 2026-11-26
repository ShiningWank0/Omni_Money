$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($env:GITHUB_ACTIONS -ne 'true' -or -not $IsWindows -or $env:RUNNER_ARCH -ne 'X64') { throw 'Windows x64 CI required' }
$lock = Get-Content -Raw (Join-Path $PSScriptRoot '../build/locks/webview2.json') | ConvertFrom-Json
if ($lock.schema -ne 1 -or $lock.artifact -notmatch '^ghcr.io/shiningwank0/omni_money-ci-webview2@sha256:[a-f0-9]{64}$' -or $lock.cabSHA256 -notmatch '^[a-f0-9]{64}$') { throw 'Invalid WebView2 lock' }
$work = Join-Path $env:RUNNER_TEMP 'omni-webview2'
New-Item -ItemType Directory $work | Out-Null
$zip = Join-Path $work 'oras.zip'
Invoke-WebRequest 'https://github.com/oras-project/oras/releases/download/v1.4.0/oras_1.4.0_windows_amd64.zip' -OutFile $zip
if ((Get-FileHash $zip -Algorithm SHA256).Hash.ToLowerInvariant() -ne 'c70d25e3463a161a5aeeddd9fe5ef882c811dde8115ab0883c0b87d3f0c50832') { throw 'ORAS checksum mismatch' }
Expand-Archive $zip (Join-Path $work 'oras')
$oras = Join-Path $work 'oras/oras.exe'
$env:DOCKER_CONFIG = Join-Path $work 'registry-config'
try {
    if (-not $env:GHCR_TOKEN) { throw 'Read-only GHCR token required' }
    $env:GHCR_TOKEN | & $oras login ghcr.io -u $env:GITHUB_ACTOR --password-stdin
    if ($LASTEXITCODE -ne 0) { throw 'GHCR login failed' }
    & $oras pull --output (Join-Path $work 'source') $lock.artifact
    if ($LASTEXITCODE -ne 0) { throw 'WebView2 artifact download failed' }
} finally {
    Remove-Item -Recurse -Force $env:DOCKER_CONFIG -ErrorAction SilentlyContinue
    Remove-Item Env:DOCKER_CONFIG
}
$cab = Join-Path $work 'source/runtime.cab'
if ((Get-FileHash $cab -Algorithm SHA256).Hash.ToLowerInvariant() -ne $lock.cabSHA256) { throw 'WebView2 CAB checksum mismatch' }
$dest = Join-Path $work 'runtime'
New-Item -ItemType Directory $dest | Out-Null
& "$env:SystemRoot/System32/expand.exe" $cab '-F:*' $dest
if ($LASTEXITCODE -ne 0) { throw 'WebView2 expansion failed' }
$executables = @(Get-ChildItem -LiteralPath $dest -Recurse -Filter msedgewebview2.exe)
if ($executables.Count -ne 1) { throw 'Expected exactly one Fixed Version Runtime executable' }
$exe = $executables[0]
if ($exe.VersionInfo.ProductVersion -ne $lock.version) { throw 'Extracted WebView2 version mismatch' }
$signature = Get-AuthenticodeSignature -LiteralPath $exe.FullName
if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'O=Microsoft Corporation') { throw 'Invalid Microsoft runtime signature' }
# Read/execute access required by the WebView2 renderer sandbox. Never grant write access.
& icacls.exe $exe.DirectoryName '/grant' '*S-1-15-2-1:(OI)(CI)(RX)' '*S-1-15-2-2:(OI)(CI)(RX)' '/T' '/Q'
if ($LASTEXITCODE -ne 0) { throw 'WebView2 sandbox ACL setup failed' }
"OMNI_CI_WEBVIEW2_RUNTIME=$($exe.DirectoryName)" | Out-File -Encoding utf8 -Append $env:GITHUB_ENV
Write-Output "Fixed Version WebView2 $($lock.version) at $($exe.DirectoryName)"
