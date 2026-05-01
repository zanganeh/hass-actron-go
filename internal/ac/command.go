package ac

import (
	"fmt"
	"strconv"
)

type acCommand struct {
	amOn         bool
	tempTarget   float64
	fanSpeed     int
	mode         int
	enabledZones string // 8-element CSV e.g. "1,0,0,0,0,0,0,0"
}

func newCommand() acCommand {
	return acCommand{enabledZones: "0,0,0,0,0,0,0,0"} // T4: pre-init, not nil
}

// buildCommand4 produces the type-4 (AC state) command JSON.
// tempTarget uses exactly 1 decimal place per spec (SPEC.md §3 Route 1 step 8).
func buildCommand4(cmd acCommand) string {
	amOn := "false"
	if cmd.amOn {
		amOn = "true"
	}
	temp := strconv.FormatFloat(cmd.tempTarget, 'f', 1, 64) // "22.0" never "22"
	return fmt.Sprintf(`{"DEVICE":[{"G":"0","V":2,"D":4,"DA":{"amOn":%s,"tempTarget":%s,"fanSpeed":%d,"mode":%d}}]}`,
		amOn, temp, cmd.fanSpeed, cmd.mode)
}

// buildCommand5 produces the type-5 (zone) command JSON.
// enabledZones CSV is injected directly → bare integers, no quotes.
func buildCommand5(cmd acCommand) string {
	return fmt.Sprintf(`{"DEVICE":[{"G":"0","V":2,"D":5,"DA":{"enabledZones":[%s]}}]}`,
		cmd.enabledZones)
}

// modeToMQTT maps iMode integer to MQTT mode payload string.
// Handles T6: None (-1) → "off", guard out-of-range.
func modeToMQTT(bOn bool, iMode int) string {
	if !bOn {
		return "off"
	}
	switch iMode {
	case 0:
		return "auto"
	case 1:
		return "heat"
	case 2:
		return "cool"
	case 3:
		return "fan_only" // T6: Fan_Only enum name → underscore preserved
	default:
		return "off"
	}
}

// fanSpeedToMQTT maps iFanSpeed integer to MQTT payload string.
func fanSpeedToMQTT(iFanSpeed int) string {
	switch iFanSpeed {
	case 0:
		return "low"
	case 1:
		return "medium"
	case 2:
		return "high"
	default:
		return "low"
	}
}
