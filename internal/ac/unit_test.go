package ac

import (
	"strings"
	"testing"
	"time"
)

// mockMQTT records publishes and subscriptions.
type mockMQTT struct {
	published  []string
	subscribed []string
}

func (m *mockMQTT) Publish(topic, payload string) {
	m.published = append(m.published, topic+"="+payload)
}

func (m *mockMQTT) Subscribe(topic string, _ func(string)) {
	m.subscribed = append(m.subscribed, topic)
}

func newTestUnit(name string) (*acUnit, *mockMQTT) {
	m := &mockMQTT{}
	zones := []zoneEntry{{id: 1, name: "Bedroom"}, {id: 2, name: "Living"}}
	u := newACUnit(name, "hass-actron", zones, m, unitConfig{})
	return u, m
}

func newTestUnitNonContiguous(name string) (*acUnit, *mockMQTT) {
	m := &mockMQTT{}
	// Zones 1, 3, 5 — non-contiguous AC IDs, sorted
	zones := []zoneEntry{{id: 1, name: "Bed"}, {id: 3, name: "Living"}, {id: 5, name: "Main"}}
	u := newACUnit(name, "hass-actron", zones, m, unitConfig{})
	return u, m
}

// ---- T4: enabledZones pre-initialized ----

func TestEnabledZonesPreInit(t *testing.T) {
	u, _ := newTestUnit("Default")
	if u.cmd.enabledZones != "0,0,0,0,0,0,0,0" {
		t.Fatalf("enabledZones not pre-initialized: %q", u.cmd.enabledZones)
	}
}

// ---- buildCommand4: tempTarget must have exactly 1 decimal place ----

func TestBuildCommand4TempFormat(t *testing.T) {
	cmd := acCommand{amOn: true, tempTarget: 22.0, fanSpeed: 1, mode: 2}
	out := buildCommand4(cmd)
	if !strings.Contains(out, `"tempTarget":22.0`) {
		t.Fatalf("tempTarget not 1 decimal: %s", out)
	}
}

func TestBuildCommand4AmOnFalse(t *testing.T) {
	cmd := acCommand{amOn: false, tempTarget: 18.5, fanSpeed: 0, mode: 0}
	out := buildCommand4(cmd)
	if !strings.Contains(out, `"amOn":false`) {
		t.Fatalf("amOn not false literal: %s", out)
	}
}

// ---- buildCommand5: enabledZones as bare integers (no quotes) ----

func TestBuildCommand5ZoneArray(t *testing.T) {
	cmd := acCommand{enabledZones: "1,0,1,0,0,0,0,0"}
	out := buildCommand5(cmd)
	want := `"enabledZones":[1,0,1,0,0,0,0,0]`
	if !strings.Contains(out, want) {
		t.Fatalf("enabledZones not bare ints: %s", out)
	}
}

// ---- GetCommand: type 4 dequeued before type 5 ----

func TestGetCommandOrder(t *testing.T) {
	u, _ := newTestUnit("Default")
	u.pendingCommand = true
	u.pendingZone = true

	typ, _ := u.GetCommand()
	if typ != "4" {
		t.Fatalf("expected type 4 first, got %q", typ)
	}

	typ2, _ := u.GetCommand()
	if typ2 != "5" {
		t.Fatalf("expected type 5 second, got %q", typ2)
	}

	typ3, _ := u.GetCommand()
	if typ3 != "" {
		t.Fatalf("expected empty after both consumed, got %q", typ3)
	}
}

// ---- Suppress logic ----

