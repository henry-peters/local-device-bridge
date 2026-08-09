# local-device-bridge

`local-device-bridge` is a self-hosted home-Wi-Fi control center for TVs,
displays, macOS, Windows, and Raspberry Pis. Run one bridge host on the trusted
local network, then use the responsive dashboard from the host computer, phone,
iPhone/iPad, or Mac. A CLI, optional Telegram bot, and authenticated API for
AI-agent systems such as Hermes use the same command engine.

The bridge is designed for local-network use: discovery, pairing, commands, and
agent traffic stay on the home LAN unless you explicitly expose a service.
Supported TVs receive a full remote dashboard; computers expose only the safe
power/status actions provided by their adapter. Capabilities are reported per
device so the project can add more TV brands without pretending that every model
uses the same pairing protocol.

## How it works

1. Install the single binary on a computer that stays on the same trusted Wi-Fi
   as the devices.
2. Run discovery. Devices must be powered on and advertising a recognizable
   service at least once before the bridge can remember them while offline.
3. Open a device guide. TV pairing is completed on the TV itself; for example,
   Samsung shows a permission prompt that the user must accept.
4. Control supported TVs with the phone/tablet/Mac remote, CLI, Telegram, or a
   connected AI agent. Computer adapters remain limited to wake, sleep, and
   status where supported.

For AI-agent access, run `local-device-bridge agent token` on the bridge host and
place that separate token in the agent's private connector settings. The agent
must read the generated manifest and device guide before pairing or issuing a
power command. See [Agent integration](docs/agent-integration.md).

## What works today

| Device | Discovery | Control/access |
| --- | --- | --- |
| Samsung Smart TV | repeated SSDP, UPnP metadata, and direct `/api/v2/` probing | on-screen pairing, remote keys, volume, mute, playback, source/channel, power-off, and Wake-on-LAN |
| Roku TV/player | SSDP plus direct ECP identity probing | ECP remote after **Control by mobile apps** is enabled; Roku has no bridge pairing prompt |
| PlayStation / Xbox / Nintendo | SSDP/mDNS identity when advertised, plus remembered records | inventory, status, and Wake-on-LAN when a MAC is known; no universal account-backed remote or power-off API |
| macOS computer | Bonjour and local-host identity | bridge host status only; remote Macs use explicit restricted SSH setup for status, wake, and sleep |
| LG/Sony/other identified TV | SSDP/mDNS and UPnP metadata | identification and model-specific guidance only; no fake Pair button or remote |
| Windows computers / Raspberry Pi | advertised network identity, bounded Windows service probe, or Raspberry Pi identity | identified inventory only; no arbitrary shell or universal pairing claim |

Phones, tablets, anonymous IP rows, speakers, and unrelated IoT devices are not
part of the focused inventory. Game consoles are included as a separate,
limited section when enabled. They may be observed internally during discovery
but are not shown as controllable devices unless a tested capability is present.

Important: live discovery works only while a device is powered on and
advertising a recognizable protocol or answering a supported vendor endpoint.
If a device is asleep, powered off, behind client isolation, or has network
remote access disabled, it may not be discoverable at all. The bridge can show
an offline device only after it has successfully discovered and saved that
device once; a clean install cannot invent the name, identity, or address of a
device that has never been seen. Guest Wi-Fi, VLANs, and privacy settings can
also block discovery.

## Install

Published releases are self-contained. End users do not need Node.js, npm,
Python, or Go. Go 1.26+ is needed only to build from source.

macOS/Linux release install:

```sh
curl -fsSL https://raw.githubusercontent.com/henry-peters/local-device-bridge/main/install.sh | bash
```

Install this checkout from source:

```sh
git clone https://github.com/henry-peters/local-device-bridge.git
cd local-device-bridge
./install.sh --from-source
```

Windows PowerShell:

```powershell
.\install.ps1 -FromSource
```

The installer opens the setup wizard automatically. It asks, one choice at a time, for:

1. CLI-only or CLI plus dashboard.
2. Automatic browser launch and host/phone dashboard access.
3. Visible product groups: TVs & displays, Game consoles, and/or computers (macOS, Windows, and Raspberry Pi).
4. Whether to configure chat integrations. If yes, Telegram and its private allowlist are shown; if no, chat setup is skipped.
5. Confirmation before saving and starting the service.
6. If phone access is enabled, whether to start it now and print a clickable
   link, QR code, and phone access instructions.

See [installation.md](docs/installation.md) for service, upgrade, certificate,
and uninstall instructions. See [agent-integration.md](docs/agent-integration.md)
for connecting an AI agent and pairing supported devices.

Release history is documented in [CHANGELOG.md](CHANGELOG.md). The version
shown by `GET /api/v1/health` matches the published release version.

## First test

```sh
local-device-bridge              # interactive CLI home
local-device-bridge discover     # immediate network scan
local-device-bridge devices list
local-device-bridge devices rename <id-or-name> "Living Room TV"
local-device-bridge dashboard open
```

