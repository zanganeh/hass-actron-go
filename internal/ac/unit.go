package ac

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	suppressDuration  = 8 * time.Second
	offlineThreshold  = 10 * time.Minute
	pollWarnThreshold = 5 * time.Minute
)

// mqttSender is the subset of mqtt.Client used by acUnit.
type mqttSender interface {
	Publish(topic string, payload string)
	Subscribe(topic string, handler func(payload string))
}

// zoneEntry holds one configured zone. Sorted by id at init time.
// topicNum = slice index + 1 (sequential 1..N for MQTT topics).
// id = AC zone position (1-8), indexes into enabledZones[id-1] and bZone[id-1].
type zoneEntry struct {
	id   int
	name string
}

type acData struct {
	bOn                 bool
	bESPOn              bool
	iMode               int
	iFanSpeed           int
	dblSetTemperature   float64
	dblRoomTemperature  float64
	iFanContinuous      int
	iCompressorActivity int
	strErrorCode        string
	bZone               [8]bool    // index = zoneID-1
	dblZoneTemp         [8]float64 // index = zoneID-1
	dtLastUpdate        time.Time  // zero value = offline at start (spec §5.1)
	dtLastRequest       time.Time
}

type acUnit struct {
	name     string
	clientID string
	zones    []zoneEntry // sorted by id, immutable after init

	dataMu sync.Mutex
	data   acData

	cmdMu          sync.Mutex
	cmd            acCommand
	pendingCommand bool
	pendingZone    bool
	lastCommand    time.Time // zero = no suppress

	dataReceived bool // read/written without lock — replicates .NET race (T2)
	event        *commandEvent
	mqtt         mqttSender
	cfg          unitConfig
}

type unitConfig struct {
	registerZoneTemps bool
}

func newACUnit(name, clientID string, zones []zoneEntry, m mqttSender, cfg unitConfig) *acUnit {
	return &acUnit{
		name:     name,
		clientID: clientID,
		zones:    zones,
		cmd:      newCommand(), // T4: enabledZones = "0,0,0,0,0,0,0,0"
		event:    newCommandEvent(),
		mqtt:     m,
		cfg:      cfg,
	}
}

// PostData ingests an AC state update (from POST /data D=6).
func (u *acUnit) PostData(d acData) {
	// Read suppress state under cmdMu (lastCommand lives there)
	u.cmdMu.Lock()
	suppressed := time.Since(u.lastCommand) < suppressDuration
	u.cmdMu.Unlock()

	// Update data under dataMu
	u.dataMu.Lock()
	u.data.dblRoomTemperature = d.dblRoomTemperature
	u.data.bESPOn = d.bESPOn
	u.data.iFanContinuous = d.iFanContinuous
	u.data.iCompressorActivity = d.iCompressorActivity
	u.data.strErrorCode = d.strErrorCode
	u.data.dtLastUpdate = d.dtLastUpdate
	u.data.dblZoneTemp = d.dblZoneTemp

	if !suppressed {
		u.data.bOn = d.bOn
		u.data.iMode = d.iMode
		u.data.iFanSpeed = d.iFanSpeed
		u.data.dblSetTemperature = d.dblSetTemperature
		u.data.bZone = d.bZone
	}

	snap := u.data // snapshot — no lock held during MQTT publish
	u.dataMu.Unlock()

	// Back-sync command from data (SPEC §5.3)
	u.backSyncCommand(d, suppressed)

	// Always-published MQTT topics
	unit := u.name
	u.mqtt.Publish("actron/aircon/"+unit+"/temperature", fmt.Sprintf("%g", snap.dblRoomTemperature))

	espVal := "OFF"
	if snap.bESPOn {
		espVal = "ON"
	}
	u.mqtt.Publish("actron/aircon/"+unit+"/esp", espVal)

	fanContVal := "OFF"
	if snap.iFanContinuous != 0 {
		fanContVal = "ON"
	}
	u.mqtt.Publish("actron/aircon/"+unit+"/fancont", fanContVal)
	u.mqtt.Publish("actron/aircon/"+unit+"/compressor", compressorPayload(snap.iCompressorActivity, snap.bOn))

	// Zone temps: topicNum = i+1, AC data index = zones[i].id-1
	if u.cfg.registerZoneTemps {
		for i, z := range u.zones {
			u.mqtt.Publish(
				fmt.Sprintf("actron/aircon/%s/zone%d/temperature", unit, i+1),
				fmt.Sprintf("%g", snap.dblZoneTemp[z.id-1]),
			)
		}
	}

	// Suppressed MQTT topics
	if !suppressed {
		u.mqtt.Publish("actron/aircon/"+unit+"/fanmode", fanSpeedToMQTT(snap.iFanSpeed))
		u.mqtt.Publish("actron/aircon/"+unit+"/mode", modeToMQTT(snap.bOn, snap.iMode))
		u.mqtt.Publish("actron/aircon/"+unit+"/settemperature", fmt.Sprintf("%g", snap.dblSetTemperature))

		// Zone state: topicNum = i+1, AC data index = zones[i].id-1
		for i, z := range u.zones {
			zVal := "OFF"
			if snap.bZone[z.id-1] {
				zVal = "ON"
			}
			u.mqtt.Publish(fmt.Sprintf("actron/aircon/%s/zone%d", unit, i+1), zVal)
		}
	}
}

