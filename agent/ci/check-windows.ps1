# Windows runner checks, all in one step: the runner ends every process a
# step started when the step finishes, so the fake server and the agent live
# inside this one step. The one-line install runs in Windows PowerShell 5.1
# (powershell.exe) as a child process. Results as annotations; exit 1 if a
# required check failed.
# Env: CI_DIST (release files), CI_BIN (fakeserver.exe), CI_SCRIPTS (agent/ci).
param([bool]$Desktop = $true)
$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'

$out = Join-Path $env:GITHUB_WORKSPACE 'out'
New-Item -ItemType Directory -Force -Path $out | Out-Null
$base = 'http://127.0.0.1:18700'
$key = 'ci-ingest-key-0123456789'
$token = 'fake-access-token-0123456789'
$email = 'user-a@example.com'
$exe = Join-Path $env:LOCALAPPDATA 'Programs\ClaudeUsage\claude-usage.exe'
$installDir = Split-Path -Parent $exe
$data = Join-Path $env:LOCALAPPDATA 'ClaudeUsage'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$entryKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\ClaudeUsage'
$script:required = 0
$script:passed = 0
$script:failedIds = @()

function OneLine($s) {
    $t = ("$s" -replace "[\r\n]+", ' ') -replace '%', '%25'
    if ($t.Length -gt 300) { $t = $t.Substring(0, 300) }
    return $t
}
function Pass([string]$Id, $Msg) { $script:required++; $script:passed++; Write-Output "pass ${Id}: $(OneLine $Msg)" }
function Fail([string]$Id, $Msg) { $script:required++; $script:failedIds += $Id; Write-Output "::error title=${Id}::$(OneLine $Msg)" }
function Info([string]$Id, $Msg) { Write-Output "::notice title=${Id}::$(OneLine $Msg)" }
# Run one check; an exception is a failure of that check.
function Check([string]$Id, [scriptblock]$Body) {
    try { & $Body } catch { Fail $Id "exception: $($_.Exception.Message)" }
}
function Stats { try { Invoke-RestMethod -TimeoutSec 10 "$base/stats" } catch { $null } }
function DevStat([string]$Device) { $s = Stats; if ($s -and $s.devices) { $s.devices.$Device } else { $null } }
function AgentProcs { @(Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -and $_.Path -eq $exe }) }
function DeviceId { try { (Get-Content -Raw -LiteralPath (Join-Path $data 'device-id') -ErrorAction Stop).Trim() } catch { '' } }
function WaitFor([int]$Seconds, [scriptblock]$Cond) {
    $end = (Get-Date).AddSeconds($Seconds)
    while ($true) {
        if (& $Cond) { return $true }
        if ((Get-Date) -ge $end) { return $false }
        Start-Sleep -Seconds 2
    }
}
# Run a program with a hard timeout; output captured to files. $p.Handle is
# read at once so that ExitCode is known after the wait.
function RunExe([string]$Path, [string]$Arguments, [int]$TimeoutSec = 60) {
    $o = Join-Path $env:RUNNER_TEMP ([IO.Path]::GetRandomFileName())
    $p = Start-Process -FilePath $Path -ArgumentList $Arguments -NoNewWindow -PassThru `
        -RedirectStandardOutput "$o.out" -RedirectStandardError "$o.err" -ErrorAction Stop
    $null = $p.Handle
    if (-not $p.WaitForExit($TimeoutSec * 1000)) {
        try { $p.Kill() } catch { }
        return @{ timeout = $true; code = $null; out = "$(Get-Content -Raw "$o.out" -ErrorAction SilentlyContinue)"; err = '' }
    }
    return @{ timeout = $false; code = $p.ExitCode; out = "$(Get-Content -Raw "$o.out" -ErrorAction SilentlyContinue)"; err = "$(Get-Content -Raw "$o.err" -ErrorAction SilentlyContinue)" }
}
function OneLiner([string]$LogName) {
    # Windows PowerShell started from pwsh inherits pwsh's PSModulePath and
    # then cannot load its own modules (Get-FileHash is missing). A user's
    # PowerShell window does not have this, so start it with its defaults.
    $saved = $env:PSModulePath
    [Environment]::SetEnvironmentVariable('PSModulePath', $null, 'Process')
    try {
        $r = RunExe 'powershell.exe' "-NoProfile -ExecutionPolicy Bypass -Command `"irm $base/files/install-ci.ps1 | iex`"" 240
    } finally {
        [Environment]::SetEnvironmentVariable('PSModulePath', $saved, 'Process')
    }
    [IO.File]::WriteAllText((Join-Path $out $LogName), "$($r.out)`n--- stderr`n$($r.err)")
    return $r
}
function Shot([string]$Name) {
    try {
        Add-Type -AssemblyName System.Windows.Forms, System.Drawing
        $b = [System.Windows.Forms.Screen]::PrimaryScreen.Bounds
        $bmp = New-Object System.Drawing.Bitmap $b.Width, $b.Height
        $g = [System.Drawing.Graphics]::FromImage($bmp)
        $g.CopyFromScreen($b.Location, [System.Drawing.Point]::Empty, $b.Size)
        $bmp.Save((Join-Path $out $Name), [System.Drawing.Imaging.ImageFormat]::Png)
        $g.Dispose(); $bmp.Dispose()
        return "$Name $($b.Width)x$($b.Height)"
    } catch { return "screenshot failed: $($_.Exception.Message)" }
}

