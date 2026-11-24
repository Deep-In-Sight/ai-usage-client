# Windows runner checks, all in one step: the runner ends every process a
# step started when the step finishes, so the fake server and the agent live
# inside this one step. The one-line install runs in Windows PowerShell 5.1
# (powershell.exe) as a child process. Results as annotations; exit 1 if a
# required check failed.
# Env: CI_DIST (release files), CI_BIN (fakeserver.exe), CI_SCRIPTS (agent/ci).
param([bool]$Desktop = $true)
# These checks overwrite the Claude and Codex sign-ins, install and remove
# the agent, and (the release check) change the hosts file and the trusted
# CAs: only ever on a GitHub Actions runner, never a PC.
if ($env:GITHUB_ACTIONS -ne 'true' -or -not $env:RUNNER_TEMP) {
    [Console]::Error.WriteLine('agent/ci: refusing to run: these checks overwrite sign-ins and system settings and run only on a GitHub Actions runner (GITHUB_ACTIONS=true, RUNNER_TEMP set)')
    exit 2
}
$ErrorActionPreference = 'Continue'
$ProgressPreference = 'SilentlyContinue'

$out = Join-Path $env:GITHUB_WORKSPACE 'out'
New-Item -ItemType Directory -Force -Path $out | Out-Null
$base = 'http://127.0.0.1:18700'
$key = 'ci-ingest-key-0123456789'
$token = 'fake-access-token-0123456789'
$email = 'user-a@example.com'
# the fake Codex sign-in (lib.sh has the same values)
$codexSig = 'fake-codex-sig-ci-0123456789'
$codexRefresh = 'fake-codex-refresh-ci-0123456789'
$codexEmail = 'user-ci@example.com'
$codexAccount = '00000000-0000-4000-8000-000000000001'
$exe = Join-Path $env:LOCALAPPDATA 'Programs\AIUsage\ai-usage.exe'
$installDir = Split-Path -Parent $exe
$data = Join-Path $env:LOCALAPPDATA 'AIUsage'
$runKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run'
$entryKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\AIUsage'
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
# Every process of the installed executable: the agent ("run") and its
# tray helpers ("tray-helper", design 6.4), told apart by the command line.
function AllProcs { @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.ExecutablePath -and $_.ExecutablePath -eq $exe }) }
function AgentProcs { @(AllProcs | Where-Object { "$($_.CommandLine)" -notmatch 'tray-helper' } | ForEach-Object { Get-Process -Id $_.ProcessId -ErrorAction SilentlyContinue }) }
function HelperProcs { @(AllProcs | Where-Object { "$($_.CommandLine)" -match 'tray-helper' }) }
# Top-level windows of a class that a process owns (the tray library's
# hidden window is "SystrayClass").
Add-Type -TypeDefinition @'
using System;
using System.Text;
using System.Runtime.InteropServices;
public static class CiWindows {
    delegate bool EnumProc(IntPtr h, IntPtr l);
    [DllImport("user32.dll")] static extern bool EnumWindows(EnumProc f, IntPtr l);
    [DllImport("user32.dll")] static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] static extern int GetClassName(IntPtr h, StringBuilder s, int n);
    public static int Count(uint pid, string cls) {
        int n = 0;
        EnumWindows((h, l) => {
            uint p; GetWindowThreadProcessId(h, out p);
            if (p == pid) { var sb = new StringBuilder(256); GetClassName(h, sb, 256); if (sb.ToString() == cls) n++; }
            return true;
        }, IntPtr.Zero);
        return n;
    }
}
'@
# Helpers of the agent with pid $Parent that each own a SystrayClass window.
function HelpersWithWindow([int]$Parent) { @(HelperProcs | Where-Object { $_.ParentProcessId -eq $Parent -and [CiWindows]::Count([uint32]$_.ProcessId, 'SystrayClass') -ge 1 }) }
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
function OneLiner([string]$LogName, [string]$From = $base) {
    # Windows PowerShell started from pwsh inherits pwsh's PSModulePath and
    # then cannot load its own modules (Get-FileHash is missing). A user's
    # PowerShell window does not have this, so start it with its defaults.
    $saved = $env:PSModulePath
    [Environment]::SetEnvironmentVariable('PSModulePath', $null, 'Process')
    try {
        $r = RunExe 'powershell.exe' "-NoProfile -ExecutionPolicy Bypass -Command `"irm $From/files/install-ci.ps1 | iex`"" 240
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

# A file's text, read while the agent may still write it (agent.log).
# A file the agent keeps locked (run.lock) reads as "".
function ReadShared([string]$Path) {
    try {
        $fs = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete)
        try { (New-Object IO.StreamReader($fs)).ReadToEnd() } finally { $fs.Dispose() }
    } catch { '' }
}
# fake sign-in and account (never real ones)
$cd = Join-Path $env:USERPROFILE '.claude'
New-Item -ItemType Directory -Force -Path $cd | Out-Null
$exp = [DateTimeOffset]::UtcNow.AddHours(8).ToUnixTimeMilliseconds()
[IO.File]::WriteAllText((Join-Path $cd '.credentials.json'), "{`"claudeAiOauth`":{`"accessToken`":`"$token`",`"expiresAt`":$exp,`"subscriptionType`":`"max`"}}")
[IO.File]::WriteAllText((Join-Path $env:USERPROFILE '.claude.json'), "{`"oauthAccount`":{`"accountUuid`":`"acc-ci-1`",`"emailAddress`":`"$email`",`"displayName`":`"User A`",`"organizationName`":`"Example`"}}")

# the fake Codex sign-in: fake JWTs ending in $codexSig, the access token
# expires in 9 days
function B64Url([string]$s) { [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($s)).TrimEnd('=').Replace('+', '-').Replace('/', '_') }
$cxAuth = '{"chatgpt_account_id":"' + $codexAccount + '","chatgpt_user_id":"user-ci","chatgpt_plan_type":"plus"}'
$cxExp = [DateTimeOffset]::UtcNow.AddDays(9).ToUnixTimeSeconds()
$cxHeader = B64Url '{"alg":"none"}'
$cxAccess = $cxHeader + '.' + (B64Url ('{"exp":' + $cxExp + ',"https://api.openai.com/auth":' + $cxAuth + '}')) + '.' + $codexSig
$cxId = $cxHeader + '.' + (B64Url ('{"email":"' + $codexEmail + '","https://api.openai.com/auth":' + $cxAuth + '}')) + '.' + $codexSig
$cxRefreshed = [DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')
$cxHome = Join-Path $env:USERPROFILE '.codex'
New-Item -ItemType Directory -Force -Path $cxHome | Out-Null
$cxJson = '{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"last_refresh":"' + $cxRefreshed + '","tokens":{"access_token":"' + $cxAccess +
    '","id_token":"' + $cxId + '","refresh_token":"' + $codexRefresh + '","account_id":"' + $codexAccount + '"}}'
[IO.File]::WriteAllText((Join-Path $cxHome 'auth.json'), $cxJson)
Info 'codex.fake_signin' "auth.json $($cxJson.Length) bytes, access token $($cxAccess.Split('.').Count) parts"

# the fake team server
$null = Start-Process -FilePath (Join-Path $env:CI_BIN 'fakeserver.exe') `
    -ArgumentList @('-listen', '127.0.0.1:18700', '-key', $key, '-token', "$token,$codexSig,$codexRefresh", '-files', $env:CI_DIST, '-scripts', $env:CI_SCRIPTS) `
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
    if ($v.ProductName -eq 'AI Usage' -and $v.CompanyName -eq 'Deep-In-Sight' -and $v.ProductVersion -eq '0.0.0-ci' -and $v.FileVersion -eq '0.0.0.0') {
        Pass 'exe.version_info' "$($v.ProductName) $($v.ProductVersion)"
    } else { Fail 'exe.version_info' "product=$($v.ProductName) company=$($v.CompanyName) product_version=$($v.ProductVersion) file_version=$($v.FileVersion)" }
}

Check 'autostart.run_value' {
    $rv = (Get-ItemProperty -Path $runKey -Name 'AIUsage' -ErrorAction SilentlyContinue).AIUsage
    if ($rv -eq "`"$exe`" run") { Pass 'autostart.run_value' $rv } else { Fail 'autostart.run_value' "value=$rv" }
}

Check 'uninstall.entry' {
    $e = Get-ItemProperty -Path $entryKey -ErrorAction SilentlyContinue
    if ($e -and $e.DisplayName -eq 'AI Usage' -and $e.DisplayVersion -eq '0.0.0-ci' -and $e.Publisher -eq 'Deep-In-Sight' -and $e.UninstallString -eq "`"$exe`" uninstall") {
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
    $ok = WaitFor 90 { $script:device = DeviceId; $d = DevStat $script:device; $d -and $d.syncs -ge 2 -and $d.last_auth_state -eq 'ok' -and $d.last_email -eq $email -and $d.last_version -eq '0.0.0-ci' }
    $d = DevStat $script:device
    if ($ok) { Pass 'sync.arrives' "device $($script:device)" }
    else { Fail 'sync.arrives' "device=$($script:device) stats=$(ConvertTo-Json -Compress -InputObject $d)" }
}

# The agent reports "tray" once a helper's icon is in the notification
# area (design 6.4); a runner without a desktop may have none.
if ($Desktop) {
    Check 'sync.mode_tray' {
        $ok = WaitFor 60 { $d = DevStat $script:device; $d -and $d.last_mode -eq 'tray' }
        $d = DevStat $script:device
        if ($ok) { Pass 'sync.mode_tray' 'tray' } else { Fail 'sync.mode_tray' "last_mode=$($d.last_mode)" }
    }
} else {
    Info 'sync.mode' "$((DevStat $script:device).last_mode)"
}

# The CI build sends nothing to Anthropic or OpenAI (its log says so).
Check 'run.vendors_off' {
    $log = ReadShared (Join-Path $data 'agent.log')
    if ($log.Contains('CI build: no request to Anthropic or OpenAI')) { Pass 'run.vendors_off' 'CI build: no request to Anthropic or OpenAI' }
    else { Fail 'run.vendors_off' 'no such line in agent.log' }
}

# The Codex block is signed in, with the fake Codex account.
Check 'sync.codex' {
    $ok = WaitFor 30 { $d = DevStat $script:device; $d -and $d.providers.codex.last_auth_state -eq 'ok' -and $d.providers.codex.last_email -eq $codexEmail }
    if ($ok) { Pass 'sync.codex' "codex ok, $codexEmail" } else { Fail 'sync.codex' "providers=$(ConvertTo-Json -Compress -InputObject (DevStat $script:device).providers)" }
}

# Claude and Codex signed in: one tray helper each, children of the
# agent, each with its tray window.
Check 'tray.provider_helpers' {
    $ok = WaitFor 30 { (HelpersWithWindow $script:agentPid).Count -eq 2 -and (HelperProcs).Count -eq 2 }
    $h = HelperProcs
    if ($ok) { Pass 'tray.provider_helpers' "helpers $($h.ProcessId -join ',') of agent $($script:agentPid)" }
    else { Fail 'tray.provider_helpers' "helpers=$($h.Count) with_window=$((HelpersWithWindow $script:agentPid).Count) cmd=$($h.CommandLine -join ' | ')" }
}

# The helper's command line names nothing but the subcommand: its
# provider, icon id and icon folder come over its standard input.
Check 'tray.helper_cmdline' {
    $c = "$((HelperProcs | Select-Object -First 1).CommandLine)"
    if ($c -match '(^|[\s"])tray-helper\s*$' -and $c -notmatch '--') { Pass 'tray.helper_cmdline' $c } else { Fail 'tray.helper_cmdline' "[$c]" }
}

Check 'status.json' {
    $r = RunExe $exe 'status --json' 30
    [IO.File]::WriteAllText((Join-Path $out 'status.json'), "$($r.out)")
    $j = $null; try { $j = "$($r.out)" | ConvertFrom-Json } catch { }
    if ($j -and ($j.snapshot.client_mode -eq 'tray' -or -not $Desktop) -and $j.snapshot.mode -eq 'coordinated') { Pass 'status.json' "coordinated $($j.snapshot.client_mode)" }
    else { Fail 'status.json' "code=$($r.code) out=$($r.out) err=$($r.err)" }
}

# The status command shows Claude's and Codex's parts, the Codex rows
# from the server's latest (5-hour 42 %, weekly 17 %).
Check 'status.both' {
    $ok = WaitFor 30 {
        $r = RunExe $exe 'status' 30; $script:statusText = "$($r.out)"
        $script:statusText -match '(?m)^Claude:\s+signed in' -and $script:statusText -match '(?m)^Codex:\s+signed in \(Plus\)' -and
            $script:statusText -match '(?m)^\s+5-hour\s+42%' -and $script:statusText -match '(?m)^\s+Weekly\s+17%'
    }
    [IO.File]::WriteAllText((Join-Path $out 'status-both.txt'), "$script:statusText")
    if ($ok) { Pass 'status.both' 'Claude and Codex (Plus): 5-hour 42%, Weekly 17%' } else { Fail 'status.both' $script:statusText }
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

# No fake token (Claude's or Codex's) in the agent's files.
Check 'files.token_leaks' {
    $hits = @(Get-ChildItem -LiteralPath $data -Recurse -File -ErrorAction SilentlyContinue |
        Where-Object { $c = ReadShared $_.FullName; $c.Contains($token) -or $c.Contains($codexSig) -or $c.Contains($codexRefresh) } |
        ForEach-Object { $_.Name })
    if ($hits.Count -eq 0) { Pass 'files.token_leaks' 'none' } else { Fail 'files.token_leaks' ($hits -join ' ') }
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

# Two helpers (the CI build's test switch AI_USAGE_TEST_FAKE_PROVIDERS):
# two child processes, each with its own tray window (design D3; uIDs
# 100 and 101). The agent is started by hand for this and ended after it;
# the upgrade below starts the installed one again.
Check 'tray.two_helpers' {
    foreach ($p in AllProcs) { Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue }
    $null = WaitFor 15 { (AllProcs).Count -eq 0 }
    $env:AI_USAGE_TEST_FAKE_PROVIDERS = 'claude,codex'
    try { $p2 = Start-Process -FilePath $exe -ArgumentList 'run' -PassThru } finally { Remove-Item Env:AI_USAGE_TEST_FAKE_PROVIDERS }
    $ok = WaitFor 60 { (HelpersWithWindow $p2.Id).Count -eq 2 }
    $h = @(HelperProcs | Where-Object { $_.ParentProcessId -eq $p2.Id })
    # each helper logs its notification icon id (it is no longer on the
    # command line)
    $log = Get-Content -LiteralPath (Join-Path $data 'agent.log') -Raw -ErrorAction SilentlyContinue
    $ids = @{}
    foreach ($m in [regex]::Matches("$log", '\[tray (claude|codex)\] notification icon id (\d+)')) { $ids[$m.Groups[1].Value] = $m.Groups[2].Value }
    $uids = (@($ids.Keys | Sort-Object | ForEach-Object { "$_=$($ids[$_])" })) -join ','
    Info 'tray.two_helpers.measured' "helpers=$($h.Count) with SystrayClass window=$((HelpersWithWindow $p2.Id).Count) uids=$uids"
    if ($Desktop) { Info 'tray.screenshot.two' (Shot 'tray-two.png') }
    if ($ok -and $ids['claude'] -eq '100' -and $ids['codex'] -eq '101') { Pass 'tray.two_helpers' "agent $($p2.Id): helpers $($h.ProcessId -join ',') uids $uids" }
    else { Fail 'tray.two_helpers' "helpers=$($h.Count) with_window=$((HelpersWithWindow $p2.Id).Count) uids=$uids" }
    if ($env:GITHUB_STEP_SUMMARY) {
        Add-Content -LiteralPath $env:GITHUB_STEP_SUMMARY "Two tray helpers on $($env:RUNNER_OS) ($((Get-CimInstance Win32_OperatingSystem).Caption)): $($h.Count) helper processes, $((HelpersWithWindow $p2.Id).Count) with a SystrayClass window, uIDs $uids."
    }
    # ending the agent ends its helpers (their standard input closes)
    Stop-Process -Id $p2.Id -Force -ErrorAction SilentlyContinue
    $gone = WaitFor 10 { (HelperProcs).Count -eq 0 }
    if ($gone) { Pass 'tray.helpers_follow_main' 'no helper left' } else { Fail 'tray.helpers_follow_main' "helpers left: $((HelperProcs).Count)" }
    $script:agentPid = $null
}

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
    $rv = (Get-ItemProperty -Path $runKey -Name 'AIUsage' -ErrorAction SilentlyContinue).AIUsage
    $problems = @()
    if ($r.code -ne 0) { $problems += "code=$($r.code) timeout=$($r.timeout)" }
    if ($rv) { $problems += 'run_value_left' }
    if (Test-Path -Path $entryKey) { $problems += 'entry_left' }
    if (Test-Path -LiteralPath $installDir) { $problems += 'install_dir_left' }
    if (Test-Path -LiteralPath $data) { $problems += 'data_left' }
    if ((AllProcs).Count -ne 0) { $problems += "processes=$((AllProcs).Count)" }
    $ev = (DevStat $script:device).uninstall_events
    if ($ev -ne 1) { $problems += "uninstall_events=$ev" }
    if ($problems.Count -eq 0) { Pass 'uninstall.via_entry' 'everything removed' } else { Fail 'uninstall.via_entry' ($problems -join ' ') }
}

# A release-style build (CI_REL_VERSION, such as 0.8.99-relcheck: no CI-build switch, no
# refusing transport) never starts the Codex CLI (ruling F1). Every
# Anthropic and OpenAI host name is mapped to this runner first and
# answered by fakevendor, whose made-up CA the machine trusts, so nothing
# can reach the real hosts; the check stops before the install if any
# name is not local. A fakecodex that records every run is first on PATH
# (install's and the agent's), is the codex_path install records, and is
# named by the CI build's fallback switch. chatgpt.com answers 403 for
# three fetch rounds, then 200 with both limit fields null for three
# more: codex must never have run, and every Codex result the server got
# must be the direct request's own answer.
$vendorNames = @('chatgpt.com', 'chat.openai.com', 'api.openai.com', 'auth.openai.com', 'api.anthropic.com', 'console.anthropic.com', 'claude.ai')
$hostsFile = Join-Path $env:SystemRoot 'System32\drivers\etc\hosts'
$vendorTag = '# ai-usage CI fake vendors'
$script:vendorProc = $null
$script:server2 = $null
$script:caThumb = $null
$script:hostsBefore = $null
# ReleaseCleanup undoes what the release check changed, each part once (in
# a finally block, so an interrupted run undoes it too). The hosts file
# gets its bytes back exactly.
function ReleaseCleanup {
    if ($script:vendorProc) { Stop-Process -Id $script:vendorProc.Id -Force -ErrorAction SilentlyContinue; $script:vendorProc = $null }
    if ($script:server2) { Stop-Process -Id $script:server2.Id -Force -ErrorAction SilentlyContinue; $script:server2 = $null }
    if ($null -ne $script:hostsBefore) {
        try { [IO.File]::WriteAllBytes($hostsFile, $script:hostsBefore); $script:hostsBefore = $null } catch { Write-Output "::error title=release cleanup::hosts file not restored: $($_.Exception.Message)" }
    }
    if ($script:caThumb) { Remove-Item -LiteralPath "Cert:\LocalMachine\Root\$($script:caThumb)" -ErrorAction SilentlyContinue; $script:caThumb = $null }
    Remove-Item Env:AI_USAGE_TEST_CODEX_FALLBACK -ErrorAction SilentlyContinue
}
if ($Desktop) {
  try {
    Check 'release.no_codex_cli' {
        $problems = @()
        $rel = $env:CI_REL
        if (-not $rel -or -not (Test-Path -LiteralPath (Join-Path $rel 'ai-usage-windows-amd64.exe'))) { Fail 'release.no_codex_cli' 'no release-style build in CI_REL'; return }
        # 1. the vendors' names are this runner's, before anything could ask them
        $script:hostsBefore = [IO.File]::ReadAllBytes($hostsFile)
        Add-Content -LiteralPath $hostsFile -Value ("`r`n127.0.0.1 " + ($vendorNames -join ' ') + " $vendorTag") -Encoding ascii
        $null = & ipconfig.exe /flushdns
        foreach ($h in $vendorNames) {
            $a = @(); try { $a = @([System.Net.Dns]::GetHostAddresses($h) | ForEach-Object { $_.IPAddressToString } | Sort-Object -Unique) } catch { }
            if (($a -join ',') -ne '127.0.0.1') { $problems += "$h=[$($a -join ',')]" }
        }
        if ($problems.Count -gt 0) { Fail 'release.no_codex_cli' "vendor names not mapped to this runner: $($problems -join ' ')"; ReleaseCleanup; return }
        # 2. the fake vendors on 127.0.0.1:443, their CA trusted by the machine
        $ca = Join-Path $out 'fakevendor-ca.pem'
        $admin = 'http://127.0.0.1:18799'
        $script:vendorProc = Start-Process -FilePath (Join-Path $env:CI_BIN 'fakevendor.exe') `
            -ArgumentList @('-listen', '127.0.0.1:443', '-admin', '127.0.0.1:18799', '-ca', $ca, '-mode', '403') `
            -RedirectStandardOutput (Join-Path $out 'fakevendor-out.log') -RedirectStandardError (Join-Path $out 'fakevendor.log') -WindowStyle Hidden -PassThru
        if (-not (WaitFor 20 { try { $null = Invoke-RestMethod -TimeoutSec 5 "$admin/healthz"; $true } catch { $false } })) {
            Fail 'release.no_codex_cli' "fakevendor did not start: $(Get-Content -Raw (Join-Path $out 'fakevendor.log') -ErrorAction SilentlyContinue)"; ReleaseCleanup; return
        }
        $b64 = ((Get-Content -Raw -LiteralPath $ca) -replace '-----[A-Z ]+-----', '') -replace '\s', ''
        $der = [Convert]::FromBase64String($b64)
        $cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2 -ArgumentList (, $der)
        $store = New-Object System.Security.Cryptography.X509Certificates.X509Store -ArgumentList 'Root', 'LocalMachine'
        $store.Open('ReadWrite'); $store.Add($cert); $store.Close()
        $script:caThumb = $cert.Thumbprint
        $code = 0
        try { $code = [int](Invoke-WebRequest -UseBasicParsing -SkipHttpErrorCheck -TimeoutSec 10 -Uri 'https://chatgpt.com/backend-api/wham/usage').StatusCode } catch { $code = "error: $($_.Exception.Message)" }
        if ("$code" -ne '403') { Fail 'release.no_codex_cli' "chatgpt.com is not the fake vendor (HTTP $code)"; ReleaseCleanup; return }
        # 3. the fakecodex, first on PATH for install and the agent it starts
        $relbin = Join-Path $env:RUNNER_TEMP 'relbin'
        New-Item -ItemType Directory -Force -Path $relbin | Out-Null
        $fake = Join-Path $relbin 'codex.exe'
        Copy-Item -LiteralPath (Join-Path $env:CI_BIN 'fakecodex.exe') -Destination $fake
        $marker = Join-Path $out 'codex-ran.txt'
        [IO.File]::WriteAllText("$fake.args", "-record`n$marker`n")
        # 4. a fake server with the release-style files that asks for Codex at every sync
        $base2 = 'http://127.0.0.1:18702'
        $script:server2 = Start-Process -FilePath (Join-Path $env:CI_BIN 'fakeserver.exe') `
            -ArgumentList @('-listen', '127.0.0.1:18702', '-key', $key, '-token', "$token,$codexSig,$codexRefresh", '-files', $rel, '-scripts', $env:CI_SCRIPTS, '-codex-poller') `
            -RedirectStandardOutput (Join-Path $out 'fakeserver2-out.log') -RedirectStandardError (Join-Path $out 'fakeserver2.log') -WindowStyle Hidden -PassThru
        if (-not (WaitFor 30 { try { $null = Invoke-RestMethod -TimeoutSec 5 "$base2/healthz"; $true } catch { $false } })) { Fail 'release.no_codex_cli' 'the second fake server did not start'; ReleaseCleanup; return }
        $savedPath = $env:Path
        $env:Path = "$relbin;$savedPath"
        $env:AI_USAGE_TEST_CODEX_FALLBACK = $fake
        try { $r = OneLiner 'install-release.log' $base2 } finally { $env:Path = $savedPath; Remove-Item Env:AI_USAGE_TEST_CODEX_FALLBACK -ErrorAction SilentlyContinue }
        if ($r.code -ne 0) { $problems += "install_code=$($r.code)" }
        function S2 { try { Invoke-RestMethod -TimeoutSec 10 "$base2/stats" } catch { $null } }
        function Results { $s = S2; $d = DeviceId; if ($s -and $d -and $s.devices.$d) { $s.devices.$d.codex_results } else { $null } }
        function Rounds([string]$Kind, [int]$N) { $c = Results; $c -and $c.$Kind -ge $N }
        if (-not (WaitFor 300 { Rounds 'wham 403' 3 })) { $problems += "rounds_403=[$(ConvertTo-Json -Compress -InputObject (Results))]" }
        try { $null = Invoke-WebRequest -UseBasicParsing -Method Post -TimeoutSec 10 -Uri "$admin/mode?m=nulls" } catch { $problems += 'mode_failed' }
        if (-not (WaitFor 300 { Rounds 'wham 200' 3 })) { $problems += "rounds_200=[$(ConvertTo-Json -Compress -InputObject (Results))]" }
        # 5. what happened
        if (Test-Path -LiteralPath $marker) { $problems += "codex_was_run=[$((Get-Content -Raw -LiteralPath $marker) -replace '\s+', ' ')]" }
        $res = Results
        $kinds = @(); if ($res) { $kinds = @($res.PSObject.Properties.Name) }
        if ($kinds.Count -eq 0 -or @($kinds | Where-Object { $_ -ne 'wham 403' -and $_ -ne 'wham 200' }).Count -gt 0) { $problems += "results=[$(ConvertTo-Json -Compress -InputObject $res)]" }
        $cfg = $null; try { $cfg = Get-Content -Raw -LiteralPath (Join-Path $data 'config.json') | ConvertFrom-Json } catch { }
        if (-not $cfg -or "$($cfg.codex_path)" -ine $fake) { $problems += "codex_path=[$($cfg.codex_path)]" }
        $d2 = (S2).devices.(DeviceId)
        if (-not $env:CI_REL_VERSION -or $d2.last_version -ne $env:CI_REL_VERSION) { $problems += "version=$($d2.last_version) want=$($env:CI_REL_VERSION)" }
        $log = ReadShared (Join-Path $data 'agent.log')
        if ($log.Contains('CI build')) { $problems += 'behaves_as_ci_build' }
        if ((S2).token_leaks -ne 0) { $problems += "token_leaks=$((S2).token_leaks)" }
        try { [IO.File]::WriteAllText((Join-Path $out 'fakevendor-stats.json'), (Invoke-WebRequest -UseBasicParsing -TimeoutSec 10 "$admin/stats").Content) } catch { }
        [IO.File]::WriteAllText((Join-Path $out 'agent-release.log'), $log)
        $u = RunExe $exe 'uninstall' 120
        if ($u.code -ne 0) { $problems += "uninstall_code=$($u.code)" }
        $null = WaitFor 15 { (AllProcs).Count -eq 0 }
        if ((AllProcs).Count -ne 0) { $problems += "processes=$((AllProcs).Count)" }
        ReleaseCleanup
        if ($problems.Count -eq 0) { Pass 'release.no_codex_cli' "$($env:CI_REL_VERSION): Codex results $(ConvertTo-Json -Compress -InputObject $res), all direct; fakecodex (PATH, codex_path, switch) never run" }
        else { Fail 'release.no_codex_cli' ($problems -join ' ') }
    }
  } finally {
    ReleaseCleanup
  }
}

$failed = ''
if ($script:failedIds.Count -gt 0) { $failed = " failed: $($script:failedIds -join ' ')" }
Write-Output "::notice title=summary::$($script:passed)/$($script:required)$failed"
if ($script:passed -ne $script:required) { exit 1 }
exit 0
