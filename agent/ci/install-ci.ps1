# CI stand-in for the installer the team server generates (the same contract:
# download the release file, check it against SHA256SUMS, write config.json
# without a BOM, run "install --config" as a direct child and wait only for
# it; exit code 0 or 10 means installed). fakeserver fills in @BASE@ and
# @KEY@. Fake values only.
# Run: irm <base>/files/install-ci.ps1 | iex
& {
  $ErrorActionPreference = 'Stop'
  $ProgressPreference = 'SilentlyContinue'
  $Base = '@BASE@'
  $File = 'ai-usage-windows-amd64.exe'
  $tmp = Join-Path ([IO.Path]::GetTempPath()) ('ai-usage-' + [guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $tmp | Out-Null
  try {
    $exe = Join-Path $tmp 'ai-usage.exe'
    $sums = Join-Path $tmp 'SHA256SUMS'
    $cfg = Join-Path $tmp 'config.json'
    $out = Join-Path $tmp 'install-out.txt'
    $err = Join-Path $tmp 'install-err.txt'
    Write-Host "Downloading $File from $Base ..."
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/files/$File" -OutFile $exe
    Invoke-WebRequest -UseBasicParsing -Uri "$Base/files/SHA256SUMS" -OutFile $sums
    $want = $null
    foreach ($line in Get-Content -LiteralPath $sums) {
      if ($line -match '^([0-9a-f]{64})  (\S+)$' -and $Matches[2] -eq $File) { $want = $Matches[1] }
    }
    if (-not $want -or (Get-FileHash -Algorithm SHA256 -LiteralPath $exe).Hash.ToLowerInvariant() -ne $want) {
      throw 'Downloaded file hash does not match SHA256SUMS. Install stopped.'
    }
    [IO.File]::WriteAllText($cfg, '{"server_urls":["' + $Base + '"],"ingest_key":"@KEY@"}')
    $p = Start-Process -FilePath $exe -ArgumentList @('install', '--config', ('"' + $cfg + '"')) -NoNewWindow -PassThru -RedirectStandardOutput $out -RedirectStandardError $err
    $null = $p.Handle
    if (-not $p.WaitForExit(120000)) {
      try { $p.Kill() } catch { }
      throw 'The installer did not finish within 2 minutes.'
    }
    foreach ($f in @($out, $err)) {
      if (Test-Path -LiteralPath $f) { Get-Content -LiteralPath $f | ForEach-Object { Write-Host $_ } }
    }
    $code = $p.ExitCode
    if ($code -ne 0 -and $code -ne 10) { throw "Install failed (exit code $code)." }
    Write-Host "install-ci: installed (exit code $code)"
  } finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
  }
}
