#!/usr/bin/env pwsh
# Local integration test — simulates AC firmware + checks MQTT output.
# Requires: docker compose up -d (service on :180, mosquitto on :1883)
# Requires: mosquitto_sub in PATH (from mosquitto-clients package)
#           OR just use curl to hit the HTTP endpoints.

$base = "http://localhost:180"

Write-Host "=== 1. Health check GET / ===" -ForegroundColor Cyan
$r = Invoke-WebRequest -Uri "$base/" -Method GET
if ($r.Content -eq "OK") { Write-Host "PASS: GET / = OK" -ForegroundColor Green }
else { Write-Host "FAIL: GET / = $($r.Content)" -ForegroundColor Red }

Write-Host ""
Write-Host "=== 2. GET /status (should show Never before first POST) ===" -ForegroundColor Cyan
$r = Invoke-WebRequest -Uri "$base/status" -Method GET
Write-Host $r.Content

Write-Host ""
Write-Host "=== 3. POST /data D=6 (simulates AC sending state) ===" -ForegroundColor Cyan
$body = @'
{
  "G":"0","V":2,"D":6,
  "DA":{
    "compressorActivity":1,
    "errorCode":"",
    "fanIsCont":0,
    "fanSpeed":1,
    "isOn":true,
    "isInESP_Mode":false,
    "mode":2,
    "roomTemp_oC":23.5,
    "setPoint":22.0,
    "enabledZones":[1,1,0,0,0,0,0,0],
    "individualZoneTemperatures_oC":[21.5,22.1,null,null,null,null,null,null]
  }
}
'@
$r = Invoke-WebRequest -Uri "$base/rest/1/block/Default/data" `
     -Method POST -Body $body -ContentType "application/json"
Write-Host "Response: $($r.Content)"
if ($r.Content -eq '{"result":1,"error":null,"id":0}') {
    Write-Host "PASS: POST /data returned correct response" -ForegroundColor Green
} else {
    Write-Host "FAIL" -ForegroundColor Red
}

Write-Host ""
Write-Host "=== 4. GET /status (should show last update time now) ===" -ForegroundColor Cyan
$r = Invoke-WebRequest -Uri "$base/status" -Method GET
Write-Host $r.Content

Write-Host ""
Write-Host "=== 5. Long-poll GET /commands (should timeout in ~10s with empty body) ===" -ForegroundColor Cyan
Write-Host "Waiting up to 12s..."
$r = Invoke-WebRequest -Uri "$base/rest/1/block/Default/commands" `
     -Method GET -TimeoutSec 15
Write-Host "Status: $($r.StatusCode)  Body length: $($r.Content.Length)"
if ($r.StatusCode -eq 200 -and $r.Content.Length -eq 0) {
    Write-Host "PASS: long-poll returned empty 200 on timeout" -ForegroundColor Green
} else {
    Write-Host "INFO: got body: $($r.Content)" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "=== 6. POST /usage/log ===" -ForegroundColor Cyan
$r = Invoke-WebRequest -Uri "$base/usage/log" `
     -Method POST -Body '{"mode":"test","method":"curl"}' -ContentType "application/json"
if ($r.Content -match '"status":200') {
    Write-Host "PASS: POST /usage/log" -ForegroundColor Green
} else {
    Write-Host "FAIL: $($r.Content)" -ForegroundColor Red
}

Write-Host ""
Write-Host "=== 7. GET /v0/AConnect ===" -ForegroundColor Cyan
$r = Invoke-WebRequest -Uri "$base/v0/AConnect?serial=TEST&mac=00:11:22&reboots=1&uptime_mins=5&bootloader=1&wifi=-70&flash_fam=2&version=1.0&msg=hello" -Method GET
if ($r.Content -match "download=") {
    Write-Host "PASS: GET /v0/AConnect" -ForegroundColor Green
    Write-Host $r.Content
} else {
    Write-Host "FAIL: $($r.Content)" -ForegroundColor Red
}

Write-Host ""
Write-Host "=== MQTT check (requires mosquitto_sub) ===" -ForegroundColor Cyan
Write-Host "Run this in a separate terminal to watch MQTT topics:"
Write-Host "  mosquitto_sub -h localhost -p 1883 -t '#' -v" -ForegroundColor DarkGray
Write-Host ""
Write-Host "Expect to see after POST /data:"
Write-Host "  actron/aircon/Default/temperature  23.5"
Write-Host "  actron/aircon/Default/mode         cool"
Write-Host "  actron/aircon/Default/fanmode      medium"
Write-Host "  actron/aircon/Default/settemperature  22"
Write-Host "  actron/aircon/Default/zone1        ON"
Write-Host "  actron/aircon/Default/zone2        ON"
Write-Host "  actron/aircon/Default/zone3        OFF"
Write-Host "  hass-actron/status                 online"
Write-Host ""
Write-Host "Done." -ForegroundColor Cyan
