<#
.SYNOPSIS
  Builds the Pi services and installs them on a radio, from Windows.

.DESCRIPTION
  Cross-compiles rar-bridge, rar-led and rar-meshtest for the Pi 4
  (linux/arm64), copies them with the service scripts and default settings
  to the radio, and runs the installer there. Needs Go and the OpenSSH
  client that ships with Windows (ssh, scp, tar).

  You will be asked for the radio's root password twice (copy, then
  install) unless you use an SSH key.

.PARAMETER Radio
  IP address or hostname of the radio, e.g. 10.41.113.1 or manet01.local.

.PARAMETER ResetConfig
  Replace /etc/config/rar with the new defaults, keeping this radio's own
  settings (HaLow interface, serial port, baud, channel, hop limit, debug).
  The old file is saved as /etc/config/rar.bak.

.PARAMETER User
  SSH user (default root).

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Radio 10.41.113.1 -ResetConfig
#>
param(
    [Parameter(Mandatory = $true)][string]$Radio,
    [switch]$ResetConfig,
    [string]$User = "root"
)
$ErrorActionPreference = "Stop"

$pi = Split-Path -Parent $PSScriptRoot
Push-Location $pi
try {
$build = Join-Path $pi "build"
$stage = Join-Path $build "stage"
$tarball = Join-Path $build "rar-stage.tar.gz"
$files = Join-Path (Join-Path $pi "packaging") "openwrt"

if (Test-Path $stage) { Remove-Item -Recurse -Force $stage }
$bin = Join-Path (Join-Path $stage "usr") "bin"
$initd = Join-Path (Join-Path $stage "etc") "init.d"
$conf = Join-Path (Join-Path $stage "etc") "config"
foreach ($d in $bin, $initd, $conf) { New-Item -ItemType Directory -Force $d | Out-Null }

Write-Host "Building for the Pi 4 (linux/arm64)..."
$saved = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED }
try {
    $env:GOOS = "linux"; $env:GOARCH = "arm64"; $env:CGO_ENABLED = "0"
    foreach ($c in "rar-bridge", "rar-led", "rar-meshtest") {
        & go build -trimpath -ldflags "-s -w" -o (Join-Path $bin $c) "./cmd/$c"
        if ($LASTEXITCODE -ne 0) { throw "go build $c failed" }
    }
}
finally {
    # Put the Go environment back so later builds target Windows again.
    foreach ($k in $saved.Keys) {
        if ($null -eq $saved[$k]) { Remove-Item "Env:$k" -ErrorAction SilentlyContinue }
        else { Set-Item "Env:$k" $saved[$k] }
    }
}

$src = Join-Path $files "files"
Copy-Item (Join-Path (Join-Path (Join-Path $src "etc") "init.d") "rar-bridge") $initd
Copy-Item (Join-Path (Join-Path (Join-Path $src "etc") "init.d") "rar-led") $initd
Copy-Item (Join-Path (Join-Path (Join-Path $src "etc") "config") "rar") (Join-Path $conf "rar.default")
Copy-Item (Join-Path $files "rar-install.sh") $stage

if (Test-Path $tarball) { Remove-Item -Force $tarball }
& tar -czf $tarball -C $stage .
if ($LASTEXITCODE -ne 0) { throw "tar failed" }

# OpenSSH 9+ scp uses SFTP by default, which openMANET's dropbear lacks;
# -O selects the classic protocol. Older scp does not know -O.
$prev = $ErrorActionPreference; $ErrorActionPreference = "Continue"
$sshVersion = (& ssh -V 2>&1 | Out-String)
$ErrorActionPreference = $prev
$scpOpts = @()
if ($sshVersion -match "OpenSSH\w*?_(\d+)\.") {
    if ([int]$Matches[1] -ge 9) { $scpOpts = @("-O") }
}

$target = "$User@$Radio"
Write-Host "Copying to $target ..."
& scp @scpOpts $tarball "${target}:/tmp/rar-stage.tar.gz"
if ($LASTEXITCODE -ne 0) { throw "scp to $target failed" }

$installArgs = ""
if ($ResetConfig) { $installArgs = "--reset-config" }
Write-Host "Installing on $target ..."
& ssh $target "rm -rf /tmp/rar-stage && mkdir -p /tmp/rar-stage && tar -xzf /tmp/rar-stage.tar.gz -C /tmp/rar-stage && sh /tmp/rar-stage/rar-install.sh $installArgs; rc=`$?; rm -rf /tmp/rar-stage /tmp/rar-stage.tar.gz; exit `$rc"
if ($LASTEXITCODE -ne 0) { throw "install on $target failed" }
Write-Host "Done."
}
finally {
    Pop-Location
}
