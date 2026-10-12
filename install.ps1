# Instalator abcollab dla Windows (PowerShell):
#
#   irm https://raw.githubusercontent.com/c-airr/ableton-collab/main/install.ps1 | iex
#
# Pobiera najnowsze wydanie z GitHub Releases, sprawdza sume sha256, instaluje
# do %LOCALAPPDATA%\Programs\abcollab i dopisuje go do PATH uzytkownika.
# Bez uprawnien administratora.
#
# Zmienne (glownie do testow):
#   ABCOLLAB_INSTALL_BASE  skad pobierac (domyslnie najnowsze wydanie)
#   ABCOLLAB_INSTALL_DIR   gdzie zainstalowac
#   ABCOLLAB_NO_PATH=1     nie ruszaj PATH

& {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue'

    $repo = 'c-airr/ableton-collab'
    $base = $env:ABCOLLAB_INSTALL_BASE
    if (-not $base) { $base = "https://github.com/$repo/releases/latest/download" }
    $dest = $env:ABCOLLAB_INSTALL_DIR
    if (-not $dest) { $dest = Join-Path $env:LOCALAPPDATA 'Programs\abcollab' }

    $arch = 'amd64'
    if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { $arch = 'arm64' }
    $asset = "abcollab-windows-$arch.zip"

    # Windows PowerShell 5.1 domyslnie nie wlacza TLS 1.2, a GitHub go wymaga
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('abcollab-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "downloading $asset..."
        Invoke-WebRequest -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset) -UseBasicParsing
        Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing

        $want = $null
        foreach ($line in Get-Content (Join-Path $tmp 'checksums.txt')) {
            $parts = $line -split '\s+'
            if ($parts.Count -ge 2 -and $parts[1].TrimStart('*') -eq $asset) { $want = $parts[0].ToLower() }
        }
        $got = (Get-FileHash -Path (Join-Path $tmp $asset) -Algorithm SHA256).Hash.ToLower()
        if (-not $want -or $want -ne $got) { throw "wrong sha256 of the downloaded $asset - aborting" }

        Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath (Join-Path $tmp 'x') -Force
        New-Item -ItemType Directory -Force -Path $dest | Out-Null
        $exe = Join-Path $dest 'abcollab.exe'
        $old = Join-Path $dest 'abcollab.old.exe'
        Remove-Item -Force -Path $old -ErrorAction SilentlyContinue
        # dzialajacego .exe nie da sie nadpisac, ale da sie go przemianowac -
        # otwarty abcollab dziala dalej, nastepne uruchomienie bierze nowa wersje
        if (Test-Path $exe) {
            try { Move-Item -Force -Path $exe -Destination $old } catch {}
        }
        try {
            Copy-Item -Path (Join-Path $tmp 'x\abcollab.exe') -Destination $exe -Force
        } catch {
            throw "can't replace $exe - close abcollab and try again"
        }
    } finally {
        Remove-Item -Recurse -Force -Path $tmp -ErrorAction SilentlyContinue
    }
    $ver = & (Join-Path $dest 'abcollab.exe') version
    Write-Host "installed: $dest\abcollab.exe ($ver)"

    if ($env:ABCOLLAB_NO_PATH -ne '1') {
        $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
        if (-not $userPath) { $userPath = '' }
        if (($userPath -split ';') -notcontains $dest) {
            $newPath = (($userPath.TrimEnd(';'), $dest) | Where-Object { $_ }) -join ';'
            [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
            Write-Host "added $dest to PATH"
        }
        if (($env:Path -split ';') -notcontains $dest) { $env:Path += ";$dest" }
    }
    Write-Host ''
    Write-Host 'Done. Type: abcollab'
}
