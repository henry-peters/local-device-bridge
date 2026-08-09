# Architecture

## Product boundary

The daemon runs on one trusted computer, called the bridge host. It discovers nearby devices and routes dashboard, CLI, and Telegram requests through one transport-independent command manager.

The visible product boundary is TVs/displays and computers. The bridge host is always listed for status, but it is deliberately not controllable by its own wake/sleep actions. Remote Mac support is limited to fixed status, Wake-on-LAN, and `pmset sleepnow` operations. The phone/tablet dashboard is a browser remote; there is no arbitrary computer shell, media streaming, or cloud integration.

## Runtime flow

```text
Local host / SSDP / Bonjour / ARP
          |
          v
   device registry ---- SQLite metadata + audit
          |
          +---- Samsung adapter ---- HTTP metadata
          |                           WebSocket remote
          +---- Roku adapter -------- ECP HTTP remote
          |                           Wake-on-LAN
          |
          +---- Mac adapter --------- Wake-on-LAN
                                      fixed SSH/local pmset
          v
     command manager
       /      |       \
 dashboard   CLI    Telegram
```

Every transport creates the same `core.Command`:

```text
Command {
  device_id
  action
  arguments
  principal
  source
}
```

The manager resolves the device adapter, executes the action, and records success or failure in the audit store. A transport never sends Samsung packets or SSH commands directly.

## Packages

- `internal/core`: canonical TV/display and computer metadata, capabilities, commands, manager, pairing interfaces, and persistence interfaces.
- `internal/discovery`: route-aware local-host identification, per-interface SSDP and Bonjour discovery, bounded direct probes for documented TV/computer services, and ARP fallback.
- `internal/samsung`: `/api/v2/` metadata, fresh WebSocket pairing/command connections, channel authorization validation, remote-key encoding, power-off, Wake-on-LAN, and post-wake API confirmation.
- `internal/roku`: SSDP-identified Roku ECP devices on port 8060, shared navigation/playback/volume/power commands, Control by mobile apps guidance, and explicit capability errors where ECP cannot provide absolute volume or wake confirmation.
- `internal/macos`: explicit Mac status/wake/sleep adapter. The local bridge host is status-only at the dashboard and command-manager boundary.
- `internal/api`: authenticated versioned HTTP API, embedded dashboard/settings view, persisted inventory filters, and LAN TLS certificate generation.
- `internal/cli`: friendly setup output and CLI client for the local API.
- `internal/telegram`: outbound long-polling bot, allowlist authorization, tap-based TV inline remote callbacks, batched volume commands, TV/Mac commands, and `/commands` help.
- `internal/store`: SQLite device metadata and audit events.
- `internal/security`: OS keychain credentials with a restricted file fallback.

## Discovery model

SSDP sends repeated discovery queries on each selected directly-connected IPv4 interface and retains every response/location for a host before choosing the richest UPnP description. Samsung probes `http://<tv>:8001/api/v2/` and HTTPS port 8002 for stable DUID, model, MAC, and firmware. Roku uses SSDP plus its documented `query/device-info` endpoint on port 8060. Bonjour/mDNS identifies advertised computer and TV services on each active interface. ARP refreshes the directly connected subnet, and a bounded sweep probes only the documented Samsung/Roku APIs plus Windows RDP/WinRM identity ports. The bridge never scans arbitrary ports or sends credentials during discovery.

Inventory visibility is controlled by the TVs/displays, computers, and offline-device settings. Mobile, audio, console, and anonymous Other records are not exposed. Dashboard Settings writes the shared live state and config file; CLI, Telegram, and agents read the same manager list.

Discovery records may be duplicated across providers. The manager normalizes them into TVs & displays or Computers; computer cards retain only supported labels such as macOS, Windows, or Raspberry Pi. Generic Linux hosts, phones, speakers, consoles, and anonymous ARP peers stay internal and are not shown because an IP address alone is not a safe device identity. Stable DUID/MAC identity wins over provider IDs, and richer vendor metadata wins over generic service records. Persisted adapters are recreated on startup so a sleeping supported TV or Mac can still be targeted for Wake-on-LAN.

Samsung power-on refreshes discovery, sends Wake-on-LAN, and waits for the TV's local API to become reachable before returning success. A failed confirmation is audited as an error and is not repeated as a second power toggle. Remote commands use a fresh WebSocket connection, so a completed off/on cycle does not depend on a stale socket. The daemon listener retries after transient listener failures; launchd, systemd, and Windows Task Scheduler definitions supervise a full process crash.

## Pairing rules

### Samsung

Pairing opens the local Samsung remote WebSocket and waits for the TV’s `ms.channel.connect` or `ms.channel.connected` event. A token found in that event is stored in the OS keychain. A successful pairing updates both the manager metadata and the live adapter. Unpair deletes the token and marks the live adapter unpaired.

Remote commands require the live adapter to be paired. Every command connection also waits for a channel authorization event. If the TV’s mobile/device/network remote setting is disabled, the command fails with an instruction to enable it; the API, dashboard, CLI, Telegram, and audit record do not claim success.

### Roku

Roku does not use the Samsung-style approval token. The adapter requires the
TV's `Control by mobile apps` setting to be enabled, then sends the same
normalized remote actions through local ECP requests. A Roku discovery record
is marked available without pretending that this setting has been verified;
failed ECP requests return the setting and LAN guidance. Wake-on-LAN is
best-effort and power-on is only reported as confirmed when the device becomes
reachable again.

### Remote Mac

Remote pairing stores only the configured username in SQLite metadata after a non-interactive SSH `pmset -g ps` check succeeds. Remote sleep uses the fixed command `sudo -n /usr/bin/pmset sleepnow`. Wake uses the target MAC address and UDP ports 7/9. The bridge host does not expose these actions to any transport.

### Identified-only devices

Recognized LG/Sony TVs, Windows computers, and Raspberry Pi systems may be listed without controls. Their detail page gives honest access guidance. Generic Linux computers, anonymous ARP peers, phones, speakers, and consoles are hidden. Adding a brand requires a real authenticated adapter and tests rather than sending Samsung keys to unrelated hardware.

## Configuration

The generated config defaults to localhost-only HTTP, five-second discovery timeouts, informational devices enabled, and Telegram disabled. LAN mode requires explicit HTTPS configuration and browser pairing. Telegram requires an environment-provided bot token plus an allowlist of numeric user/chat IDs.

## Security boundaries

- The API is loopback-only by default.
- LAN binding is opt-in and uses HTTPS plus a browser pairing token.
- Telegram is outbound-only and rejects users/chats outside the configured allowlist.
- Samsung remote keys are an enum allowlist; no arbitrary packet endpoint exists.
- Mac remote execution accepts no arbitrary command string and only runs fixed power/status commands.
- TV tokens, dashboard tokens, and bot tokens are never written to normal logs.
- SQLite stores metadata, pairing state, and audit events; credentials use the OS keychain where available.
