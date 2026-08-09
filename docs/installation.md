# Installation

local-device-bridge is distributed as one compiled daemon. The terminal colors,
spinners, arrow-key menus, dashboard, Samsung and Roku adapters, SQLite state
layer, and API are included in that binary. Users do not install Node.js,
Python, npm, ffmpeg, or a separate animation package.

## What must be installed?

| User | Required packages | Why |
| --- | --- | --- |
| Release user on macOS, Linux, or Windows | The release binary only | The dashboard, CLI styling, database, discovery, and adapters are embedded |
| Source builder/contributor | Go 1.26+, Git, and the target OS toolchain | Builds the single binary and runs tests |
| Dashboard user on a phone | A modern browser on the same trusted LAN | The phone is a browser remote; it does not run Go or Node.js |
| Telegram user | A Telegram bot token and configured allowlist IDs | Telegram uses outbound polling |

Node.js, npm, Python, and frontend package managers are intentionally not
runtime dependencies. The current dashboard is embedded static HTML/CSS/JS,
and terminal colors/animations are compiled Go code. Node.js can be added later
if the project deliberately migrates to a separate frontend toolchain, but
installing it today does not improve the released bridge.

The supported build targets are macOS Intel and Apple Silicon, Linux amd64 and
arm64, and Windows amd64. GoReleaser creates these artifacts from one source
tree; users do not cross-install platform packages.

## macOS and Linux

When this repository has been published and a release exists, the installer can
be run from a terminal with:

```sh
curl -fsSL https://raw.githubusercontent.com/local-device-bridge/local-device-bridge/main/install.sh | bash
```

The script detects macOS/Linux and Intel/Apple Silicon/ARM64, downloads the
matching release artifact, installs it to `~/.local/bin`, and opens the
step-by-step setup wizard. It does not change router settings. The wizard lets
you choose whether to start the daemon and open the dashboard automatically;
run it again any time to change settings.

For a checked-out repository, build locally instead:

```sh
./install.sh --from-source
```

That path requires Go 1.26+ at build time. Runtime requirements are only the
operating system, local network access, and the OS keychain when available.

On Linux, the binary does not install operating-system packages automatically.
For the fullest discovery and browser-launch experience, install the utilities
provided by your distribution: `iputils-ping` and `net-tools` for optional
ping/ARP discovery, and `xdg-utils` for opening the dashboard automatically.
`openssh-client` is needed only when pairing to a remote Mac. Package names can
vary (for example, Fedora uses `iputils`, `net-tools`, and `openssh-clients`).
Missing optional utilities do not prevent the daemon from starting; they only
disable the related discovery, browser-launch, or remote-Mac feature. Node.js,
npm, Python, and ffmpeg are not required.

Useful options:

```sh
./install.sh --help
LDB_INSTALL_DIR="$HOME/bin" ./install.sh --from-source
LDB_NO_SETUP=1 ./install.sh --from-source
```

The installer preserves the state/configuration directory. Installing a newer
binary does not erase pairings. Restart the daemon after an upgrade so the
running process uses the new adapter and UI.

## Windows

From PowerShell in a checked-out repository:

```powershell
.\install.ps1 -FromSource
```

For a published release, omit `-FromSource`. The script installs the Windows
binary under `%LOCALAPPDATA%\local-device-bridge`, adds that directory to the
user PATH, and opens setup. `install.ps1 -NoSetup` only installs the binary.

Installers are intentionally explicit about unsupported release artifacts. A
missing release is not silently replaced with an untrusted download.

## First run

The setup wizard is interactive when attached to a terminal:

1. Choose CLI-only or CLI plus dashboard.
2. Choose automatic/manual dashboard launch and whether to show its link.
3. Choose localhost-only or trusted-LAN dashboard access. If LAN access is
   enabled, choose HTTPS-only or the explicit HTTP phone compatibility link.
4. Choose the focused inventory groups: TVs & displays and computers (macOS,
   identified Windows computers, and Raspberry Pi).
5. Answer **Yes — configure chat options** or **No — skip chat setup**. Only
   the Yes path opens Telegram, its bot token, multiple allowed IDs, and the
   private/public choice.
6. Review and save.
7. If phone access is enabled, choose whether to start the phone dashboard now.
   The Yes option starts the single daemon instance and prints a clickable LAN
   link plus a one-time QR pairing link. Scanning it signs the phone in
   automatically; the dashboard token is shown only as a manual fallback with
   `local-device-bridge dashboard token`. The No option finishes in
   the terminal; run `local-device-bridge dashboard phone` later for the same
   access screen.

Use the arrow keys and Enter in a real terminal. In CI, a pipe, or a redirected
session the same wizard automatically falls back to numbered choices and plain
text so it remains scriptable. `NO_COLOR=1` disables ANSI colors.

