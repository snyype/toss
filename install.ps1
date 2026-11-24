# Installs the latest Toss release on Windows (PowerShell 5.1+ or PowerShell 7).
#   PowerShell:  irm https://raw.githubusercontent.com/snyype/toss/main/install.ps1 | iex
#   cmd:         powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/snyype/toss/main/install.ps1 | iex"
#
# Same approach as install.sh: no GitHub API calls (60 unauthenticated requests
# per hour per IP), only github.com's /releases/latest redirects. The binary is
# checked against checksums.txt from the same release before it is installed.
#
# Everything runs inside a script block and stops with `return`, never `exit`,
# because `irm ... | iex` runs in the user's own session and `exit` would close it.

& {
    $ErrorActionPreference = 'Stop'
    # Invoke-WebRequest's progress bar makes downloads many times slower on 5.1.
    $ProgressPreference = 'SilentlyContinue'
    # Windows PowerShell 5.1 may default to TLS 1.0, which GitHub refuses.
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
    } catch {}

    $Repo = 'snyype/toss'
    $Base = "https://github.com/$Repo/releases"
    $DestDir = 'C:\toss'
    $Destination = Join-Path $DestDir 'toss.exe'
    $Tmp = "$Destination.download"

    # A 32-bit PowerShell on 64-bit Windows reports x86 in PROCESSOR_ARCHITECTURE.
    $arch = $env:PROCESSOR_ARCHITEW6432
    if (-not $arch) { $arch = $env:PROCESSOR_ARCHITECTURE }
    switch ($arch) {
        'AMD64' { $Cpu = 'amd64' }
        'ARM64' { $Cpu = 'arm64' }
        default {
            Write-Host "Unsupported CPU architecture: $arch (Toss has 64-bit Windows builds only)." -ForegroundColor Red
            return
        }
    }

    Write-Host 'Finding the latest Toss release...'
    # /releases/latest redirects to /releases/tag/<tag>; read the tag from the
    # Location header instead of calling the API.
    $LatestTag = ''
    try {
        $req = [Net.HttpWebRequest]::Create("$Base/latest")
        $req.Method = 'HEAD'
        $req.AllowAutoRedirect = $false
        $req.UserAgent = 'toss-installer'
        $resp = $req.GetResponse()
        $location = $resp.Headers['Location']
        $resp.Close()
        if ($location -match '/tag/([^/?#]+)') { $LatestTag = $Matches[1] }
    } catch {}
    $Version = $LatestTag -replace '^[a-z]*-', '' -replace '^v', ''

    # Candidates, best first (mirrors install.sh):
    #   1. the combined release (tag v<x.y.z>), e.g. toss-windows-amd64.exe
    #   2. ARM64 falls back to the amd64 build (runs under emulation)
    #   3. the older per-platform release (tag win-<x.y.z>)
    if ($LatestTag) { $Combined = "download/$LatestTag" } else { $Combined = 'latest/download' }
    $Candidates = @("$Combined/toss-windows-$Cpu.exe")
    if ($Cpu -eq 'arm64') { $Candidates += "$Combined/toss-windows-amd64.exe" }
    if ($Version) {
        if ($Cpu -eq 'arm64') { $Candidates += "download/win-$Version/toss-arm64.exe" }
        $Candidates += "download/win-$Version/toss.exe"
    }

    New-Item -ItemType Directory -Force -Path $DestDir | Out-Null

    $Downloaded = $null
    foreach ($part in $Candidates) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "$Base/$part" -OutFile $Tmp
            $Downloaded = $part
            break
        } catch {
            Remove-Item -Force -ErrorAction SilentlyContinue $Tmp
        }
    }
    if (-not $Downloaded) {
        Write-Host "Could not download a Toss build for windows/$Cpu. Get it manually from $Base" -ForegroundColor Red
        return
    }
    $label = if ($Version) { "v$Version " } else { '' }
    Write-Host "Downloaded Toss ${label}for windows/$Cpu ($(Split-Path $Downloaded -Leaf))."

    # Verify against checksums.txt from the same release. Old per-platform
    # releases have none and are let through with a warning.
    $asset = Split-Path $Downloaded -Leaf
    $releasePath = $Downloaded.Substring(0, $Downloaded.LastIndexOf('/'))
    $sums = $null
    try {
        $sums = (Invoke-WebRequest -UseBasicParsing -Uri "$Base/$releasePath/checksums.txt").Content
        if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
    } catch {}
    if ($null -eq $sums) {
        Write-Host 'Warning: this release has no checksums.txt, skipping checksum verification.' -ForegroundColor Yellow
    } else {
        $expected = $null
        foreach ($line in ($sums -split "`r?`n")) {
            $fields = $line.Trim() -split '\s+'
            if ($fields.Count -ge 2 -and $fields[1].TrimStart('*') -eq $asset) { $expected = $fields[0].ToLower(); break }
        }
        if (-not $expected) {
            Remove-Item -Force -ErrorAction SilentlyContinue $Tmp
            Write-Host "Error: checksums.txt has no entry for $asset. Toss was not installed." -ForegroundColor Red
            return
        }
        $actual = (Get-FileHash -Algorithm SHA256 -Path $Tmp).Hash.ToLower()
        if ($actual -ne $expected) {
            Remove-Item -Force -ErrorAction SilentlyContinue $Tmp
            Write-Host "Error: checksum mismatch for $asset - the download is corrupted or was tampered with. Toss was not installed." -ForegroundColor Red
            return
        }
        Write-Host 'Checksum verified.'
    }

    try {
        Move-Item -Force -Path $Tmp -Destination $Destination
    } catch {
        Remove-Item -Force -ErrorAction SilentlyContinue $Tmp
        Write-Host "Could not replace $Destination - close any running toss and try again." -ForegroundColor Red
        return
    }
    # Drop the "downloaded from the internet" mark so SmartScreen doesn't prompt.
    Unblock-File -Path $Destination -ErrorAction SilentlyContinue

    # Add C:\toss to the *user* PATH (no admin needed, no setx truncation), and
    # to this session's PATH so `toss` works right away when run via iex.
    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $userPath) { $userPath = '' }
    if (-not (($userPath -split ';') -contains $DestDir)) {
        [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ";$DestDir").TrimStart(';'), 'User')
        Write-Host "Added $DestDir to your PATH. Open a new terminal to use toss from cmd or other windows."
    }
    if (-not (($env:Path -split ';') -contains $DestDir)) {
        $env:Path = $env:Path.TrimEnd(';') + ";$DestDir"
    }

    Write-Host "Toss ${label}installed to $Destination. Run 'toss' to get started." -ForegroundColor Green
}