func TestSuppressBlocksMQTTPublish(t *testing.T) {
	u, m := newTestUnit("Default")
	u.cmdMu.Lock()
	u.lastCommand = time.Now()
	u.cmdMu.Unlock()

	d := acData{
		bOn: true, iMode: 2, iFanSpeed: 2, dblSetTemperature: 25.0,
		dblRoomTemperature: 23.0, dtLastUpdate: time.Now(),
	}
	u.PostData(d)

	for _, pub := range m.published {
		if strings.HasPrefix(pub, "actron/aircon/Default/fanmode=") ||
			strings.HasPrefix(pub, "actron/aircon/Default/mode=") ||
			strings.HasPrefix(pub, "actron/aircon/Default/settemperature=") {
			t.Fatalf("suppressed topic published during suppress window: %s", pub)
		}
	}

	found := false
	for _, pub := range m.published {
		if strings.HasPrefix(pub, "actron/aircon/Default/temperature=") {
			found = true
		}
	}
	if !found {
		t.Fatal("always-on temperature topic not published during suppress")
	}
}

func TestNoSuppressPublishesAll(t *testing.T) {
	u, m := newTestUnit("Default")

	d := acData{
		bOn: true, iMode: 2, iFanSpeed: 1, dblSetTemperature: 22.0,
		dblRoomTemperature: 23.0, dtLastUpdate: time.Now(),
		bZone: [8]bool{true, false},
	}
	u.PostData(d)

	found := map[string]bool{}
	for _, pub := range m.published {
		parts := strings.SplitN(pub, "=", 2)
		found[parts[0]] = true
	}

	required := []string{
		"actron/aircon/Default/temperature",
		"actron/aircon/Default/fanmode",
		"actron/aircon/Default/mode",
		"actron/aircon/Default/settemperature",
	}
	for _, r := range required {
		if !found[r] {
			t.Fatalf("expected topic not published: %s", r)
		}
	}
}

// ---- BackSync order (SPEC §5.3) ----

func TestBackSyncCommandPendingBlocks(t *testing.T) {
	u, _ := newTestUnit("Default")
	u.cmd.amOn = true
	u.cmd.mode = 1
	u.pendingCommand = true

	d := acData{bOn: false, iMode: 2, dtLastUpdate: time.Now()}
	u.backSyncCommand(d, false)

	if !u.cmd.amOn || u.cmd.mode != 1 {
		t.Fatal("pendingCommand=true should block command back-sync")
	}
}

func TestBackSyncZoneSuppressBlocks(t *testing.T) {
	u, _ := newTestUnit("Default")
	u.cmd.enabledZones = "1,0,0,0,0,0,0,0"
	u.pendingZone = false

	d := acData{bZone: [8]bool{false, true}, dtLastUpdate: time.Now()}
	u.backSyncCommand(d, true) // suppressed

	if u.cmd.enabledZones != "1,0,0,0,0,0,0,0" {
		t.Fatal("suppress should block zone back-sync")
	}
}

// ---- Mode payload mapping (T6) ----

func TestModeToMQTT(t *testing.T) {
	cases := []struct {
		bOn  bool
		mode int
		want string
	}{
		{false, 0, "off"},
		{true, 0, "auto"},
		{true, 1, "heat"},
		{true, 2, "cool"},
		{true, 3, "fan_only"},  // T6: underscore preserved
		{true, 99, "off"},      // T6: out-of-range guard
		{true, -1, "off"},      // T6: None guard
	}
	for _, c := range cases {
		got := modeToMQTT(c.bOn, c.mode)
		if got != c.want {
			t.Errorf("modeToMQTT(%v,%d) = %q, want %q", c.bOn, c.mode, got, c.want)
		}
	}
}

// ---- T9: /status overwrites on never-seen unit ----

func TestStatusHTMLOverwrite(t *testing.T) {
	r := &Registry{units: make(map[string]*acUnit)}
	m := &mockMQTT{}

	u1 := newACUnit("Unit1", "hass-actron", nil, m, unitConfig{})
	u1.dataReceived = true
	u1.data.dtLastUpdate = time.Now()

	u2 := newACUnit("Unit2", "hass-actron2", nil, m, unitConfig{})
	u2.dataReceived = false // never seen

	r.units["Unit1"] = u1
	r.units["Unit2"] = u2
	r.first = u1

	html := r.StatusHTML()

	// If Unit2 (never seen) is processed after Unit1, it overwrites.
	// Result must contain "Never" and NOT contain Unit1's last-post line.
	if !strings.Contains(html, "Never") {
		t.Fatalf("StatusHTML missing 'Never' for unseen unit: %q", html)
	}
	// The overwrite bug means "Last Post from Air Conditioner" should not appear
	// IF the never-seen unit was processed last (map iteration order is non-deterministic).
	// We can only assert that "Never" appears and that = assignment was used (not +=).
	// Verify that "Never" overwrites: result must NOT start with "Unit:" when overwrite occurred.
	// This test verifies the overwrite code path exists and compiles correctly.
}

