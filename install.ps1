$ErrorActionPreference = "Stop"

$Repo = "kint-pro/kint-data-cli"
$Binary = "kint-data.exe"
$InstallDir = "$env:LOCALAPPDATA\kint-data"

$Arch = if ([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture -eq "Arm64") { "arm64" } else { "amd64" }

$Release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
$Version = $Release.tag_name.TrimStart("v")

$Url = "https://github.com/$Repo/releases/download/v$Version/kint-data-cli_${Version}_windows_${Arch}.zip"

$Tmp = Join-Path $env:TEMP "kint-data-install"
New-Item -ItemType Directory -Force -Path $Tmp | Out-Null
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null

Write-Host "Downloading kint-data v$Version (windows/$Arch)..."
Invoke-WebRequest $Url -OutFile "$Tmp\kint-data.zip"

$ChecksumUrl = "https://github.com/$Repo/releases/download/v$Version/checksums.txt"
$Checksums = (Invoke-WebRequest $ChecksumUrl).Content
$ArchiveName = "kint-data-cli_${Version}_windows_${Arch}.zip"
$Expected = ($Checksums -split "`n" | Where-Object { $_ -match [regex]::Escape($ArchiveName) }) -split '\s+' | Select-Object -First 1
$Actual = (Get-FileHash "$Tmp\kint-data.zip" -Algorithm SHA256).Hash.ToLower()
if (-not $Expected -or $Expected.ToLower() -ne $Actual) {
    throw "Checksum verification failed for $ArchiveName"
}

Expand-Archive "$Tmp\kint-data.zip" -DestinationPath $Tmp -Force
Move-Item "$Tmp\$Binary" "$InstallDir\$Binary" -Force
Remove-Item $Tmp -Recurse -Force

$UserPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($UserPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$UserPath;$InstallDir", "User")
    Write-Host "Added $InstallDir to user PATH (restart your shell)"
}

Write-Host "Installed kint-data v$Version to $InstallDir\$Binary"
Write-Host ""
Write-Host 'Tip: Set-Alias -Name kd -Value kint-data -Scope Global'
