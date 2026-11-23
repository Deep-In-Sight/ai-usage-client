# Claude Usage Tray Indicator

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

## Requirements

- Ubuntu 24.04 with the default GNOME desktop (GNOME Shell 46). Developed and
  tested on an X11 session.
- [Claude Code](https://claude.com/claude-code) installed on the same machine
  and signed in with a claude.ai subscription. The indicator has no login of
  its own; it uses the one Claude Code keeps.

Nothing else needs installing: the libraries it uses ship with GNOME Shell.

## Install

```sh
git clone https://github.com/Deep-In-Sight/claude-usage-tray-indicator.git
cd claude-usage-tray-indicator
./install.sh
```

Then restart GNOME Shell once so it loads the extension:

- **X11:** press Alt+F2, type `r`, press Enter. Your windows stay open.
- **Wayland:** log out and back in.

After that it starts by itself at every login.

The Claude Code robot icon in the screenshot is Anthropic's artwork, so it is
not included in this repository. `install.sh` copies it from the Claude Code
extension for VS Code (or Cursor) if you have that installed. Without it the
indicator works the same, just without the icon.

## Using it

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

## How it works

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

## Limitations

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

## Development

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
