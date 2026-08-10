# CLI experience

The CLI is the fastest way to inspect or control a bridge host. It uses the
same API and command engine as the dashboard and Telegram bot, so a command has
the same authorization and audit behavior regardless of where it came from.

## Setup wizard

Run:

```sh
local-device-bridge setup
```

The wizard intentionally reveals one decision at a time. In a terminal, use
`↑`/`↓` and `Enter` (or `j`/`k`) to choose. Options use circles while browsing
and finish with a checkmark; the navigation arrow is not left behind. If a
terminal is narrow, the banner switches to a single-line title and menu rows
are shortened instead of wrapping. If a terminal does not support raw keyboard
input, numbered menus are used automatically.

The setup order is:

1. Choose **CLI + dashboard** or **CLI only**.
2. If the dashboard is enabled, choose whether it opens automatically, whether
   its link is shown, and whether access is localhost-only or available to a
   phone on the same trusted Wi-Fi.
3. Select **TVs & displays**, **Game consoles**, and/or **Computers**. The computer group is limited to macOS, identified Windows computers, and Raspberry Pi. Consoles are shown separately and support status plus Wake-on-LAN when a MAC is known; phones, speakers, generic Linux hosts, and anonymous IP devices are not shown.
4. Answer **Yes — configure chat options** or **No — skip chat setup**. The Yes path then lets you select Telegram and configure it.
5. Review and save.
6. If phone access is enabled, choose whether to start the phone dashboard now.
   The Yes option prints a clickable link, QR code, and phone access instructions; the No
   option leaves a later `local-device-bridge dashboard phone` command.

For most people, **CLI + dashboard**, automatic opening, localhost-only access,
and both focused product groups are the best starting choices. Enable the
phone link only when the bridge and phone are on the same non-guest network.

The console option does not install a console account or cloud connector. It
adds a **Game consoles** inventory section and the safe commands `status` and
`on` (Wake-on-LAN when a MAC is known). Use `local-device-bridge agent guide
<console>` for the platform-specific network-wake steps.

After setup, running `local-device-bridge` with no command opens the interactive
CLI home. It shows separate Host and Phone dashboard links, the current
inventory using the saved product-group preference, prints the
most useful commands, and offers refresh, network scan, dashboard, command
reference, and setup actions. Use `local-device-bridge cli` for the same screen
explicitly.

### Chat connections

The wizard first asks whether chat options should be configured. If you choose
Yes, it shows a multi-select chat-services menu. Telegram is the first
available connector and supports multiple allowed user or chat IDs. After
selecting it, the wizard asks for, in order:

1. The bot token, hidden while typed and stored in the OS keychain.
2. Allowed user/chat IDs, entered as a comma-separated list.
3. Private allowlist or explicitly confirmed public commands.

Private allowlist is the default and should be used for a home bridge. Public
commands are intentionally marked risky because anyone who discovers the bot
could control the bridge.

Future connector ideas, not implemented or enabled yet, are Discord, Slack,
Matrix, Signal, WhatsApp Business, and an Apple Messages bridge. Each would
need its own authentication, privacy, and outbound-only security review before
being added to the selector.

The wizard does not ask for a TV IP address. Discovery supplies the current
address, while Samsung credentials are kept in the OS keychain. Pairing is a
separate action because the TV must display and accept its own permission
prompt:

```sh
local-device-bridge discover
local-device-bridge pair <device-id>
```

The pair command never accepts a Mac username, IP address, SSH option, or
shell command on the command line. For a remote Mac it first resolves the
device, then asks privately for the target Mac's short login name so the value
does not enter shell history. The name is the account name used by Remote
Login, not the Mac's friendly name, Apple ID, or email address. The dashboard
uses the same field. Before pairing, enable **System Settings → General →
Sharing → Remote Login** and configure normal SSH key access yourself. The
bridge only checks existing status access; it never creates keys, edits
`authorized_keys`, writes `sudoers`, stores a Mac password, or runs arbitrary
shell commands.

The bridge host is intentionally status-only. Remote Mac Wake uses
Wake-on-LAN. Remote Mac sleep needs a separate administrator policy on the
target Mac and will return a clear authorization error when that policy is not
present.

## Terminal remote

For a supported TV with navigation capabilities:

```sh
local-device-bridge remote <device-id>
```