# fake sign-in and account (never real ones)
$cd = Join-Path $env:USERPROFILE '.claude'
New-Item -ItemType Directory -Force -Path $cd | Out-Null
$exp = [DateTimeOffset]::UtcNow.AddHours(8).ToUnixTimeMilliseconds()
[IO.File]::WriteAllText((Join-Path $cd '.credentials.json'), "{`"claudeAiOauth`":{`"accessToken`":`"$token`",`"expiresAt`":$exp,`"subscriptionType`":`"max`"}}")
[IO.File]::WriteAllText((Join-Path $env:USERPROFILE '.claude.json'), "{`"oauthAccount`":{`"accountUuid`":`"acc-ci-1`",`"emailAddress`":`"$email`",`"displayName`":`"User A`",`"organizationName`":`"Example`"}}")

# the fake team server
$null = Start-Process -FilePath (Join-Path $env:CI_BIN 'fakeserver.exe') `
    -ArgumentList @('-listen', '127.0.0.1:18700', '-key', $key, '-token', $token, '-files', $env:CI_DIST, '-scripts', $env:CI_SCRIPTS) `
    -RedirectStandardOutput (Join-Path $out 'fakeserver-out.log') -RedirectStandardError (Join-Path $out 'fakeserver.log') `
    -WindowStyle Hidden -PassThru
if (-not (WaitFor 30 { try { $null = Invoke-RestMethod -TimeoutSec 5 "$base/healthz"; $true } catch { $false } })) {
    Fail 'fakeserver' 'did not start'
}

function TempIcons { @(Get-ChildItem -Path $env:TEMP -Filter 'systray_temp_icon_*' -ErrorAction SilentlyContinue).Count }
$script:tempIconsBefore = TempIcons

$script:installAt = Get-Date
Check 'install.oneline' {
    $r = OneLiner 'install.log'
    $script:installAt = Get-Date
    if ($r.code -eq 0 -and (Test-Path -LiteralPath $exe) -and "$($r.out)" -match [regex]::Escape('under the ^ arrow')) { Pass 'install.oneline' 'exit 0' }
    else { Fail 'install.oneline' "timeout=$($r.timeout) code=$($r.code) exe=$(Test-Path -LiteralPath $exe) out: $("$($r.out)".Trim() -split "`n" | Select-Object -Last 3) err: $("$($r.err)".Trim() -split "`n" | Select-Object -First 4)" }
}

Check 'exe.version_info' {
    $v = (Get-Item -LiteralPath $exe).VersionInfo
    if ($v.ProductName -eq 'Claude Usage' -and $v.CompanyName -eq 'Deep-In-Sight' -and $v.ProductVersion -eq '0.0.0-ci' -and $v.FileVersion -eq '0.0.0.0') {
        Pass 'exe.version_info' "$($v.ProductName) $($v.ProductVersion)"
    } else { Fail 'exe.version_info' "product=$($v.ProductName) company=$($v.CompanyName) product_version=$($v.ProductVersion) file_version=$($v.FileVersion)" }
}

Check 'autostart.run_value' {
    $rv = (Get-ItemProperty -Path $runKey -Name 'ClaudeUsage' -ErrorAction SilentlyContinue).ClaudeUsage
    if ($rv -eq "`"$exe`" run") { Pass 'autostart.run_value' $rv } else { Fail 'autostart.run_value' "value=$rv" }
}