The daemon performs a quiet scan every minute. The dashboard polls the daemon
without starting duplicate scans. Devices that have been discovered once stay
in SQLite and remain visible as offline after restart or sleep. Use **Scan
network** after waking a TV or changing a network-control setting. A device
that has never advertised a supported service cannot be identified while it is
fully powered off; it must be seen once before it can be remembered offline.

For phone access, choose **This computer + my phone** during setup, then run:

```sh
local-device-bridge dashboard phone
local-device-bridge dashboard token
```

The easiest phone setup is to run `local-device-bridge dashboard phone` on the
bridge computer and scan the QR code printed in the terminal with the phone
Camera app. The QR contains a short-lived, one-use browser pairing link: it
opens the dashboard and signs that phone in automatically, so no token needs to
be typed. The printed link is also clickable in modern terminals. Keep the
phone on the same trusted Wi-Fi. If the QR cannot be used, the dashboard token
is available as a clearly labeled manual fallback and is not the Agent API or
Telegram bot token. Phone access uses token-protected HTTP by default on the
trusted home Wi-Fi, so users do not need to install or trust a certificate.
HTTPS remains available as an explicit hardened option. Never expose the
dashboard port to the public internet.

## Pairing and access

### Samsung TV

Samsung pairing requires both a reachable local API and TV approval:

1. Wake the TV. For network wake, commonly use **Settings → General → Network
   → Expert Settings → Power On with Mobile**. Newer firmware may place this
   under **Connection → Network**.
2. Open **Settings → General → External Device Manager → Device Connect
   Manager**. Enable access notifications/network remote access and remove an
   old blocked bridge entry from the device list. Labels vary by model.
3. Scan the network, open the Samsung device, select **Pair TV** once, and
   accept the on-screen request.
4. Wait for **Paired** before using the remote.

After pairing, give the device a stable operator name. The provider ID is kept
internally, but all later commands can use the friendly name:

```sh
local-device-bridge devices rename <device-id> "Living Room TV"
local-device-bridge device "Living Room TV" status
local-device-bridge device "Living Room TV" on
```

CLI equivalent:

```sh
local-device-bridge pair <samsung-device-id>
local-device-bridge unpair <samsung-device-id>
```

The saved credential is keyed by TV identity, not a hard-coded IP. A Samsung
record is offered for pairing only after the bridge verifies its live `/api/v2/`
control service; an AirPlay/mDNS name by itself is not enough. Every remote
command opens a fresh WebSocket connection, so daemon restarts and TV off/on
cycles do not reuse a stale socket. If Samsung rejects a saved credential, the
bridge clears it and requires a fresh pair instead of repeatedly opening the
approval prompt. Wake and power commands report failure unless their effect can
be confirmed; “command sent” is never treated as proof.

If pairing reports timeout, refused, or no route, the TV was not reachable from
the bridge at that moment. Verify that `http://<tv-ip>:8001/api/v2/` or the TV’s
HTTPS endpoint responds from the bridge host, then scan again. Sharing an SSID
does not prove peer-to-peer LAN access.

### Roku

Open **Settings → System → Advanced system settings → Control by mobile apps →
Enabled**. Roku uses its documented ECP service on port 8060 and does not issue
a local-device-bridge pairing prompt. Absolute volume is not portable through
ECP; use volume steps.

### macOS computer

The bridge host is status-only and never shows wake or sleep controls. A remote
Mac page generates the exact restricted setup commands. The user explicitly
enables Remote Login, installs a key, and permits only the fixed status/sleep
operations. The bridge never stores a Mac password or accepts arbitrary shell
commands.

### Game consoles

PlayStation, Xbox, and Nintendo records appear in the **Game consoles** section
when their network service is advertised. The bridge supports status and
Wake-on-LAN when discovery knows the console MAC address; it does not request
console accounts or imitate a cloud/mobile-app session. Use the official
console app for authenticated remote control and shutdown. The dashboard and
CLI deliberately label universal console power-off as unavailable.

### Identified-only device families

There is no universal “pair any device” protocol. LG webOS, Sony BRAVIA,
Windows computers, and Raspberry Pi use different
authentication systems, many of which are model-specific or not public.
Unimplemented families are labeled **Identified only** and receive guidance;
they do not receive a misleading Pair button or Samsung remote.

## CLI

```text
local-device-bridge setup
local-device-bridge daemon
local-device-bridge discover
local-device-bridge devices list
local-device-bridge devices rename <id-or-name> "Living Room TV"
local-device-bridge remote <supported-tv-id-or-name>
local-device-bridge pair <supported-device-id-or-name> [mac-user]
local-device-bridge unpair <supported-device-id-or-name>

local-device-bridge device <id-or-name> status
local-device-bridge device <id-or-name> on|off
local-device-bridge device <id-or-name> volume-up [steps]|volume-down [steps]
local-device-bridge device <id-or-name> mute|key <KEY>|source <NAME>|channel <NUMBER>

local-device-bridge dashboard open|phone|token|cert|trust
local-device-bridge agent manifest|openapi|token
local-device-bridge agent guide <device-id-or-name>
```