Controls are immediate: arrow keys navigate, Enter selects, `+`/`-` adjust
volume, `M` toggles mute, Space or `P` sends play/pause, `H` is Home, `B` is
Back, `S` is Source, `W` wakes, `O` powers off, and `Q` exits. The same remote
layout is translated by each adapter; Samsung still needs pairing, while Roku
needs **Control by mobile apps** enabled and has no bridge pairing prompt. Use
the `device` subcommands for scripts and automation.

Useful non-interactive forms:

```sh
local-device-bridge device <id> status
local-device-bridge device <id> volume +3
local-device-bridge device <id> volume -2
local-device-bridge device <id> volume 20
local-device-bridge device <id> key HOME
```

Device IDs are stable provider identifiers, but they do not need to be shown
to users. Save a unique friendly name once, then use it everywhere:

```sh
local-device-bridge devices rename <id-or-name> "Living Room TV"
local-device-bridge pair "Living Room TV"
local-device-bridge device "Living Room TV" status
local-device-bridge device "Living Room TV" on
local-device-bridge device "Living Room TV" volume +3
```

The same name can be used by Telegram and the agent API. If two devices have
the same discovered name, the bridge rejects the ambiguous reference and asks
the caller to use the stable ID or choose a unique alias.

`volume +3` and `volume -2` mean relative steps. An unsigned value requests
an absolute level only where the adapter supports it.

For phone dashboard sign-in, do not use the agent or Telegram token. On the bridge
computer run:

```sh
local-device-bridge dashboard phone   # prints the one-time phone URL and QR
local-device-bridge dashboard token   # shows the dashboard token later
```

`dashboard phone` renders a QR code and a clickable one-time pairing link in a
real terminal. Scan it with the phone Camera app or open the link on the phone.
The browser signs in automatically, removes the one-time credential from the
address bar, and opens the dashboard without asking for a token. The link
expires after 10 minutes and works once; run the command again for a new one.
If the QR cannot be used, the dashboard token remains available as a manual
fallback with `local-device-bridge dashboard token`.

The dashboard token is a reusable manual fallback generated for this
installation and stored locally by the bridge. The separate `agent token` is
for Hermes or another authorized API client and is not accepted by the browser
sign-in form. The QR pairing link is intentionally different from both tokens.

## Agent integration

The running bridge can generate its host-specific machine contract:

```sh
local-device-bridge agent token
local-device-bridge agent manifest
local-device-bridge agent openapi
local-device-bridge agent guide <device-id>
```

An authorized agent should follow this order: `GET /devices`, `GET
/devices/{reference}/guide`, follow the device-specific instructions, `POST
/devices/{reference}/pair`, ask the user to accept the on-device prompt, wait
for `paired=true`, and only then call `/commands`. It can then save a friendly
name with `POST /devices/{reference}/name` and use that alias for later
commands. The dashboard's **Agent API** page shows these steps and the full
generated endpoint list.

To give an AI agent access, keep the bridge on the trusted LAN, run
`local-device-bridge agent token` on the bridge computer, and paste that value
into the agent's private environment or connector settings. Tell the agent:
“Use the local-device-bridge API at the bridge URL, authenticate with the
provided bearer token, read `/api/v1/agent/manifest` and the selected device
guide first, ask me before pairing or power commands, and use a saved device
alias for later commands.” Never put the token in a repository, public prompt,
Telegram chat, or QR code.

An agent should read the manifest, read the selected device guide, refresh
discovery when an address may have changed, and execute only actions listed in
that device's capabilities. The bearer token is local and private; never put it
in a repository, public prompt, or public URL.

## LAN dashboard access

By default, trusted-LAN phone access uses token-protected HTTP, so a phone does
not need to trust a private certificate. `dashboard open` opens the Mac-local
dashboard. `dashboard phone` prints the LAN URL for a phone; it does not open
that phone-only URL in the Mac browser. Scan the QR printed by `dashboard phone`
instead of typing the URL. If HTTPS was selected during setup, the browser may
show a one-time warning because the certificate is not issued by a public
certificate authority. On a trusted home LAN, use the browser's **Advanced →
Proceed** option, or run:

```sh
local-device-bridge dashboard trust
```

That command prints an explicit platform-specific trust command. The bridge
does not silently modify the operating system's trust store.

## Visual behavior

Colors and short spinners are enabled only when the output is an interactive
terminal. Piped output stays plain and stable; this makes commands safe to use
from launchd, systemd, CI, logs, and shell scripts. Set `NO_COLOR=1` to force
plain output even in a terminal.

The visual layer has no runtime package installation. It is ANSI escape
sequences and terminal handling compiled into the Go binary.
