$ErrorActionPreference = 'Stop'
Unregister-ScheduledTask -TaskName 'local-device-bridge' -Confirm:$false
Write-Host 'Removed local-device-bridge scheduled task.'