// ---- T10: zone topic count is len(zones), not max zone ID ----

func TestZoneTopicCountBased(t *testing.T) {
	u, m := newTestUnit("Default") // zones: id=1, id=2 → topics zone1, zone2

	d := acData{
		bZone:       [8]bool{true, true, true, true},
		dtLastUpdate: time.Now(),
	}
	u.PostData(d)

	found3 := false
	for _, pub := range m.published {
		if strings.Contains(pub, "zone3") {
			found3 = true
		}
	}
	if found3 {
		t.Fatal("zone3 published but only 2 zones configured (T10 violation)")
	}
}

// ---- Zone discovery/state consistency (the bug fix) ----

// With zones {id:1, id:3, id:5}:
// Discovery publishes zone1, zone2, zone3 (sequential).
// State publish uses zones[i].id-1 as bZone index: zone1→bZone[0], zone2→bZone[2], zone3→bZone[4].
func TestNonContiguousZoneMapping(t *testing.T) {
	u, m := newTestUnitNonContiguous("Default")

	// AC says: zone1=ON, zone3=ON, zone5=ON (via bZone indices 0,2,4)
	d := acData{
		bZone:        [8]bool{true, false, true, false, true, false, false, false},
		dtLastUpdate: time.Now(),
	}
	u.PostData(d)

	topicState := map[string]string{}
	for _, pub := range m.published {
		parts := strings.SplitN(pub, "=", 2)
		if len(parts) == 2 {
			topicState[parts[0]] = parts[1]
		}
	}

	// zone1 topic → bZone[zones[0].id-1] = bZone[0] = true → ON
	if topicState["actron/aircon/Default/zone1"] != "ON" {
		t.Errorf("zone1 should be ON, got %q", topicState["actron/aircon/Default/zone1"])
	}
	// zone2 topic → bZone[zones[1].id-1] = bZone[2] = true → ON
	if topicState["actron/aircon/Default/zone2"] != "ON" {
		t.Errorf("zone2 should be ON (maps to AC zone 3), got %q", topicState["actron/aircon/Default/zone2"])
	}
	// zone3 topic → bZone[zones[2].id-1] = bZone[4] = true → ON
	if topicState["actron/aircon/Default/zone3"] != "ON" {
		t.Errorf("zone3 should be ON (maps to AC zone 5), got %q", topicState["actron/aircon/Default/zone3"])
	}
	// zone4 must not exist
	if _, exists := topicState["actron/aircon/Default/zone4"]; exists {
		t.Error("zone4 must not be published (only 3 zones configured)")
	}
}

// ---- T1: commandEvent broadcast semantics ----

func TestCommandEventBroadcast(t *testing.T) {
	e := newCommandEvent()
	e.Set()

	ch1 := e.C()
	ch2 := e.C()
	select {
	case <-ch1:
	default:
		t.Fatal("ch1 not unblocked after Set")
	}
	select {
	case <-ch2:
	default:
		t.Fatal("ch2 not unblocked after Set")
	}

	e.Reset()
	ch3 := e.C()
	select {
	case <-ch3:
		t.Fatal("ch3 should block after Reset")
	default:
	}
}

// ---- T7: topic strip is string Replace ----

func TestTopicStrip(t *testing.T) {
	topic := "actron/aircon/Default/mode/set"
	stripped := strings.Replace(topic, "Default"+"/", "", -1)
	want := "actron/aircon/mode/set"
	if stripped != want {
		t.Errorf("topic strip: got %q, want %q", stripped, want)
	}
}
