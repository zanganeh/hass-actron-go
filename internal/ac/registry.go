package ac

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"hass-actron/internal/config"
)

const (
	defaultUnitKey = "Default"
	mqttInitDelay  = 10 * time.Second
	mqttInterval   = 30 * time.Second
)

// Registry holds all AC units. Immutable after Configure completes.
type Registry struct {
	units map[string]*acUnit
	first *acUnit // fallback for unknown device keys
}

// Configure creates all unit instances, publishes HA discovery, subscribes MQTT topics.
// MQTT client must be started before calling Configure (SPEC §1 CRITICAL ORDER).
func Configure(cfg *config.Config, m mqttSender) *Registry {
	r := &Registry{units: make(map[string]*acUnit)}

	unitNames := cfg.MultipleUnits
	if len(unitNames) == 0 {
		unitNames = []string{defaultUnitKey}
	}

	for _, unitName := range unitNames {
		clientID := "hass-actron"
		if unitName != defaultUnitKey {
			clientID = "hass-actron" + strings.ToLower(unitName)
		}

		// Build zones slice sorted by zone ID (T10: count-based topic numbering)
		var zones []zoneEntry
		for _, z := range cfg.Zones {
			zUnit := z.Unit
			if zUnit == "" {
				zUnit = defaultUnitKey
			}
			if zUnit != unitName {
				continue
			}
			if z.ID < 1 || z.ID > 8 {
				continue
			}
			zones = append(zones, zoneEntry{id: z.ID, name: z.Name})
			if len(zones) >= 8 {
				break
			}
		}
		sort.Slice(zones, func(i, j int) bool { return zones[i].id < zones[j].id })

		ucfg := unitConfig{registerZoneTemps: cfg.RegisterZoneTemperatures}
		u := newACUnit(unitName, clientID, zones, m, ucfg)

		if r.first == nil {
			r.first = u
		}
		r.units[unitName] = u

		u.PublishDiscovery(cfg.RegisterZoneTemperatures)
		u.SubscribeCommands(r)
	}

	return r
}

// Unit returns the unit for a device key, falling back to first unit.
func (r *Registry) Unit(device string) *acUnit {
	if u, ok := r.units[device]; ok {
		return u
	}
	return r.first
}

// AllUnits returns all units.
func (r *Registry) AllUnits() []*acUnit {
	units := make([]*acUnit, 0, len(r.units))
	for _, u := range r.units {
		units = append(units, u)
	}
	return units
}

// MQTTUpdate publishes online/offline for all units.
func (r *Registry) MQTTUpdate() {
	for _, u := range r.units {
		u.MQTTAvailability()
	}
}

// StartMQTTTimer fires MQTTUpdate every 30s after 10s delay (SPEC §9).
func (r *Registry) StartMQTTTimer(ctx context.Context) {
	go func() {
		select {
		case <-time.After(mqttInitDelay):
		case <-ctx.Done():
			return
		}
		r.MQTTUpdate()
		ticker := time.NewTicker(mqttInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				r.MQTTUpdate()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// StartPollWatchers starts per-unit poll warning goroutines.
func (r *Registry) StartPollWatchers(ctx context.Context) {
	for _, u := range r.units {
		u.StartPollWatcher(ctx)
	}
}

// PostData ingests AC state for a given device key.
// Also triggers MQTTUpdate (SPEC §3 Route 2 — called on every POST /data).
func (r *Registry) PostData(device string, d acData) {
	u := r.Unit(device)
	u.dataReceived = true
	u.PostData(d)
	r.MQTTUpdate()
}

// GetCommand dequeues the next pending command for a device.
func (r *Registry) GetCommand(device string) (string, string) {
	return r.Unit(device).GetCommand()
}

// UpdateRequestTime records GET /commands receipt time.
func (r *Registry) UpdateRequestTime(device string) {
	r.Unit(device).UpdateRequestTime()
}

// EventC returns the command event channel for a device (for long-poll).
func (r *Registry) EventC(device string) <-chan struct{} {
	return r.Unit(device).event.C()
}

// StatusHTML builds the /status HTML body (replicates overwrite bug — T9).
func (r *Registry) StatusHTML() string {
	result := ""
	for _, u := range r.units {
		result += "Unit: " + u.name + "<br/>"
		html, received := u.StatusHTML()
		if received {
			result += html
		} else {
			result = html // T9: = not += overwrites all prior content
		}
	}
	return result
}

// MQTT inbound routing (used by subscription callbacks)

func (r *Registry) HandleMode(unit string, payload string) {
	r.Unit(unit).ChangeMode(parseModePayload(payload))
}

func (r *Registry) HandleFan(unit string, payload string) {
	r.Unit(unit).ChangeFanSpeed(parseFanPayload(payload))
}

func (r *Registry) HandleTemperature(unit string, temp float64) {
	r.Unit(unit).ChangeTemperature(temp)
}

// HandleMQTTMessage is a fallback raw-topic router (T7 strip logic).
// Not used in normal operation — subscriptions route directly via callbacks.
func (r *Registry) HandleMQTTMessage(topic string, payload string) {
	parts := strings.Split(topic, "/")
	if len(parts) != 5 {
		log.Printf("MQTT unexpected topic token count: %q", topic)
		return
	}
	unitName := parts[2]
	stripped := strings.Replace(topic, unitName+"/", "", -1) // T7

	u := r.Unit(unitName)

	switch stripped {
	case "actron/aircon/mode/set":
		u.ChangeMode(parseModePayload(payload))
	case "actron/aircon/fan/set":
		u.ChangeFanSpeed(parseFanPayload(payload))
	default:
		log.Printf("MQTT unhandled topic: %q", topic)
	}
}

func parseModePayload(p string) int {
	switch p {
	case "off":
		return -1
	case "auto":
		return 0
	case "heat":
		return 1
	case "cool":
		return 2
	case "fan_only":
		return 3
	default:
		return -1
	}
}

func parseFanPayload(p string) int {
	switch p {
	case "low":
		return 0
	case "medium":
		return 1
	case "high":
		return 2
	default:
		return 0
	}
}