func (u *acUnit) backSyncCommand(d acData, suppressed bool) {
	u.cmdMu.Lock()
	defer u.cmdMu.Unlock()

	// Commands: check pendingCommand FIRST, then suppress (SPEC §5.3)
	if !u.pendingCommand && !suppressed {
		mode := d.iMode
		if mode == -1 {
			mode = 0
		}
		u.cmd.amOn = d.bOn
		u.cmd.mode = mode
		u.cmd.fanSpeed = d.iFanSpeed
		u.cmd.tempTarget = d.dblSetTemperature
	}

	// Zones: check suppress FIRST, then pendingZone (SPEC §5.3 — different order)
	if !suppressed && !u.pendingZone {
		parts := make([]string, 8)
		for i := 0; i < 8; i++ {
			if d.bZone[i] {
				parts[i] = "1"
			} else {
				parts[i] = "0"
			}
		}
		u.cmd.enabledZones = strings.Join(parts, ",")
	}
}

// GetCommand dequeues the next pending command.
func (u *acUnit) GetCommand() (string, string) {
	u.cmdMu.Lock()
	defer u.cmdMu.Unlock()

	if u.pendingCommand {
		cmd := u.cmd // value copy (T3)
		u.pendingCommand = false
		if !u.pendingZone {
			u.event.Reset()
		}
		return "4", buildCommand4(cmd)
	}
	if u.pendingZone {
		cmd := u.cmd // value copy (T3)
		u.pendingZone = false
		u.event.Reset()
		return "5", buildCommand5(cmd)
	}
	return "", ""
}

func (u *acUnit) postCommand(newCmd acCommand, forZone bool) {
	u.cmdMu.Lock()
	defer u.cmdMu.Unlock()

	mainDiffers := newCmd.amOn != u.cmd.amOn ||
		newCmd.fanSpeed != u.cmd.fanSpeed ||
		newCmd.mode != u.cmd.mode ||
		newCmd.tempTarget != u.cmd.tempTarget
	zoneDiffers := newCmd.enabledZones != u.cmd.enabledZones

	u.cmd = newCmd // value copy (T3)

	if !forZone && mainDiffers {
		u.pendingCommand = true
		u.lastCommand = time.Now()
		u.event.Set()
	}
	if zoneDiffers {
		u.pendingZone = true
		u.lastCommand = time.Now()
		u.event.Set()
	}
}

// ChangeMode handles MQTT mode/set.
func (u *acUnit) ChangeMode(mode int) {
	// T2: read iMode WITHOUT dataMu while holding cmdMu — replicates .NET race
	u.cmdMu.Lock()
	newCmd := u.cmd
	iMode := u.data.iMode // intentional race: no dataMu (T2)
	u.cmdMu.Unlock()

	if mode == -1 { // None — off, preserve current mode
		newCmd.amOn = false
		newCmd.mode = iMode
	} else {
		newCmd.amOn = true
		newCmd.mode = mode
	}

	u.mqtt.Publish("actron/aircon/"+u.name+"/mode", modeToMQTT(newCmd.amOn, newCmd.mode))
	u.postCommand(newCmd, false)
}

