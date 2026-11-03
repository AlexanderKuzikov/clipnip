param(
    [Parameter(Mandatory = $true)][string]$Root,
    [string]$FFmpeg = "",
    [switch]$KeepBackup
)

$ErrorActionPreference = "Stop"

if (-not $FFmpeg) {
    $candidates = @(
        (Join-Path $PSScriptRoot "..\clipnip-bin\ffmpeg.exe"),
        (Join-Path $env:LOCALAPPDATA "clipnip\bin\ffmpeg.exe")
    )
    foreach ($c in $candidates) {
        if (Test-Path -LiteralPath $c) { $FFmpeg = $c; break }
    }
}
if (-not $FFmpeg -or -not (Test-Path -LiteralPath $FFmpeg)) {
    throw "ffmpeg not found; pass -FFmpeg <path>"
}
$FFmpeg = (Resolve-Path -LiteralPath $FFmpeg).Path

function Get-DurationSeconds([string]$probeText) {
    $m = [regex]::Match($probeText, 'Duration:\s*(\d+):(\d+):(\d+(?:\.\d+)?)')
    if (-not $m.Success) { return $null }
    return [double]$m.Groups[1].Value * 3600 + [double]$m.Groups[2].Value * 60 + [double]$m.Groups[3].Value
}

$files = Get-ChildItem -LiteralPath $Root -Recurse -File -Filter "*.mp4" -ErrorAction Stop
$fixed = 0
$skipped = 0
$failed = 0

foreach ($file in $files) {
    $probe = & $FFmpeg -hide_banner -nostdin -i $file.FullName 2>&1 | Out-String
    if ($probe -notmatch 'major_brand\s*:\s*dash') {
        $skipped++
        continue
    }
    if ($probe -match '(?m)^\s*Stream #\d+:\d+.*: Audio:') {
        # DASH с аудио не трогаем: это не файл, потерявший звук.
        $skipped++
        continue
    }

    $tmp = $file.FullName + ".fix.mp4"
    $bak = $file.FullName + ".bak"
    Remove-Item -LiteralPath $tmp, $bak -Force -ErrorAction SilentlyContinue
    Write-Host "fixing $($file.FullName)"
    & $FFmpeg -hide_banner -nostdin -y -i $file.FullName -map 0 -map -0:a -c copy -movflags +faststart -loglevel error $tmp
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $tmp)) {
        Write-Warning "remux failed: $($file.FullName)"
        Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
        $failed++
        continue
    }

    $newProbe = & $FFmpeg -hide_banner -nostdin -i $tmp 2>&1 | Out-String
    $oldDur = Get-DurationSeconds $probe
    $newDur = Get-DurationSeconds $newProbe
    $durationOK = $null -ne $oldDur -and $null -ne $newDur -and [math]::Abs($oldDur - $newDur) -le 0.5
    if ($newProbe -match 'major_brand\s*:\s*dash' -or $newProbe -notmatch 'Stream #\d+:\d+.*: Video:' -or -not $durationOK) {
        Write-Warning "verification failed: $($file.FullName)"
        Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
        $failed++
        continue
    }

    & $FFmpeg -v error -i $tmp -map 0:v:0 -c copy -f null - 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Warning "demux check failed: $($file.FullName)"
        Remove-Item -LiteralPath $tmp -Force -ErrorAction SilentlyContinue
        $failed++
        continue
    }

    [System.IO.File]::Replace($tmp, $file.FullName, $bak, $true)
    if ($KeepBackup) {
        Write-Host "  backup kept: $bak"
    } else {
        Remove-Item -LiteralPath $bak -Force
    }
    $fixed++
}

Write-Host "done: fixed=$fixed skipped=$skipped failed=$failed"