The installer runs the setup wizard automatically and asks whether the
dashboard should open automatically or whether the installation should remain
CLI-first. Running
`local-device-bridge` with no arguments opens the interactive CLI home. It shows
the Host and Phone dashboard links separately, shows devices according to the
saved inventory preference, and provides quick actions for refresh, scanning,
opening the dashboard, and setup.

Live discovery requires the device to be powered on and advertising a
recognizable service, or answering a supported vendor endpoint. The inventory
is persistent after that first successful discovery: a supported TV, display,
Windows computer, macOS computer, or Raspberry Pi remains in the
database and is shown as offline when it later sleeps or powers off. A device
that has never been seen cannot be named or shown while fully offline; wake it
once and run a scan to create its remembered record.

If you chose CLI-only or manual dashboard launch:

```sh
local-device-bridge daemon
local-device-bridge discover
local-device-bridge devices list
```

`local-device-bridge dashboard open` opens the dashboard on the bridge
computer. `local-device-bridge dashboard phone` prints a clickable LAN link,
renders a one-time QR pairing link for the phone Camera app, and prints the
Agent API guidance. Scan the QR while the phone is on the same Wi-Fi; the
browser signs in automatically and no address or token typing is required.
The link expires after 10 minutes and works once. If the QR cannot be used,
run `local-device-bridge dashboard token` on the bridge computer for the
manual browser fallback. The daemon uses a state-directory lock, so starting `daemon` a
second time exits with a clear already-running message instead of launching a
duplicate discovery or Telegram process.

When LAN access is enabled, the dashboard URL is HTTPS with a private
self-signed certificate. A browser warning on the first visit is expected. On
the trusted home LAN, use **Advanced → Proceed**, or print explicit trust
instructions with:

```sh
local-device-bridge dashboard cert
local-device-bridge dashboard trust
```

LAN mode provides separate links: the Host link is a loopback-only HTTP
companion with no certificate warning, while the secure Phone link is HTTPS on
the LAN address. If a phone browser cannot proceed past a private certificate
warning, setup can enable HTTP compatibility mode on the main LAN port. That
mode remains token-protected but is not encrypted, so use it only on a trusted
home LAN.

After trusting the certificate on the bridge computer, fully reload or reopen
the browser. The generated certificate includes the current LAN IP and is
marked as a local trust root so the explicit trust command remains valid after
future daemon restarts. Phones and other computers must trust the certificate
separately, or use **Advanced → Proceed** on the private home network.

The installer never silently adds a certificate authority to the operating
system or exposes the dashboard to the public internet.

For a long-running installation, use the service files in
[`../deploy/README.md`](../deploy/README.md). Service supervision restarts a
crashed daemon, while the daemon itself keeps its HTTP listener alive through a
transient listener failure.

## Updating and uninstalling

Install a newer release over the existing binary, then restart the service.
Configuration, SQLite state, and keychain credentials are separate from the
binary. To remove only the executable:

```sh
rm "$HOME/.local/bin/local-device-bridge"
```

Do not remove the state directory unless you intentionally want to delete device
pairings, audit history, and configuration. The Windows executable is at
`%LOCALAPPDATA%\local-device-bridge\local-device-bridge.exe`.

## Clean reset before reinstalling

This removes the installed bridge and all saved pairings/configuration while
leaving a checked-out repository untouched. Stop the daemon first. These are
deliberately destructive commands; use them only when a fresh setup is wanted.

### macOS

```sh
launchctl bootout "gui/$(id -u)/com.local-device-bridge" 2>/dev/null || true
rm -f "$HOME/.local/bin/local-device-bridge"
rm -f "$HOME/Library/LaunchAgents/com.local-device-bridge.plist"
rm -rf "$HOME/Library/Application Support/local-device-bridge"
security delete-generic-password -s local-device-bridge -a dashboard-token 2>/dev/null || true
security delete-generic-password -s local-device-bridge -a telegram_bot_token 2>/dev/null || true
```

TV token account names are identity-derived. Remove any remaining
`local-device-bridge` keychain items whose account begins with `tv-token-`, or
use Keychain Access and search for the service `local-device-bridge`.

### Linux

```sh
systemctl --user disable --now local-device-bridge.service 2>/dev/null || true
rm -f "$HOME/.local/bin/local-device-bridge"
rm -f "$HOME/.config/systemd/user/local-device-bridge.service"
rm -rf "$HOME/.config/local-device-bridge"
```

### Windows PowerShell

```powershell
Stop-Service local-device-bridge -ErrorAction SilentlyContinue
Remove-Item "$env:LOCALAPPDATA\local-device-bridge" -Recurse -Force
```

After a reset, install from the repository with `./install.sh --from-source`
on macOS/Linux or `.\install.ps1 -FromSource` on Windows, then run setup.
