# SPDX-License-Identifier: MIT
#
# Installs defy-installer on Windows from its latest release, in PowerShell:
#
#   irm https://github.com/borovaka/defy-installer/releases/latest/download/install.ps1 | iex
#
# It downloads the archive for this computer, checks it against the
# release's SHA256SUMS and unpacks it into $HOME\defy-installer, keeping the
# defy-installer.yaml there. DEFY_INSTALLER_DIR and DEFY_INSTALLER_BASE
# change the folder and the download URL. iex runs this in your own
# session, so it keeps to a script block and throws where exit would close
# the window.

& {
    $ErrorActionPreference = 'Stop'
    # Windows PowerShell 5.1 downloads many times slower with the progress
    # bar, and may offer GitHub only TLS 1.0, which GitHub refuses.
    $ProgressPreference = 'SilentlyContinue'
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $base = if ($env:DEFY_INSTALLER_BASE) { $env:DEFY_INSTALLER_BASE } else { 'https://github.com/borovaka/defy-installer/releases/latest/download' }
    $dest = if ($env:DEFY_INSTALLER_DIR) { $env:DEFY_INSTALLER_DIR } else { Join-Path $HOME 'defy-installer' }
    # The machine's processor: an x64 PowerShell on Arm reports x64 as its own.
    $archive = if ([Environment]::GetEnvironmentVariable('PROCESSOR_ARCHITECTURE', 'Machine') -eq 'ARM64') { 'defy-installer-windows-arm64.zip' } else { 'defy-installer-windows.zip' }

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory $tmp | Out-Null
    try {
        Invoke-WebRequest -UseBasicParsing "$base/$archive" -OutFile "$tmp\$archive"
        Invoke-WebRequest -UseBasicParsing "$base/SHA256SUMS" -OutFile "$tmp\SHA256SUMS"
        $want = foreach ($line in Get-Content "$tmp\SHA256SUMS") {
            $hash, $name = -split $line
            if ($name -eq $archive) { $hash }
        }
        if (-not $want -or (Get-FileHash "$tmp\$archive" -Algorithm SHA256).Hash -ne $want) {
            throw "$archive does not match the release's SHA256SUMS"
        }
        # Invoke-WebRequest leaves no mark of the web, so Windows starts the
        # program without "Windows protected your PC".
        Expand-Archive "$tmp\$archive" $tmp
        New-Item -ItemType Directory -Force $dest | Out-Null
        foreach ($file in Get-ChildItem "$tmp\defy-installer") {
            if ($file.Name -ne 'defy-installer.yaml' -or -not (Test-Path "$dest\defy-installer.yaml")) {
                Copy-Item $file.FullName $dest -Force
            }
        }
    } finally {
        Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }

    Write-Host "Installed $(& "$dest\defy-installer.exe" --version) in $dest. Next, run:"
    Write-Host "  cd `"$dest`"; .\defy-installer.exe install    # or update, on a keyboard that runs ZMK"
}