Check 'uninstall.entry' {
    $e = Get-ItemProperty -Path $entryKey -ErrorAction SilentlyContinue
    if ($e -and $e.DisplayName -eq 'Claude Usage' -and $e.DisplayVersion -eq '0.0.0-ci' -and $e.Publisher -eq 'Deep-In-Sight' -and $e.UninstallString -eq "`"$exe`" uninstall") {
        Pass 'uninstall.entry' $e.UninstallString
    } else { Fail 'uninstall.entry' "name=$($e.DisplayName) version=$($e.DisplayVersion) publisher=$($e.Publisher) uninstall=$($e.UninstallString)" }
}

$script:agentPid = $null
Check 'run.process' {
    $ok = WaitFor 15 { (AgentProcs).Count -eq 1 }
    $ps = AgentProcs
    if ($ok -and $ps.Count -eq 1) { $script:agentPid = $ps[0].Id; Pass 'run.process' "pid $($script:agentPid)" }
    else { Fail 'run.process' "processes=$($ps.Count)" }
}

$script:device = DeviceId
Check 'sync.arrives' {
    $ok = WaitFor 90 { $d = DevStat $script:device; $d -and $d.syncs -ge 2 -and $d.last_auth_state -eq 'ok' -and $d.last_email -eq $email -and $d.last_version -eq '0.0.0-ci' }
    $d = DevStat $script:device
    if ($ok) { Pass 'sync.arrives' "device $($script:device)" }
    else { Fail 'sync.arrives' "device=$($script:device) stats=$(ConvertTo-Json -Compress -InputObject $d)" }
}

Check 'sync.mode_tray' {
    $d = DevStat $script:device
    if ($d -and $d.last_mode -eq 'tray') { Pass 'sync.mode_tray' 'tray' } else { Fail 'sync.mode_tray' "last_mode=$($d.last_mode)" }
}

Check 'status.json' {
    $r = RunExe $exe 'status --json' 30
    [IO.File]::WriteAllText((Join-Path $out 'status.json'), "$($r.out)")
    $j = $null; try { $j = "$($r.out)" | ConvertFrom-Json } catch { }
    if ($j -and $j.snapshot.client_mode -eq 'tray' -and $j.snapshot.mode -eq 'coordinated') { Pass 'status.json' 'coordinated tray' }
    else { Fail 'status.json' "code=$($r.code) out=$($r.out) err=$($r.err)" }
}

Check 'run.no_listen' {
    $tcp = @(Get-NetTCPConnection -State Listen -OwningProcess $script:agentPid -ErrorAction SilentlyContinue)
    $udp = @(Get-NetUDPEndpoint -OwningProcess $script:agentPid -ErrorAction SilentlyContinue)
    if ($script:agentPid -and $tcp.Count -eq 0 -and $udp.Count -eq 0) { Pass 'run.no_listen' "pid $($script:agentPid)" }
    else { Fail 'run.no_listen' "pid=$($script:agentPid) tcp=$($tcp.Count) udp=$($udp.Count)" }
}

Check 'fakeserver.token_leaks' {
    $s = Stats
    if ($s -and $s.token_leaks -eq 0) { Pass 'fakeserver.token_leaks' '0' } else { Fail 'fakeserver.token_leaks' "$($s.token_leaks)" }
}

if ($Desktop) {
    Check 'notice.consumed' {
        $left = ($script:installAt.AddSeconds(30) - (Get-Date)).TotalSeconds
        if ($left -gt 0) { Start-Sleep -Seconds ([int][Math]::Ceiling($left)) }
        if (-not (Test-Path -LiteralPath (Join-Path $data 'notify-pending'))) { Pass 'notice.consumed' 'shown' }
        else { Fail 'notice.consumed' 'notify-pending is still there 30 s after install' }
    }
    Info 'tray.screenshot' (Shot 'tray.png')
}
Check 'tray.no_temp_icons' {
    $left = ($script:installAt.AddSeconds(30) - (Get-Date)).TotalSeconds
    if ($left -gt 0) { Start-Sleep -Seconds ([int][Math]::Ceiling($left)) }
    $n = TempIcons
    if ($n -le $script:tempIconsBefore) { Pass 'tray.no_temp_icons' "$n" } else { Fail 'tray.no_temp_icons' "systray_temp_icon_* in TEMP: $($script:tempIconsBefore) -> $n" }
}
Info 'exe.signature' "$((Get-AuthenticodeSignature -LiteralPath $exe).Status)"

Check 'upgrade.in_place' {
    $before = (DevStat $script:device).syncs
    $oldPid = $script:agentPid
    $r = OneLiner 'install-upgrade.log'
    $ok = WaitFor 60 { $ps = AgentProcs; $d = DevStat $script:device; $ps.Count -eq 1 -and $ps[0].Id -ne $oldPid -and $d.syncs -gt $before }
    $ps = AgentProcs
    $same = (DeviceId) -eq $script:device
    if ($r.code -eq 0 -and $ok -and $same) { $script:agentPid = $ps[0].Id; Pass 'upgrade.in_place' "pid $oldPid -> $($ps[0].Id)" }
    else { Fail 'upgrade.in_place' "code=$($r.code) processes=$($ps.Count) pids=$($ps.Id -join ',') old=$oldPid device_same=$same syncs $before -> $((DevStat $script:device).syncs)" }
}

Copy-Item -LiteralPath (Join-Path $data 'agent.log') -Destination $out -ErrorAction SilentlyContinue

Check 'uninstall.via_entry' {
    $u = (Get-ItemProperty -Path $entryKey -ErrorAction SilentlyContinue).UninstallString
    if ("$u" -notmatch '^"([^"]+)"\s+(.+)$') { Fail 'uninstall.via_entry' "UninstallString=$u"; return }
    $r = RunExe $Matches[1] $Matches[2] 120
    [IO.File]::WriteAllText((Join-Path $out 'uninstall.log'), "$($r.out)`n--- stderr`n$($r.err)")
    Start-Sleep -Seconds 3
    $rv = (Get-ItemProperty -Path $runKey -Name 'ClaudeUsage' -ErrorAction SilentlyContinue).ClaudeUsage
    $problems = @()
    if ($r.code -ne 0) { $problems += "code=$($r.code) timeout=$($r.timeout)" }
    if ($rv) { $problems += 'run_value_left' }
    if (Test-Path -Path $entryKey) { $problems += 'entry_left' }
    if (Test-Path -LiteralPath $installDir) { $problems += 'install_dir_left' }
    if (Test-Path -LiteralPath $data) { $problems += 'data_left' }
    if ((AgentProcs).Count -ne 0) { $problems += "processes=$((AgentProcs).Count)" }
    if ($problems.Count -eq 0) { Pass 'uninstall.via_entry' 'everything removed' } else { Fail 'uninstall.via_entry' ($problems -join ' ') }
}

$failed = ''
if ($script:failedIds.Count -gt 0) { $failed = " failed: $($script:failedIds -join ' ')" }
Write-Output "::notice title=summary::$($script:passed)/$($script:required)$failed"
if ($script:passed -ne $script:required) { exit 1 }
exit 0
