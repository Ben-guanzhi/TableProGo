# W15 (private distribution) - build a release archive.
#
# There is no WinGet/Scoop manifest and no publish step here, because the mainline
# is not public. This script produces what a private build actually needs:
#
#   dist/tablepro-go-<version>-<os>-<arch>.zip   the binary (the frontend is
#                                                embedded - see //go:embed)
#   dist/SHA256SUMS.txt                          so a recipient can verify what
#                                                they were handed
#   dist/RELEASE-NOTES.md                        the changelog section for this
#                                                version, copied from CHANGELOG.md
#
# Usage:
#   powershell -File scripts/release.ps1 -Version 1.2.0
#   powershell -File scripts/release.ps1 -Version 1.2.0 -SkipTests   # not advised
#
# Messages are ASCII on purpose: Windows PowerShell 5.1 reads a BOM-less .ps1 as
# ANSI, so a Chinese string literal here would split and fail to parse.

[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$Version,
  [string]$OutDir = "dist",
  [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Push-Location $root
try {
  if ($Version -notmatch '^\d+\.\d+\.\d+') {
    throw "version must look like 1.2.0 (got: $Version)"
  }

  # The frontend is embedded, so a stale dist would ship yesterday's UI.
  Write-Host '== build frontend =='
  Push-Location frontend
  try {
    npm run build
    if ($LASTEXITCODE -ne 0) { throw 'frontend build failed' }
  } finally { Pop-Location }

  if (-not $SkipTests) {
    Write-Host '== test =='
    go build ./...
    if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
    go test ./... -count=1
    if ($LASTEXITCODE -ne 0) { throw 'go test failed (use -SkipTests only if you are sure)' }
  }

  $commit = (git rev-parse --short HEAD).Trim()
  $date = (Get-Date).ToString('yyyy-MM-dd')
  $goos = if ($env:GOOS) { $env:GOOS } else { 'windows' }
  $goarch = if ($env:GOARCH) { $env:GOARCH } else { 'amd64' }

  if (-not (Test-Path $OutDir)) { New-Item -ItemType Directory -Path $OutDir | Out-Null }
  $exe = if ($goos -eq 'windows') { 'tablepro-go.exe' } else { 'tablepro-go' }
  $binary = Join-Path $OutDir $exe

  Write-Host "== compile $Version ($commit, $date, $goos/$goarch) =="
  $ldflags = "-X main.buildVersion=$Version -X main.buildCommit=$commit -X main.buildDate=$date"
  $env:CGO_ENABLED = '1'   # mattn/go-sqlite3 needs cgo
  go build -trimpath -ldflags $ldflags -o $binary .
  if ($LASTEXITCODE -ne 0) { throw 'go build failed' }

  # Prove the stamp took: an archive whose --version says "dev" is a packaging bug
  # that only surfaces in the recipient's bug report.
  $reported = & $binary --version
  Write-Host "   $reported"
  if ($reported -notmatch [regex]::Escape($Version)) {
    throw "the binary does not report $Version (got: $reported)"
  }

  $zipName = "tablepro-go-$Version-$goos-$goarch.zip"
  $zipPath = Join-Path $OutDir $zipName
  if (Test-Path $zipPath) { Remove-Item $zipPath -Force }

  # Ship the changelog section beside the binary: a private recipient has no
  # release page to read.
  $notesPath = Join-Path $OutDir 'RELEASE-NOTES.md'
  $changelog = Get-Content 'CHANGELOG.md' -Raw -Encoding UTF8
  $header = "## [$Version]"
  $start = $changelog.IndexOf($header)
  if ($start -ge 0) {
    $next = $changelog.IndexOf("`n## [", $start + $header.Length)
    $section = if ($next -ge 0) { $changelog.Substring($start, $next - $start) } else { $changelog.Substring($start) }
    Set-Content -Path $notesPath -Value $section.Trim() -Encoding UTF8
  } else {
    Set-Content -Path $notesPath -Value "# $Version" -Encoding UTF8
  }

  $stage = Join-Path $OutDir 'stage'
  if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
  New-Item -ItemType Directory -Path $stage | Out-Null
  Copy-Item $binary (Join-Path $stage $exe)
  Copy-Item $notesPath (Join-Path $stage 'RELEASE-NOTES.md')
  if (Test-Path 'LICENSE') { Copy-Item 'LICENSE' (Join-Path $stage 'LICENSE') }

  Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $zipPath -Force
  Remove-Item $stage -Recurse -Force
  Remove-Item $binary -Force

  $hash = (Get-FileHash $zipPath -Algorithm SHA256).Hash.ToLower()
  $sums = Join-Path $OutDir 'SHA256SUMS.txt'
  "sha256  $zipName" | Set-Content -Path $sums -Encoding UTF8
  "$hash  $zipName" | Add-Content -Path $sums -Encoding UTF8

  Write-Host ''
  Write-Host '== done =='
  Write-Host "  $zipPath"
  Write-Host "  sha256 $hash"
  Write-Host ''
  Write-Host 'Distribute only this zip plus SHA256SUMS.txt. The recipient verifies with:'
  Write-Host "  Get-FileHash .\$zipName -Algorithm SHA256"
} finally {
  Pop-Location
}
