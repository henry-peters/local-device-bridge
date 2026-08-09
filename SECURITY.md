# Security policy

## Deployment guidance

Run the daemon on a trusted host and keep the dashboard bound to `127.0.0.1` unless LAN access is required. If LAN access is enabled, use a trusted network and replace the generated certificate with a certificate trusted by the intended clients when practical.

The bridge host is intentionally status-only: its own dashboard, CLI, and Telegram clients cannot issue wake or sleep commands against it. Remote Mac sleep is restricted to a fixed `pmset` command after explicit pairing.

Do not expose port 8787 to the public internet or forward it from a router. Telegram does not require inbound connectivity.

Trusted-LAN dashboard mode defaults to token-protected HTTP for easier phone
setup and must only be used on a trusted home network. HTTPS with a generated
private certificate is available as an explicit hardened option. A browser's
certificate warning is expected until the operator explicitly trusts that
certificate on the device; use `local-device-bridge dashboard trust` to print
the platform command. The bridge does not silently alter OS trust stores.

## Credentials

TV pairing tokens, dashboard credentials, and Telegram bot tokens entered by
the setup wizard are stored using the native OS keychain. If the keychain is
unavailable, the bridge uses a `0600` fallback file under the state directory.
The `TELEGRAM_BOT_TOKEN` environment variable overrides the saved Telegram
token and is useful for services or CI. Tokens are never included in setup
output, SQLite, or logs.

Telegram private allowlists are the default. Public command mode is an explicit
setup confirmation and should only be used when the bot is intentionally
available to every Telegram account that can find it; that account can then
control the bridge.

The agent manifest and OpenAPI endpoint are protected by the same bearer token
as the local API. Treat `local-device-bridge agent token` output as a secret.
The reusable dashboard and agent tokens are never printed by setup. The
one-time phone pairing link and its QR code are intentionally displayed when
the operator explicitly requests phone access; they expire after ten minutes
and can be used only once.

## Reporting

Please do not publish credentials, TV pairing tokens, network captures, or public IP addresses in an issue. For security reports, open a private GitHub security advisory when the repository is published.
