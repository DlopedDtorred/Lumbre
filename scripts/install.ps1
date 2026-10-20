$ErrorActionPreference = "Stop"

$Repository = if ($env:LUMBRE_REPO) { $env:LUMBRE_REPO } else { "DlopedDtorred/Lumbre" }
$Version = if ($env:LUMBRE_VERSION) { $env:LUMBRE_VERSION } else { "latest" }
$InstallDir = if ($env:LUMBRE_INSTALL_DIR) {
    $env:LUMBRE_INSTALL_DIR
} else {
    Join-Path $env:LOCALAPPDATA "Programs\Lumbre"
}

if ($Repository -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') {
    throw "LUMBRE_REPO must be a GitHub owner/repository, for example: owner/Lumbre"
}

$Asset = "lumbre-windows-x86_64.zip"
if ($Version -eq "latest") {
    $Url = "https://github.com/$Repository/releases/latest/download/$Asset"
} else {
    $Url = "https://github.com/$Repository/releases/download/$Version/$Asset"
}

$Temp = Join-Path ([System.IO.Path]::GetTempPath()) ("lumbre-" + [guid]::NewGuid())
$Archive = Join-Path $Temp $Asset
$Checksums = Join-Path $Temp "SHA256SUMS"
$Unpacked = Join-Path $Temp "package"
try {
    New-Item -ItemType Directory -Path $Temp | Out-Null
    Invoke-WebRequest -Uri $Url -OutFile $Archive
    Invoke-WebRequest -Uri "$($Url.Substring(0, $Url.LastIndexOf('/')))/SHA256SUMS" -OutFile $Checksums
    $Expected = ((Get-Content $Checksums | Where-Object { $_ -match "^\S+\s+\*?$([regex]::Escape($Asset))$" } | Select-Object -First 1) -split "\s+")[0]
    if (!$Expected) {
        throw "Release checksum is missing for $Asset."
    }
    $Actual = (Get-FileHash -LiteralPath $Archive -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($Actual -ne $Expected.ToLowerInvariant()) {
        throw "The downloaded package failed its SHA-256 check."
    }
    Expand-Archive -LiteralPath $Archive -DestinationPath $Unpacked
    $Lumbre = Join-Path $Unpacked "lumbre.exe"
    $Engine = Join-Path $Unpacked "llama-server.exe"
    $Web = Join-Path $Unpacked "web\index.html"
    if (!(Test-Path $Lumbre) -or !(Test-Path $Engine) -or !(Test-Path $Web)) {
        throw "Release package is missing Lumbre, llama-server, or the web app."
    }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -Force $Lumbre (Join-Path $InstallDir "lumbre.exe")
    Copy-Item -Force $Engine (Join-Path $InstallDir "llama-server.exe")
    Copy-Item -Recurse -Force (Join-Path $Unpacked "web") (Join-Path $InstallDir "web")

    $UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
    $PathEntries = @($UserPath -split ";" | Where-Object { $_ })
    if ($PathEntries -notcontains $InstallDir) {
        [Environment]::SetEnvironmentVariable("Path", (($PathEntries + $InstallDir) -join ";"), "User")
    }
    Write-Host "Lumbre installed successfully in $InstallDir"
    Write-Host "Open a new terminal, then run: lumbre serve"
} finally {
    if (Test-Path $Temp) {
        Remove-Item -LiteralPath $Temp -Recurse -Force
    }
}
