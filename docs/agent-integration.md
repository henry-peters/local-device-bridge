# AI agent integration

local-device-bridge can be used by an AI agent that supports authenticated HTTP
or OpenAPI tools. The bridge host and the agent must be on the same trusted home
LAN unless you deliberately provide another secure network path. Do not expose
the API to the public internet.

## What the agent can do

The generated API lets an authorized agent:

- discover and list supported devices;
- read the exact pairing/setup guide for a device;
- pair or unpair a device when its adapter supports pairing;
- assign a friendly name such as `Living Room TV`;
- read status and capabilities;
- execute only normalized, capability-checked commands such as power, volume,
  mute, navigation, source, channel, and play/pause.

The bridge host itself is status-only. It cannot be woken or put to sleep from
its own dashboard, CLI, Telegram, or agent connection.

## Connect an agent

On the bridge computer:

```sh
local-device-bridge discover
local-device-bridge agent token
local-device-bridge agent manifest
local-device-bridge agent openapi
```

Give the generated agent token only to the agent's private secret or connector
settings. Do not put it in a repository, public prompt, Telegram message, QR
code, or URL. The token is unique to this installation and is separate from:

- the dashboard token used only for manual browser fallback;
- the one-time phone QR/link credential;
- the Telegram BotFather token.

The agent API base is the bridge URL followed by `/api/v1`. For a trusted-LAN
setup it is commonly `https://BRIDGE-HOST:8787/api/v1`; the exact URL and any
certificate guidance are printed by `local-device-bridge dashboard phone`.

Recommended instruction for an agent:

> Use local-device-bridge on my trusted home LAN. Authenticate with the private
> bearer token I provided. Read `/api/v1/agent/manifest`, list devices, read the
> selected device guide, and ask me before pairing, powering, or changing a
> device. Use the saved friendly device name for later commands.

## Safe pairing workflow

The agent must not claim that a command succeeded merely because an HTTP request
was sent. It should follow this sequence:

1. `GET /api/v1/devices`
2. `GET /api/v1/devices/{id}/guide`
3. Follow the displayed device-specific settings steps.
4. `POST /api/v1/devices/{id}/pair` when the guide says pairing is available.
5. The user accepts the device's on-screen prompt, if one appears.
6. Confirm `paired` and the device capabilities before sending commands.
7. Use `POST /api/v1/devices/{id}/name` to save a friendly name if requested.

For Samsung TVs, the TV must be awake, on the same non-guest Wi-Fi, and have
its network/mobile remote setting enabled. Common settings are **Settings →
General → Network → Expert Settings → Power On with Mobile** and **Settings →
General → External Device Manager → Device Connect Manager → Access
Notification**. Model and firmware labels vary. The user must accept the
Samsung permission prompt before the remote is unlocked.

Roku uses its own **Control by mobile apps** setting and does not show a
Samsung-style pairing prompt. Recognized LG/Sony TVs and Windows/Raspberry Pi
computers may be listed with guidance but do not receive a fake universal
remote. New TV brands require a tested adapter with its own authentication and
capability rules.

## Phone remote versus agent access

For a human using a phone or iPad, run:

```sh
local-device-bridge dashboard phone
```

Scan the printed QR code with the phone Camera app. The one-time link signs the
browser in automatically and opens the dashboard remote; no API URL or agent
token needs to be typed. The phone and bridge must remain on the same trusted
Wi-Fi. Use the dashboard's **Agent API** page for a human-readable copy of the
agent workflow.
