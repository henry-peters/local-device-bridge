# Changelog

All notable public releases of `local-device-bridge` are documented here.

## [1.0.6] - 2026-08-10

### Security and pairing

- Removed dashboard-generated SSH-key and `sudoers` commands from Mac pairing.
- Mac pairing now checks existing SSH access and non-privileged power status;
  it never creates keys, edits target files, stores Mac passwords, or accepts
  arbitrary shell commands.
- The CLI now accepts only `pair <device>` and privately prompts for a remote
  Mac's short account name when required, keeping it out of shell history.
- Sleep authorization is reported separately and is never silently granted by
  pairing.

### Reliability

- macOS launchd keeps the daemon alive across network changes and unexpected
  exits, and `dashboard open` repairs a missing per-user service before using a
  temporary fallback.
- Added regression tests for safe Mac probes and persistent service manifests.

## [1.0.5] - 2026-08-09

### Added

- Setup automatically registers the dashboard daemon with macOS `launchd`,
  Linux user `systemd`, or Windows Task Scheduler.
- Added `local-device-bridge service install|uninstall` for repairing or
  intentionally removing automatic startup.

### Reliability

- The operating-system supervisor keeps one daemon process alive after login,
  restart, and unexpected daemon exits.

## [1.0.4] - 2026-08-09

### Fixed

- Keep the phone QR renderer working when setup is launched through a pipe or
  another non-TTY output mode.
- Automatically repair older LAN-enabled configurations that still bind the
  service to localhost, which prevented phones from connecting.
- Support an optional `server.dashboard_origin` override for computers with
  multiple network interfaces.

### Added

- Console inventory and CLI/dashboard support for PlayStation, Xbox, and
  Nintendo discovery.
- Safe console status and Wake-on-LAN controls when a MAC address is known,
  with explicit guidance instead of pretending to support universal console
  account control or power-off.

## [1.0.3] - 2026-08-09

### Fixed

- Correct the default release repository in the macOS/Linux and Windows
  installers so a normal install downloads from the public project repository.

### Release notes

This is the recommended release for new installations. Existing `v1.x` users
can upgrade normally.

## [1.0.2] - 2026-08-09

### Fixed

- Align the embedded API health version with the published release.
- Add a public release history so operators can verify what changed between
  installed versions.
- Correct the documented GitHub repository URL used by the source installer.

### Release notes

This is a maintenance release. It contains no new device protocol and is safe
to install over `v1.0.0` or `v1.0.1`.

## [1.0.1] - 2026-08-09

### Security

- Update `golang.org/x/net` to a patched release and refresh related Go
  networking dependencies.
- Verify the repository dependency graph has no remaining known alerts for the
  vulnerabilities fixed by this release.

## [1.0.0] - 2026-08-09

### Added

- First stable cross-platform release for macOS, Linux, and Windows.
- Samsung TV discovery, on-screen pairing, remote control, volume, playback,
  source, channel, power-off, and Wake-on-LAN support.
- Roku identification and ECP control where the Roku mobile-control setting is
  enabled.
- Focused inventory for TVs/displays, macOS, Windows, and Raspberry Pi
  computers.
- Local dashboard, terminal CLI, optional Telegram transport, SQLite state,
  OS keychain credentials, and authenticated AI-agent API.
- Cross-platform release artifacts and automated test/build workflows.

### Security and usability

- Dashboard stays on localhost by default.
- Phone access uses a one-use QR browser pairing link on trusted home Wi-Fi;
  HTTPS is optional and explicitly configured.
- TV commands report confirmed results or actionable failures instead of
  claiming success merely because a request was sent.

## Versioning

The project follows semantic versioning. The pre-stable `v0.2.0` preview was
retired from the public release list; stable users should start at `v1.0.0`
and upgrade to the latest `v1.x` release.
