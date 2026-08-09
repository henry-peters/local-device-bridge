# Device compatibility

Discovery and control are separate. A device is controllable only when a tested
adapter recognizes it and advertises the required capabilities.

| Family | Discovery | Control | Access |
| --- | --- | --- | --- |
| Samsung Smart TV | repeated SSDP, UPnP description, `/api/v2/` probe | tested local WebSocket remote + WOL | on-screen token pairing and network-remote setting |
| Roku TV/player | SSDP and ECP identity probe | tested ECP remote | **Control by mobile apps**; no pairing prompt |
| PlayStation / Xbox / Nintendo | SSDP/mDNS when advertised, plus remembered inventory | status and Wake-on-LAN when a MAC is known | enable the console's network-wake setting; official app required for account-backed control |
| macOS | Bonjour/host identity | host status; remote Mac status/wake/sleep | explicit restricted SSH setup |
| LG webOS TV | SSDP/mDNS/UPnP when advertised | not implemented | identified only; webOS pairing varies by model |
| Sony BRAVIA | SSDP/mDNS/UPnP when advertised | not implemented | identified only; IP-control authentication is model-specific |
| Windows computer / Raspberry Pi | mDNS/SSDP identity, bounded Windows service probe, or Raspberry Pi OUI/hostname | not implemented | identified only; no arbitrary remote shell |

Unknown ARP peers are retained only as internal evidence and never labeled as a
TV, display, Mac, Windows computer, or Raspberry Pi. Phones, tablets, audio
devices, and anonymous IP peers are intentionally excluded from the focused
inventory. Game consoles are a separate limited inventory category. Sleeping devices often
advertise no public discovery service, so absence from the inventory is
expected until a recognizable service appears. An IP address alone is
intentionally not enough to create a user-facing device record.

Before claiming hardware support, test discovery after DHCP changes, access
revocation, daemon restart, sleep/wake, navigation, volume, playback,
power-off, network isolation, and clear failure behavior.

Protocol references:

- [Samsung same-LAN `/api/v2/` diagnostic](https://developer.samsung.com/smarttv/develop/extension-libraries/smart-view-sdk/receiver-apps/debugging.html)
- [Roku External Control Protocol and SSDP discovery](https://developer.roku.com/dev/docs/external-control-api)
- [Google Cast `_googlecast._tcp` discovery troubleshooting](https://developers.google.com/cast/docs/discovery)
- [Sony BRAVIA professional-display IP control](https://pro-bravia.sony.net/develop/integrate/ip-control/)