// ChangeFanSpeed handles MQTT fan/set.
func (u *acUnit) ChangeFanSpeed(fanSpeed int) {
	u.cmdMu.Lock()
	newCmd := u.cmd
	u.cmdMu.Unlock()

	newCmd.fanSpeed = fanSpeed
	u.mqtt.Publish("actron/aircon/"+u.name+"/fanmode", fanSpeedToMQTT(fanSpeed))
	u.postCommand(newCmd, false)
}

// ChangeTemperature handles MQTT temperature/set.
func (u *acUnit) ChangeTemperature(temp float64) {
	u.cmdMu.Lock()
	newCmd := u.cmd
	u.cmdMu.Unlock()

	newCmd.tempTarget = temp
	// Optimistic publish: %g format (no "F1" — SPEC §4.6 note)
	u.mqtt.Publish("actron/aircon/"+u.name+"/settemperature", fmt.Sprintf("%g", temp))
	u.postCommand(newCmd, false)
}

// changeZone handles a zone toggle. topicNum is the sequential MQTT topic number
// (1..len(zones)); acZoneID is the AC's position (1-8) in enabledZones.
// These differ when zones are non-contiguous (e.g. zones {1,3,5} → topicNum 1,2,3).
func (u *acUnit) changeZone(topicNum int, acZoneID int, on bool) {
	u.cmdMu.Lock()
	newCmd := u.cmd // value copy

	parts := strings.Split(newCmd.enabledZones, ",")
	if len(parts) == 8 && acZoneID >= 1 && acZoneID <= 8 {
		if on {
			parts[acZoneID-1] = "1"
		} else {
			parts[acZoneID-1] = "0"
		}
		newCmd.enabledZones = strings.Join(parts, ",")
	}

	// Optimistic publish inside lock (SPEC §4.6); topic uses sequential topicNum
	zVal := "OFF"
	if on {
		zVal = "ON"
	}
	u.mqtt.Publish(fmt.Sprintf("actron/aircon/%s/zone%d", u.name, topicNum), zVal)
	u.cmdMu.Unlock()

	u.postCommand(newCmd, true)
}

// UpdateRequestTime records the time of a GET /commands request.
func (u *acUnit) UpdateRequestTime() {
	u.dataMu.Lock()
	u.data.dtLastRequest = time.Now()
	u.dataMu.Unlock()
}

// IsOnline returns true if last PostData was within 10 minutes.
func (u *acUnit) IsOnline() bool {
	u.dataMu.Lock()
	defer u.dataMu.Unlock()
	return time.Since(u.data.dtLastUpdate) < offlineThreshold
}

// StatusHTML returns the HTML fragment for GET /status.
func (u *acUnit) StatusHTML() (string, bool) {
	u.dataMu.Lock()
	dr := u.dataReceived
	lu := u.data.dtLastUpdate
	lr := u.data.dtLastRequest
	u.dataMu.Unlock()

	if dr {
		return "Last Post from Air Conditioner: " + lu.String() + "<br/>" +
			"Last Request from Air Conditioner: " + lr.String() + "<br/><br/>", true
	}
	return "Last Update from Air Conditioner: Never<br/><br/>", false
}

func compressorPayload(activity int, bOn bool) string {
	switch activity {
	case 0:
		return "heating"
	case 1:
		return "cooling"
	case 2:
		if bOn {
			return "idle"
		}
		return "off"
	default:
		return "off"
	}
}

// MQTTAvailability publishes online/offline to this unit's availability topic.
func (u *acUnit) MQTTAvailability() {
	payload := "offline"
	if u.IsOnline() {
		payload = "online"
	}
	u.mqtt.Publish(u.clientID+"/status", payload)
}

