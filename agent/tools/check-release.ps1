# The Windows half of tools/check-release.sh: the Windows release file
# reports exactly VERSION, and a run with every CI test switch set (a new
# LOCALAPPDATA, nothing signed in, a server address that refuses) logs no
# "CI build" line and writes no test view. VERSION must be a release's;
# with -Rehearsal (ordinary CI) it must instead be one that can never be
# released, as tools/version.sh release_version says.
# Usage: pwsh tools/check-release.ps1 [-Rehearsal] FILE VERSION
param([Parameter(Mandatory)][string]$File, [Parameter(Mandatory)][string]$Version, [switch]$Rehearsal)
$ErrorActionPreference = 'Stop'
function Fail([string]$Msg) { Write-Output "check-release: $File`: $Msg"; exit 1 }

# tools/version.sh release_version, the same expression
$testBuild = $Version -cmatch '^v?0\.0\.0([-.+]|$)|-(([0-9A-Za-z-]+\.)*)(ci|dev|dryrun|relcheck)(\.|$)'
if ($Rehearsal -and -not $testBuild) { Fail "a rehearsal needs a version that can never be released, not $Version" }
if (-not $Rehearsal -and $testBuild) { Fail "not a release version: $Version" }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ('check-release-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
$saved = @{}
$vars = @{
    'LOCALAPPDATA'                 = (Join-Path $tmp 'local')
    'USERPROFILE'                  = (Join-Path $tmp 'u')
    'AI_USAGE_TEST_FAKE_PROVIDERS' = 'claude,codex'
    'AI_USAGE_TEST_DUMP_VIEW'      = (Join-Path $tmp 'view.json')
    'AI_USAGE_TEST_CODEX_FALLBACK' = (Join-Path $tmp 'codex.exe')
}
try {
    $o = Join-Path $tmp 'version.txt'
    $p = Start-Process -FilePath $File -ArgumentList 'version' -NoNewWindow -PassThru -RedirectStandardOutput $o -RedirectStandardError "$o.err"
    $null = $p.Handle
    if (-not $p.WaitForExit(30000)) { try { $p.Kill() } catch { }; Fail 'version did not answer' }
    $out = "$(Get-Content -Raw -LiteralPath $o -ErrorAction SilentlyContinue)".Trim()
    $f = $out -split '\s+'
    if ($f.Count -lt 2 -or $f[0] -ne 'ai-usage' -or $f[1] -cne $Version) { Fail "reports [$out], not $Version" }

    # every switch a CI build reads (internal/cli testSwitches)
    $data = Join-Path $vars['LOCALAPPDATA'] 'AIUsage'
    New-Item -ItemType Directory -Force -Path $data, $vars['USERPROFILE'] | Out-Null
    [IO.File]::WriteAllText((Join-Path $data 'config.json'), '{"server_urls":["http://127.0.0.1:9"],"ingest_key":"check-release-key-0123456789"}')
    foreach ($k in $vars.Keys) { $saved[$k] = [Environment]::GetEnvironmentVariable($k, 'Process'); [Environment]::SetEnvironmentVariable($k, $vars[$k], 'Process') }
    try {
        $r = Start-Process -FilePath $File -ArgumentList @('run', '--headless') -PassThru -WindowStyle Hidden
    } finally {
        foreach ($k in $vars.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k], 'Process') }
    }
    $log = Join-Path $data 'agent.log'
    $started = $false
    for ($i = 0; $i -lt 30 -and -not $started; $i++) {
        Start-Sleep -Milliseconds 200
        $t = ''
        try {
            $fs = [IO.File]::Open($log, 'Open', 'Read', 'ReadWrite, Delete')
            try { $t = (New-Object IO.StreamReader($fs)).ReadToEnd() } finally { $fs.Dispose() }
        } catch { }
        $started = $t.Contains("ai-usage $Version started")
    }
    Start-Sleep -Seconds 4
    Stop-Process -Id $r.Id -Force -ErrorAction SilentlyContinue
    $null = $r.WaitForExit(10000)
    $t = "$(Get-Content -Raw -LiteralPath $log -ErrorAction SilentlyContinue)"
    if (-not $t.Contains("ai-usage $Version started")) { Fail "the run did not start: $($t.Substring(0, [Math]::Min(300, $t.Length)))" }
    if ($t.Contains('CI build')) { Fail "behaves as a CI build: $((($t -split "`n") | Where-Object { $_ -match 'CI build' } | Select-Object -First 5) -join ' ')" }
    if (Test-Path -LiteralPath $vars['AI_USAGE_TEST_DUMP_VIEW']) { Fail 'wrote the test view' }
    Write-Output "check-release: $File ok ($Version, no test switch honoured)"
} finally {
    Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
}
