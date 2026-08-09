# Running the daemon as a service

Use one service supervisor for a host. Do not also run `local-device-bridge daemon` in a terminal, or two processes will compete for port `8787`.

The service definitions restart the daemon after an unexpected exit. This is the supported long-running installation path and prevents a crashed or manually replaced daemon from leaving the dashboard offline.

## macOS (launchd)

1. Build or download the binary and install it at `/usr/local/bin/local-device-bridge`.
2. Copy `com.local-device-bridge.plist` to `~/Library/LaunchAgents/`.
3. Load it:

```sh
launchctl bootout "gui/$(id -u)/com.local-device-bridge" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/com.local-device-bridge.plist"
launchctl kickstart -k "gui/$(id -u)/com.local-device-bridge"
```

Check status and logs:

```sh
launchctl print "gui/$(id -u)/com.local-device-bridge"
tail -f /tmp/local-device-bridge.log /tmp/local-device-bridge.err
```

After installing a new binary, run the `bootout`/`bootstrap` commands again. A service supervisor can restart a process, but it cannot replace an old binary automatically.

## Linux (systemd)

1. Install the binary at `/usr/local/bin/local-device-bridge`.
2. Copy `local-device-bridge.service` to `~/.config/systemd/user/`.
3. Enable and start it:

```sh
systemctl --user daemon-reload
systemctl --user enable --now local-device-bridge.service
```

Check it with:

```sh
systemctl --user status local-device-bridge.service
journalctl --user -u local-device-bridge.service -f
```

After installing a new binary:

```sh
systemctl --user restart local-device-bridge.service
```

## Windows

Run PowerShell as the account that should own the dashboard and execute:

```powershell
Set-ExecutionPolicy -Scope Process Bypass
& .\deploy\windows\install-task.ps1
```

The scheduled task starts at logon and restarts the daemon after an unexpected exit. After replacing the executable, run:

```powershell
Restart-ScheduledTask -TaskName local-device-bridge
```

## Manual terminal mode

For a quick test, run:

```sh
./local-device-bridge daemon
```

Keep that terminal open. `daemon` is intentionally a foreground process so logs remain visible; closing the terminal stops it. For permanent use, install the supervisor above. Check the active binary with:

```sh
curl http://127.0.0.1:8787/api/v1/health
```

If you rebuild the repository, stop the old process and start the newly built binary, or restart the service. The dashboard is embedded in the binary, so an already-running daemon will continue serving its previous build until restarted.