// StartPollWatcher logs a warning if no POST /data received for 5 minutes.
func (u *acUnit) StartPollWatcher(ctx interface{ Done() <-chan struct{} }) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		// 1 minute initial delay (spec §9)
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
		for {
			select {
			case <-ticker.C:
				u.dataMu.Lock()
				lu := u.data.dtLastUpdate
				u.dataMu.Unlock()
				if u.dataReceived && time.Since(lu) > pollWarnThreshold {
					log.Printf("WARNING: no POST /data from AC for unit %s in >5 minutes", u.name)
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// ParsePostData converts raw POST /data D=6 fields into acData.
func ParsePostData(
	compressorActivity int,
	errorCode string,
	fanIsCont int,
	fanSpeed int,
	isOn bool,
	isInESPMode bool,
	mode int,
	roomTemp float64,
	setPoint float64,
	bZones [8]bool,
	zoneTemps [8]float64,
) acData {
	return acData{
		iCompressorActivity: compressorActivity,
		strErrorCode:        errorCode,
		iFanContinuous:      fanIsCont,
		iFanSpeed:           fanSpeed,
		bOn:                 isOn,
		bESPOn:              isInESPMode,
		iMode:               mode,
		dblRoomTemperature:  roomTemp,
		dblSetTemperature:   setPoint,
		bZone:               bZones,
		dblZoneTemp:         zoneTemps,
		dtLastUpdate:        time.Now(),
	}
}

// PublishDiscovery sends HA auto-discovery messages for this unit.
// Both discovery and state topics use sequential topicNum (i+1) so HA entities
// and state updates are always in sync regardless of configured zone IDs.
func (u *acUnit) PublishDiscovery(registerZoneTemps bool) {
	clientID := u.clientID
	unit := u.name
	avail := clientID + "/status"

	deviceJSON := fmt.Sprintf(`{"identifiers":["%s"],"name":"Actron Air Conditioner","model":"Add-On","manufacturer":"ActronAir"}`, clientID)

	// Climate entity
	var climateTopic, climatePayload string
	if unit == "Default" {
		climateTopic = "homeassistant/climate/actronaircon/config"
		climatePayload = fmt.Sprintf(`{"name":"Air Conditioner","unique_id":"%s-AC","device":%s,"modes":["off","auto","cool","fan_only","heat"],"fan_modes":["high","medium","low"],"mode_command_topic":"actron/aircon/%s/mode/set","temperature_command_topic":"actron/aircon/%s/temperature/set","fan_mode_command_topic":"actron/aircon/%s/fan/set","min_temp":"12","max_temp":"30","temp_step":"0.5","fan_mode_state_topic":"actron/aircon/%s/fanmode","action_topic":"actron/aircon/%s/compressor","temperature_state_topic":"actron/aircon/%s/settemperature","mode_state_topic":"actron/aircon/%s/mode","current_temperature_topic":"actron/aircon/%s/temperature","availability_topic":"%s"}`,
			clientID, deviceJSON, unit, unit, unit, unit, unit, unit, unit, unit, avail)
	} else {
		climateTopic = "homeassistant/climate/actronaircon/" + unit + "/config"
		climatePayload = fmt.Sprintf(`{"name":"Air Conditioner %s","unique_id":"%s-AC","device":%s,"modes":["off","auto","cool","fan_only","heat"],"fan_modes":["high","medium","low"],"mode_command_topic":"actron/aircon/%s/mode/set","temperature_command_topic":"actron/aircon/%s/temperature/set","fan_mode_command_topic":"actron/aircon/%s/fan/set","min_temp":"12","max_temp":"30","temp_step":"0.5","fan_mode_state_topic":"actron/aircon/%s/fanmode","action_topic":"actron/aircon/%s/compressor","temperature_state_topic":"actron/aircon/%s/settemperature","mode_state_topic":"actron/aircon/%s/mode","current_temperature_topic":"actron/aircon/%s/temperature","availability_topic":"%s"}`,
			unit, clientID, deviceJSON, unit, unit, unit, unit, unit, unit, unit, unit, avail)
	}
	u.mqtt.Publish(climateTopic, climatePayload)

	// ESP sensor
	var espTopic, espPayload string
	if unit == "Default" {
		espTopic = "homeassistant/sensor/actron/esp/config"
		espPayload = fmt.Sprintf(`{"name":"Air Conditioner ESP","unique_id":"%s-AC-ESP","device":%s,"state_topic":"actron/aircon/%s/esp","availability_topic":"%s"}`,
			clientID, deviceJSON, unit, avail)
	} else {
		espTopic = "homeassistant/sensor/actron" + unit + "/esp/config"
		espPayload = fmt.Sprintf(`{"name":"Air Conditioner ESP","unique_id":"%s-AC-ESP","device":%s,"state_topic":"actron/aircon/%s/esp","availability_topic":"%s"}`,
			clientID, deviceJSON, unit, avail)
	}
	u.mqtt.Publish(espTopic, espPayload)

	// Fan continuous sensor
	var fanContTopic, fanContPayload string
	if unit == "Default" {
		fanContTopic = "homeassistant/sensor/actron/fancont/config"
		fanContPayload = fmt.Sprintf(`{"name":"Air Conditioner Fan Continuous","unique_id":"%s-AC-FANC","device":%s,"state_topic":"actron/aircon/%s/fancont","availability_topic":"%s"}`,
			clientID, deviceJSON, unit, avail)
	} else {
		fanContTopic = "homeassistant/sensor/actron" + unit + "/fancont/config"
		fanContPayload = fmt.Sprintf(`{"name":"Air Conditioner Fan Continuous","unique_id":"%s-AC-FANC","device":%s,"state_topic":"actron/aircon/%s/fancont","availability_topic":"%s"}`,
			clientID, deviceJSON, unit, avail)
	}
	u.mqtt.Publish(fanContTopic, fanContPayload)

	// Zone switches + zone temp sensors — topicNum = i+1, name from sorted zone slice
	for i, z := range u.zones {
		topicNum := i + 1
		zoneName := z.name

		var switchTopic, switchPayload string
		if unit == "Default" {
			switchTopic = fmt.Sprintf("homeassistant/switch/actron/airconzone%d/config", topicNum)
			switchPayload = fmt.Sprintf(`{"name":"%s Zone","unique_id":"%s-z%ds","device":%s,"state_topic":"actron/aircon/%s/zone%d","command_topic":"actron/aircon/%s/zone%d/set","payload_on":"ON","payload_off":"OFF","state_on":"ON","state_off":"OFF","availability_topic":"%s"}`,
				zoneName, clientID, topicNum, deviceJSON, unit, topicNum, unit, topicNum, avail)
		} else {
			switchTopic = fmt.Sprintf("homeassistant/switch/actron%s/airconzone%d/config", unit, topicNum)
			switchPayload = fmt.Sprintf(`{"name":"%s Zone","unique_id":"%s-z%ds","device":%s,"state_topic":"actron/aircon/%s/zone%d","command_topic":"actron/aircon/%s/zone%d/set","payload_on":"ON","payload_off":"OFF","state_on":"ON","state_off":"OFF","availability_topic":"%s"}`,
				zoneName, clientID, topicNum, deviceJSON, unit, topicNum, unit, topicNum, avail)
		}
		u.mqtt.Publish(switchTopic, switchPayload)

		var tempTopic, tempPayload string
		if unit == "Default" {
			tempTopic = fmt.Sprintf("homeassistant/sensor/actron/airconzone%d/config", topicNum)
		} else {
			tempTopic = fmt.Sprintf("homeassistant/sensor/actron%s/airconzone%d/config", unit, topicNum)
		}
		if registerZoneTemps {
			tempPayload = fmt.Sprintf(`{"name":"%s","unique_id":"%s-z%dt","device":%s,"state_topic":"actron/aircon/%s/zone%d/temperature","unit_of_measurement":"°C","availability_topic":"%s"}`,
				zoneName, clientID, topicNum, deviceJSON, unit, topicNum, avail)
		} else {
			tempPayload = "{}"
		}
		u.mqtt.Publish(tempTopic, tempPayload)
	}
}

// SubscribeCommands subscribes to all MQTT command topics for this unit.
// Zone subscriptions use sequential topicNum (i+1); callbacks carry the actual
// AC zone ID (z.id) for enabledZones indexing.
func (u *acUnit) SubscribeCommands(registry *Registry) {
	unit := u.name
	for i, z := range u.zones {
		topicNum := i + 1
		acID := z.id
		topic := fmt.Sprintf("actron/aircon/%s/zone%d/set", unit, topicNum)
		u.mqtt.Subscribe(topic, func(payload string) {
			u.changeZone(topicNum, acID, payload == "ON")
		})
	}
	u.mqtt.Subscribe("actron/aircon/"+unit+"/mode/set", func(payload string) {
		registry.HandleMode(unit, payload)
	})
	u.mqtt.Subscribe("actron/aircon/"+unit+"/fan/set", func(payload string) {
		registry.HandleFan(unit, payload)
	})
	u.mqtt.Subscribe("actron/aircon/"+unit+"/temperature/set", func(payload string) {
		if temp, err := strconv.ParseFloat(strings.TrimSpace(payload), 64); err == nil {
			registry.HandleTemperature(unit, temp)
		}
	})
}
