# Claude Usage Tray Indicator

This repository holds two ways to keep an eye on your Claude plan usage:

| | Runs on | What it does |
| --- | --- | --- |
| [Claude Usage agent](#claude-usage-agent) | Windows, macOS, Ubuntu | A tray icon with your weekly usage. It also reports the numbers to your team's Claude Usage server, and you install it from that server. |
| [GNOME Shell extension](#gnome-shell-extension) | Ubuntu 24.04 (GNOME 46) | The original: a ring and a percentage in the top bar, for you alone. |

Use one of them on a PC, not both. Each asks Anthropic for the numbers on its
own, so together they make twice the requests.

The extension, at a glance (its details are [further down](#gnome-shell-extension)):

A GNOME Shell extension that shows your Claude plan usage in the Ubuntu top
bar, so you don't have to keep opening the usage page on claude.ai.

![The indicator in the top bar with its card open](docs/screenshot.png)

- **Top bar:** a ring gauge and your weekly usage across all models, as a
  percentage.
- **Click:** a card with two bars, weekly usage for all models and for Fable,
  each with its reset time.
- It always shows the last numbers it fetched successfully. It refreshes every
  5 minutes in the background and again the moment you open the card.

> Unofficial. Not affiliated with or endorsed by Anthropic. It relies on an
> undocumented endpoint, which may change or stop working without notice.

## Claude Usage agent

A small program that runs for your OS user. It shows your weekly usage in the
tray (the Windows notification area, the macOS menu bar, the Ubuntu top bar)
and sends it to your team's Claude Usage server, whose dashboard shows the
whole team. Only one PC per Claude account asks Anthropic at a time: the
server picks it, and every other PC on that account gets the numbers from the
server.

### What it sends

To your team's server, and nowhere else. (The server also sees this PC's IP
address, as any server you connect to does.)

- Anthropic's whole usage reply for each fetch, with when it was fetched and
  its HTTP status: the percentage of each limit, the reset times, the
  per-product breakdown and the extra-usage spend
- The account: its ID, email, display name, organization name and type,
  plan and rate-limit tier
- This PC: a random ID made at the first run, the PC name, your OS user
  name, the OS and its version, and the CPU type
- The agent: its version, and whether it shows a tray icon
- The sign-in: whether Claude Code is signed in, and when the sign-in expires
- Whether you asked for fresh numbers with **Refresh now**

Field by field, every sync request carries `schema`, `client.version`,
`client.os`, `client.os_version`, `client.arch`, `client.mode`, `device.id`,
`device.hostname`, `device.os_user`, `account.uuid`, `account.email`,
`account.display_name`, `account.organization_name`,
`account.organization_type`, `account.subscription_type`,
`account.rate_limit_tier`, `auth.state`, `auth.token_expires_at`,
`results[].account_uuid`, `results[].fetched_at`, `results[].http_status`,
`results[].usage` and `want_fresh`. Nothing else.

### What it never sends

Your token, your conversations, your files.

- It reads the sign-in Claude Code keeps and never refreshes or changes it.
  The token goes only to `api.anthropic.com`, and redirects are refused.
  Normally only the one PC the server picks for an account uses it; while
  the server cannot be reached, each PC asks Anthropic on its own, every 30
  minutes.
- It never listens on a port. Every connection goes out, to your server over
  HTTP and to Anthropic over HTTPS, so there is no firewall prompt.

### Install

Open your team's server (the link your team shared, such as
`http://claude-usage.local`) and copy the command shown there for your OS:

```sh
# Windows, in PowerShell
irm http://claude-usage.local/install.ps1 | iex

# macOS, in Terminal
curl -fsSL http://claude-usage.local/install.sh | sh

# Ubuntu or another Linux, in a terminal
wget -qO- http://claude-usage.local/install.sh | sh
```

Use the command your server's page shows; it may use another address. The
files on this repository's Releases page do not install on their own: the
server's command writes the settings the agent needs (the server's address and
its key).

You need Claude Code signed in with your claude.ai subscription, now or
later; the agent starts reporting once you sign in. On a Mac, macOS may ask
whether `claude-usage` may use the "Claude Code-credentials" keychain item;
choose **Always Allow**. The agent is not code-signed, so macOS may ask again
after an upgrade. It installs for the OS
user who runs the command only, and needs no administrator rights.

| OS | Installed in | Starts at login through |
| --- | --- | --- |
| Windows | `%LOCALAPPDATA%\Programs\ClaudeUsage\`, with an entry in Settings › Apps › Installed apps | `HKCU\Software\Microsoft\Windows\CurrentVersion\Run` |
| macOS | `~/Library/Application Support/ClaudeUsage/` | `~/Library/LaunchAgents/io.github.deep-in-sight.claude-usage.plist` |
| Linux with a desktop | `~/.local/bin/claude-usage` | `~/.config/autostart/claude-usage.desktop` |
| Linux without a desktop | `~/.local/bin/claude-usage` | the systemd user service `claude-usage.service`, plus linger so it runs without a login. If linger is refused, the installer says so and the agent runs only while you are logged in. |

Run the same command again to upgrade. The PC stays the same device on the
dashboard and nothing waiting to be sent is lost.

### Using it

The menu, from top to bottom:

| Item | What it is |
| --- | --- |
| New version available: 1.1.0 | Shown when your server has a newer agent. Opens the server's install page. |
| All models  71%  Resets Sun 01:00 · in 1d 6h | Weekly usage across all models, and when it resets. |
| Fable  76%  Resets Sun 01:00 · in 1d 6h | The weekly limit for Fable. |
| Claude Code sign-in needed | Claude Code is not signed in on this PC. "Sign-in expired: open Claude Code to renew it" means the sign-in ran out. |
| Account team-a@…  ·  3 PCs on this account | The signed-in account, and how many PCs report for it. |
| Server connected  ·  Updated 18:58  ·  poller TEST-PC-03 | When the numbers were fetched, and which PC fetches them (`this PC fetches` when it is this one). |
| Server not reachable  ·  this PC fetches every 30 min | The server cannot be reached; this PC asks Anthropic itself, every 30 minutes, until it can. |
| Open dashboard | Opens your server's dashboard. |
| Refresh now | Asks the server for fresh numbers. |
| Start at login | Turns starting at login on or off. |
| Remove Claude Usage | Removes the agent. Click it twice within 10 seconds. |
| Quit | Stops the agent until the next login. |

| You see | It means |
| --- | --- |
| Terracotta ring | Normal. |
| Yellow | 75% or more, or Anthropic flags the limit as being used up fast. |
| Red | 90% or more. |
| `⚠` after the percentage, or a faded ring | The numbers are more than 15 minutes old. |
| `–%` | No numbers yet, or Claude Code sign-in needed. |

- **Windows:** the icon draws the percentage inside the ring. New icons go
  under the `^` arrow on the taskbar; drag it onto the taskbar to keep it in
  view.
- **macOS:** the ring and the percentage in the menu bar.
- **Ubuntu:** the ring with the percentage inside it, in the top bar (through
  the AppIndicator extension Ubuntu has on by default).
- Without a tray, such as a server with no desktop, it runs without an icon.
  Ask it what it is doing with `~/.local/bin/claude-usage status`.

### Remove

- From the tray: **Remove Claude Usage**, clicked twice.
- Windows: Settings › Apps › Installed apps › Claude Usage › Uninstall, or in
  PowerShell:
  `Start-Process -Wait -NoNewWindow -FilePath "$env:LOCALAPPDATA\Programs\ClaudeUsage\claude-usage.exe" -ArgumentList uninstall`
- macOS: `"$HOME/Library/Application Support/ClaudeUsage/claude-usage" uninstall`
- Linux: `~/.local/bin/claude-usage uninstall`

Removing takes the program, its settings and its start-at-login entry away.
Linger, if the installer turned it on, is left as it is. Windows cannot
delete a program while it runs, so removing it from the tray or from
Installed apps moves `claude-usage.exe` to your `TEMP` folder; the next
install deletes it there, or you can delete it yourself.

### Security notes

- The executables are not code-signed. Downloaded with a browser, they meet
  SmartScreen ("Windows protected your PC"), Edge's download warning or
  Gatekeeper. Installed with the command above, they meet none of these.
- Windows 11 with Smart App Control in enforcement mode is not supported yet.
- Every release carries `SHA256SUMS`, and the install commands check the
  file they download against it.
- All the source is in this repository, under `agent/`.

### Building and releases

- `agent/tools/build.sh [VERSION]` builds the Linux and Windows files into
  `agent/dist/` (with Go if it is installed, else in Docker).
  `agent/tools/build-macos.sh VERSION` builds the macOS file on a Mac.
- CI builds all three files and `SHA256SUMS` on every push, and shows what a
  release would publish.
- Releases are made only from a `v*` tag on a commit of `main`. The release
  workflow attaches the three files and `SHA256SUMS`.

## GNOME Shell extension

### Requirements

- Ubuntu 24.04 with the default GNOME desktop (GNOME Shell 46). Developed and
  tested on an X11 session.
- [Claude Code](https://claude.com/claude-code) installed on the same machine
  and signed in with a claude.ai subscription. The indicator has no login of
  its own; it uses the one Claude Code keeps.

Nothing else needs installing: the libraries it uses ship with GNOME Shell.

### Install

```sh
git clone https://github.com/Deep-In-Sight/ai-usage-client.git
cd ai-usage-client
./install.sh
```

Then restart GNOME Shell once so it loads the extension:

- **X11:** press Alt+F2, type `r`, press Enter. Your windows stay open.
- **Wayland:** log out and back in.

After that it starts by itself at every login.

### Using it

| To do this | Do this |
| --- | --- |
| Turn it off | Click the indicator, then **Quit**. It stays off until you turn it on again. |
| Turn it on | `gnome-extensions enable claude-usage@localhost`, or switch on "Claude Usage" in the Extensions app. |
| Update | `git pull`, run `./install.sh`, restart GNOME Shell. |
| Remove | `./install.sh --uninstall` |

What the display tells you:

| You see | It means |
| --- | --- |
| Terracotta ring or bar | Normal. |
| Yellow | 75% or more, or Anthropic flags the limit as being used up fast. |
| Red | 90% or more. |
| `⚠` next to the percentage | The number is more than 15 minutes old. |
| A grey note at the bottom of the card | Why the latest query failed. The numbers above are the last good ones. |
| `–%` and "no data" | Nothing has ever been fetched, usually because Claude Code is not signed in. |

### How it works

- It reads the access token Claude Code stores in
  `~/.claude/.credentials.json` and calls
  `https://api.anthropic.com/api/oauth/usage`, the endpoint behind Claude
  Code's own `/usage` command. One reply carries both limits.
- The token is only ever read. The indicator never refreshes it or writes to
  that file, sends it nowhere but `api.anthropic.com`, and refuses redirects.
- The endpoint is rate limited: we measured roughly 30 accepted requests per
  rolling hour per account, an allowance shared with Claude Code itself.
  Polling once a minute got about every second request refused, so the
  background refresh is 5 minutes, the same pace Claude Code uses. Opening
  the card fetches straight away unless a query ran in the last 10 seconds.
  When a request is refused it waits 2, 4, 8, then 10 minutes.
- The last successful reply is saved in `~/.cache/claude-usage/last.json`,
  which is why numbers are there right after a login.

### Limitations

- **GNOME Shell 46 only.** Other versions refuse to load it. Newer ones need
  at least the version list in `metadata.json` extended, and possibly small
  code changes. Ubuntu 22.04 (GNOME 42) uses an older extension format and
  will not work.
- **The sign-in can expire.** Claude Code's token lasts about 8 hours and is
  renewed only when Claude Code runs. Until then the indicator keeps the last
  numbers and says "Sign-in expired".
- **The second bar is Fable.** To watch another model's weekly limit, change
  `SCOPED_MODEL` in `claude-usage@localhost/usage.js`.
- Not tested on Wayland, with a light desktop theme, or with an account that
  signs in to Claude Code with an API key instead of a subscription.

### Development

```sh
gjs -m tests/test_usage.js                # unit tests for parsing and text
dev/preview_shell.sh /tmp/preview.png     # render it in a throwaway shell
```

`dev/preview_shell.sh` starts a headless GNOME Shell with its own D-Bus
session, settings and runtime directory, loads the extension from this
checkout, opens the card and saves a screenshot. Your running desktop is not
touched, which matters because a shell extension's code is only reloaded by
restarting the shell. Its header documents a few environment variables, for
example to preview the signed-out state.

Go easy on the endpoint while testing: each preview run with your real home
directory makes a request, and a burst of them can lock the account out of
the endpoint for about half an hour. `dev/probe_usage.py` makes one request
and prints the status and limits.

`dev/xinput_fake.py` is a small stand-in for `xdotool` (clicks, key presses
and an idle check on X11), used to click through the real indicator.

## Credits

The robot icon (`claude-usage@localhost/clawd.svg`) is Clawd, the Claude Code
mascot. It is artwork by Anthropic, © Anthropic PBC, taken from the Claude
Code extension for VS Code and used here only to identify the product this
indicator reports on. All rights to it remain with Anthropic. "Claude" and
"Claude Code" are Anthropic's names; this project is independent of Anthropic.

The agent uses Clawd as its application icon.
