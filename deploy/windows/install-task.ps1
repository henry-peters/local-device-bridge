$ErrorActionPreference = 'Stop'
$Binary = Join-Path $PSScriptRoot '..\local-device-bridge.exe'
$Binary = (Resolve-Path $Binary).Path
$Action = New-ScheduledTaskAction -Execute $Binary -Argument 'daemon'
$Trigger = New-ScheduledTaskTrigger -AtLogOn
$Principal = New-ScheduledTaskPrincipal -UserId $env:USERNAME -LogonType Interactive -RunLevel Limited
$Settings = New-ScheduledTaskSettingsSet `
  -StartWhenAvailable `
  -RestartCount 999 `
  -RestartInterval (New-TimeSpan -Minutes 1) `
  -ExecutionTimeLimit (New-TimeSpan -Seconds 0)
Register-ScheduledTask -TaskName 'local-device-bridge' -Action $Action -Trigger $Trigger -Principal $Principal -Settings $Settings -Force
Write-Host 'Installed local-device-bridge scheduled task.'
