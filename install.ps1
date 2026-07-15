#!/usr/bin/env pwsh
<#
.SYNOPSIS
    Installs the forecast binary from the latest (or pinned) GitHub release.

.DESCRIPTION
    irm https://raw.githubusercontent.com/commondatageek/delivery-forecast/main/install.ps1 | iex

    Native-Windows equivalent of install.sh. Uses only PowerShell built-ins
    (Invoke-RestMethod, Invoke-WebRequest, Expand-Archive, Get-FileHash) -
    no external tools required.

.PARAMETER InstallDir
    Where to install forecast.exe. Defaults to $env:FORECAST_INSTALL_DIR or
    "$env:USERPROFILE\.forecast\bin".

.PARAMETER Version
    Release tag to install, e.g. "v1.2.3". Defaults to $env:FORECAST_VERSION
    or the latest non-prerelease release.

.PARAMETER NoModifyPath
    Skip adding the install directory to the user's PATH. Also honored via
    $env:FORECAST_NO_MODIFY_PATH (any non-empty value).
#>
[CmdletBinding()]
param(
    [string]$InstallDir = $env:FORECAST_INSTALL_DIR,
    [string]$Version = $env:FORECAST_VERSION,
    [switch]$NoModifyPath
)

$ErrorActionPreference = 'Stop'

$Repo = 'commondatageek/delivery-forecast'
$Binary = 'forecast.exe'

function Say([string]$Message) {
    Write-Host "forecast-install: $Message"
}

function Warn([string]$Message) {
    Write-Warning "forecast-install: $Message"
}

function Fail([string]$Message) {
    Write-Error "forecast-install: error: $Message" -ErrorAction Continue
    exit 1
}

function Get-Arch {
    switch ($env:PROCESSOR_ARCHITECTURE) {
        'AMD64' { return 'amd64' }
        'ARM64' { return 'arm64' }
        default { Fail "unsupported CPU architecture '$($env:PROCESSOR_ARCHITECTURE)'" }
    }
}

function Resolve-Version([string]$Pinned) {
    if ($Pinned) {
        return $Pinned
    }

    Say 'resolving latest release...'
    $apiUrl = "https://api.github.com/repos/$Repo/releases/latest"
    try {
        $release = Invoke-RestMethod -Uri $apiUrl -Headers @{ 'Accept' = 'application/vnd.github+json' }
    } catch {
        Fail "failed to fetch latest release info from ${apiUrl}: $_"
    }

    if (-not $release.tag_name) {
        Fail "could not determine the latest release tag from $apiUrl (does the repo have a published, non-prerelease release?)"
    }

    return $release.tag_name
}

function Test-Checksum {
    param(
        [string]$FilePath,
        [string]$AssetName,
        [string]$ChecksumsPath
    )

    $line = Select-String -Path $ChecksumsPath -Pattern ([regex]::Escape($AssetName)) | Select-Object -First 1
    if (-not $line) {
        Fail "no checksum entry for $AssetName in checksums.txt"
    }
    $want = ($line.Line -split '\s+')[0].ToLowerInvariant()

    $got = (Get-FileHash -Path $FilePath -Algorithm SHA256).Hash.ToLowerInvariant()

    if ($got -ne $want) {
        Fail "checksum mismatch for ${AssetName}: got $got, want $want"
    }

    Say 'checksum OK'
}

function Add-InstallDirToPath([string]$Dir, [bool]$Skip) {
    if ($Skip) {
        Say "note: $Dir is not guaranteed to be on your PATH. Add it manually, or omit -NoModifyPath / FORECAST_NO_MODIFY_PATH to have this script do it."
        return
    }

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $entries = @()
    if ($userPath) {
        $entries = $userPath -split ';' | Where-Object { $_ -ne '' }
    }

    if ($entries -contains $Dir) {
        Say "PATH already configured (User environment variable)"
        return
    }

    $newPath = if ($userPath) { "$Dir;$userPath" } else { $Dir }
    [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
    Say "added $Dir to your User PATH - open a new terminal for this to take effect"
}

function Main {
    param(
        [string]$InstallDir,
        [string]$Version,
        [bool]$NoModifyPath
    )

    $arch = Get-Arch
    $resolvedVersion = Resolve-Version -Pinned $Version

    if (-not $InstallDir) {
        $InstallDir = Join-Path $env:USERPROFILE '.forecast\bin'
    }

    $asset = "forecast_${resolvedVersion}_windows_${arch}.zip"
    $baseUrl = "https://github.com/$Repo/releases/download/$resolvedVersion"
    $assetUrl = "$baseUrl/$asset"
    $checksumsUrl = "$baseUrl/checksums.txt"

    $workDir = Join-Path ([System.IO.Path]::GetTempPath()) ([System.IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $workDir | Out-Null

    try {
        $assetPath = Join-Path $workDir $asset
        $checksumsPath = Join-Path $workDir 'checksums.txt'

        Say "downloading $asset ($resolvedVersion)..."
        try {
            Invoke-WebRequest -Uri $assetUrl -OutFile $assetPath
            Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath
        } catch {
            Fail "failed to download release assets: $_"
        }

        Test-Checksum -FilePath $assetPath -AssetName $asset -ChecksumsPath $checksumsPath

        Say 'extracting...'
        $extractDir = Join-Path $workDir 'extracted'
        Expand-Archive -Path $assetPath -DestinationPath $extractDir

        $binaryPath = Get-ChildItem -Path $extractDir -Filter $Binary -Recurse | Select-Object -First 1
        if (-not $binaryPath) {
            Fail "'$Binary' not found inside $asset"
        }

        New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
        $destPath = Join-Path $InstallDir $Binary
        Move-Item -Path $binaryPath.FullName -Destination $destPath -Force

        Add-InstallDirToPath -Dir $InstallDir -Skip $NoModifyPath

        Say "installed $Binary $resolvedVersion to $destPath"
        & $destPath version
    } finally {
        Remove-Item -Path $workDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

$skipPathEdit = $NoModifyPath.IsPresent -or [bool]$env:FORECAST_NO_MODIFY_PATH
Main -InstallDir $InstallDir -Version $Version -NoModifyPath $skipPathEdit
