# CLAUDE.md — hass-actron-go

Go rewrite of the .NET 6 `hass-actron` Home Assistant add-on. Bridges Actron
Air Conditioning units to Home Assistant via MQTT. Target: <25 MB RSS on
Raspberry Pi 3 (ARMv7 / aarch64).

---

## Architecture

```
AC firmware  →  HTTP POST /rest/1/block/{unit}/data
                         ↓
              cmd/hass-actron  (main)
                   ├── internal/httpserver   HTTP :180 (AC ↔ HA bridge)
                   ├── internal/ac           State machine, zone registry
                   ├── internal/mqtt         paho wrapper, QoS 1, retain
                   ├── internal/config       JSON options.json unmarshal
                   └── internal/proxy        DNS cache, forward to cloud
```

State machine lives in `internal/ac/unit.go`. One `Unit` per AC serial.
`Registry` in `internal/ac/registry.go` owns all units; keyed by serial/name.

---

## Release Process (STRICT — do not deviate)

**Only release from `main`/`master` with a clean working tree.**

```powershell
.\scripts\release.ps1 -Version X.Y.Z
```

The script enforces:
1. Version is higher than current `config.json` version
2. Branch is `main` or `master`
3. Working tree is clean
4. Local branch is up-to-date with remote
5. `go test ./...` passes
6. Bumps `config.json`, commits `chore: release vX.Y.Z`, tags `vX.Y.Z`, pushes

**Never** create a tag manually. **Never** tag without bumping `config.json`.
**Never** release from a feature branch.

After the script runs, GitHub Actions builds 5-arch Docker images and pushes
them to GHCR. HA installations see the update automatically.

---

## Spec Traps (T1–T11)

All 11 traps from the original .NET spec are implemented. **Do not break them.**

| Trap | Location | Description |
|------|----------|-------------|
| T1 | `internal/ac/event.go` | ManualResetEvent → closed-channel broadcast |
| T2 | `unit.go:ChangeMode` | `iMode` read inside `cmdMu`, no `dataMu` |
| T3 | `unit.go:PostData` | Suppress timer 8s post-command |
| T4 | `unit.go` | `enabledZones` pre-init to `"0,0,0,0,0,0,0,0"` |
| T5 | `unit.go:PostData` | Snapshot pattern — unlock before MQTT publish |
| T6 | `command.go:modeToMQTT` | mode -1 → "off", 3 → "fan_only" guards |
| T7 | `unit.go:BackSync` | Command fields: check `!pendingCommand && !suppressed` first |
| T8 | `unit.go:BackSync` | Zone fields: check `!suppressed && !pendingZone` first |
| T9 | `registry.go:StatusHTML` | `result = html` not `+=` for never-seen units |
| T10 | `unit.go` | Zone topic num = count-based (`i+1`), AC index = `zone.id - 1` |
| T11 | `main.go` | Shutdown: PublishOffline → sleep 500ms → Disconnect |

---

## Zone Mapping Rule

Zones are stored as `[]zoneEntry{id, name}` sorted by `id`. The MQTT topic
number is position-based (`i+1`), the AC array index is `zone.id - 1`.

Example: zones configured as IDs {1, 3, 5}:
- `zone1` → AC bZone[0], AC dblZoneTemp[0]
- `zone2` → AC bZone[2], AC dblZoneTemp[2]
- `zone3` → AC bZone[4], AC dblZoneTemp[4]

**Never** use the zone ID as the topic number directly.

---

## MQTT

- QoS 1, retain=true for all publishes
- QoS 1 for subscriptions
- CleanSession=true; resubscribe via `OnConnect` handler
- ClientID: `hass-actron` (must not conflict with another running instance)
- Broker default: `core-mosquitto` (HA Mosquitto add-on)

---

## Back-Sync Order

Order matters — do not swap:

```
command fields:  if !pendingCommand && !suppressed → publish
zone fields:     if !suppressed && !pendingZone    → publish
```

`pendingCommand` checked before `suppressed` for commands.
`suppressed` checked before `pendingZone` for zones.

---

## Testing

```bash
go test ./...
```

Key tests in `internal/ac/unit_test.go`:
- `TestNonContiguousZoneMapping` — zones {1,3,5} map correctly
- `TestZoneTopicCountBased` — topic num ≠ zone ID
- `TestStatusHTMLOverwrite` — T9 regression

Local integration test (requires Docker):
```bash
docker compose up -d
pwsh testdata/test-local.ps1
```

---

## Config (options.json / HA add-on schema)

| Field | Type | Default | Notes |
|-------|------|---------|-------|
| `MQTTBroker` | str | `core-mosquitto` | |
| `MQTTUser` | str? | `""` | |
| `MQTTPassword` | str? | `""` | |
| `MQTTLogs` | bool? | `true` | |
| `MQTTTLS` | bool? | `false` | |
| `RegisterZoneTemperatures` | bool? | `false` | Publish per-zone temp sensors |
| `ForwardToOriginalWebService` | bool? | `false` | Proxy to Actron cloud |
| `Zones` | list | — | `{Name, Id, Unit?}` |

`Unit` defaults to `"Default"` when absent.

---

## Do Not

- Do not change the MQTT topic structure without updating HA discovery config payloads
- Do not use QoS 2 (core-mosquitto disconnects on QoS 2 with CleanSession=true)
- Do not add `sync.Mutex` locks around MQTT publish calls (deadlock risk — T5)
- Do not iterate zones by map (non-deterministic order) — use `[]zoneEntry` sorted slice
- Do not release without running `go test ./...`
- Do not push tags manually — use `scripts/release.ps1`