`remote` provides arrow-key navigation, Enter/OK, Home, Back, Play/Pause,
volume, mute, wake, and power-off only when the selected adapter supports them.
See [cli.md](docs/cli.md) for the interactive UI and safe smoke test.

## Telegram

Telegram is optional and uses outbound long polling, so it needs no router port
forwarding. Setup stores the bot token in the OS keychain and supports multiple
allowed user/chat IDs. Private allowlist mode is recommended.

```text
/devices
/scan
/tv <name-or-id>              open tap controls
/tv <name-or-id> help         device-specific commands
/remote <name-or-id>          full tap remote
/commands                     organized reference
```

Typed volume commands accept steps, for example `/tv Living Room volume up 5`.
The tap remote avoids long commands for normal phone use.

## Configuration

```json
{
  "server": {
    "bind": "127.0.0.1:8787",
    "allow_lan": false,
    "insecure_lan_http": true,
    "dashboard_origin": ""
  },
  "discovery": {
    "interfaces": [],
    "timeout": 5000000000,
    "scan_interval": 60000000000,
    "show_display_devices": true,
    "show_console_devices": true,
    "show_computer_devices": true,
    "show_offline_devices": true
  },
  "telegram": {
    "enabled": false,
    "token_env": "TELEGRAM_BOT_TOKEN",
    "allowed_ids": [],
    "allow_public": false
  }
}
```

Legacy visibility keys are read for upgrade compatibility, but mobile and
anonymous network-peer sections are no longer exposed. Dashboard Settings writes the
three current product-group switches to the same configuration used by CLI and
Telegram.

Discovery automatically uses every active directly-connected IPv4 interface.
If a computer has a VPN, Docker bridge, or multiple LANs and you want to force
one network, set `discovery.interfaces` to interface names such as `["en1"]` on
macOS or `["Wi-Fi"]` on Windows. Do not enter a TV IP address; the bridge
re-discovers current addresses on every scan.

## Local agent API

The bearer-authenticated API exposes discovery, device guides, capabilities,
commands, and audit events. An agent should read the manifest, then the selected
device guide, and execute only advertised capabilities.

```sh
local-device-bridge agent token
local-device-bridge agent manifest
local-device-bridge agent openapi
local-device-bridge agent guide <device-id>
```

The dashboard has an **Agent API** page for the same contract.

The agent workflow is deliberately ordered: list devices, read the selected
device guide, ask the user to complete any device setting, start pairing, wait
for the device prompt to be accepted and for `paired: true`, then rename and
control it. For example, an authorized client can use the returned device ID
or alias in these requests:

```text
GET  /api/v1/devices
GET  /api/v1/devices/{reference}/guide
POST /api/v1/devices/{reference}/pair
POST /api/v1/devices/{reference}/name       {"name":"Living Room TV"}
POST /api/v1/devices/Living%20Room%20TV/commands  {"action":"power_on"}
```

For Samsung, the guide tells the user to wake the TV, enable its network
remote/access setting, choose Pair TV, accept the on-screen request, and wait
for pairing to finish. The bridge refuses to present a Samsung pairing target
until its real local control service has been verified, so an agent can explain
the exact missing step instead of claiming that a request was sent.

Tokens are intentionally separate and generated per installation:

| Token | Used by | How to show it | Lifetime |
| --- | --- | --- | --- |
| Dashboard token | manual browser fallback on a phone or LAN computer | `local-device-bridge dashboard token` | unique to this installation; stable across restarts; stored in the OS keychain |
| One-time phone link | QR/clickable phone sign-in | `local-device-bridge dashboard phone` | random, expires after 10 minutes, and works once |
| Agent token | Hermes or another authorized API client | `local-device-bridge agent token` | unique to this installation; stable across restarts; stored separately in the OS keychain |
| Telegram bot token | Telegram polling | enter during setup or provide `TELEGRAM_BOT_TOKEN` | controlled by BotFather; never use it for HTTP sign-in |

`local-device-bridge dashboard phone` prints a one-time QR code and clickable
link. Scanning or opening it signs the phone in automatically, then removes the
pairing credential from the address bar. On macOS Terminal or iTerm2, use
Cmd-click. The phone must be on the same trusted Wi-Fi.

## Development

```sh
make fmt
make test
make vet
make build
make cross-build
```

Tests use fake protocol servers and do not require LAN hardware. Release builds
target macOS Intel/Apple Silicon, Linux amd64/arm64, and Windows amd64.

Read [architecture](ARCHITECTURE.md), [compatibility](docs/compatibility.md),
[security](SECURITY.md), and [contributing](CONTRIBUTING.md) before publishing a
new adapter claim.

## License

MIT. See [LICENSE](LICENSE).
