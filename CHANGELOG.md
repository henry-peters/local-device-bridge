# Changelog

All notable public releases of `local-device-bridge` are documented here.

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
