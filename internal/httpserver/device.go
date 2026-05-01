package httpserver

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"hass-actron/internal/ac"
	"hass-actron/internal/proxy"
)

// Route 1: GET /rest/{version}/block/{device}/commands (long-poll)
func (h *handler) commands(w http.ResponseWriter, r *http.Request) {
	device := r.PathValue("device")
	log.Printf("GET /commands from %s device=%s", r.RemoteAddr, device)
	setCORSHeaders(w)

	h.registry.UpdateRequestTime(device)

	// Capture event channel BEFORE select (T1 — closed channel semantics)
	eventCh := h.registry.EventC(device)

	select {
	case <-eventCh:
		// command pending — fall through
	case <-time.After(10 * time.Second):
		// timeout — return empty 200
		w.WriteHeader(http.StatusOK)
		log.Printf("GET /commands timeout for %s", r.RemoteAddr)
		return
	case <-r.Context().Done():
		return
	}

	cmdType, payload := h.registry.GetCommand(device)
	if cmdType != "4" && cmdType != "5" {
		w.WriteHeader(http.StatusOK)
		log.Printf("GET /commands no command type for %s", r.RemoteAddr)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(payload))
	log.Printf("GET /commands sent type=%s to %s", cmdType, r.RemoteAddr)
}

// Route 2: POST /rest/{version}/block/{device}/data
func (h *handler) data(w http.ResponseWriter, r *http.Request) {
	device := r.PathValue("device")
	setCORSHeaders(w)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("POST /data read error: %v", err)
	}
	strData := string(body)

	// Forward before parse if configured (fire-and-forget)
	if h.forwardToOriginal {
		userAgent := r.Header.Get("User-Agent")
		contentType := r.Header.Get("Content-Type")
		ninjaToken := r.Header.Get("X-Ninja-Token")
		host := r.Host
		proxy.ForwardData(host, r.URL.RequestURI(), strData, userAgent, contentType, ninjaToken)
	}

	if err == nil {
		h.parseAndPostData(device, strData)
	}

	// Always return the same response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"result":1,"error":null,"id":0}`))
}

type dataHeader struct {
	D int `json:"D"`
}

type dataHeader6 struct {
	DA map[string]json.RawMessage `json:"DA"`
}

func (h *handler) parseAndPostData(device, strData string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("POST /data parse panic: %v", r)
		}
	}()

	var hdr dataHeader
	if err := json.Unmarshal([]byte(strData), &hdr); err != nil {
		log.Printf("POST /data header parse error: %v", err)
		return
	}

	if hdr.D != 6 {
		return // silently ignore non-D6 messages
	}

	var hdr6 dataHeader6
	if err := json.Unmarshal([]byte(strData), &hdr6); err != nil {
		log.Printf("POST /data D6 parse error: %v", err)
		return
	}

	da := hdr6.DA

	var (
		compressorActivity int
		errorCode          string
		fanIsCont          int
		fanSpeed           int
		isOn               bool
		isInESPMode        bool
		mode               int
		roomTemp           float64
		setPoint           float64
		bZones             [8]bool
		zoneTemps          [8]float64
	)

	if v, ok := da["compressorActivity"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			compressorActivity, _ = strconv.Atoi(s)
		} else {
			json.Unmarshal(v, &compressorActivity)
		}
	}
	if v, ok := da["errorCode"]; ok {
		json.Unmarshal(v, &errorCode)
	}
	if v, ok := da["fanIsCont"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			fanIsCont, _ = strconv.Atoi(s)
		} else {
			json.Unmarshal(v, &fanIsCont)
		}
	}
	if v, ok := da["fanSpeed"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			fanSpeed, _ = strconv.Atoi(s)
		} else {
			json.Unmarshal(v, &fanSpeed)
		}
	}
	if v, ok := da["isOn"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			isOn, _ = strconv.ParseBool(s)
		} else {
			json.Unmarshal(v, &isOn)
		}
	}
	if v, ok := da["isInESP_Mode"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			isInESPMode, _ = strconv.ParseBool(s)
		} else {
			json.Unmarshal(v, &isInESPMode)
		}
	}
	if v, ok := da["mode"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			mode, _ = strconv.Atoi(s)
		} else {
			json.Unmarshal(v, &mode)
		}
	}
	if v, ok := da["roomTemp_oC"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			roomTemp, _ = strconv.ParseFloat(s, 64)
		} else {
			json.Unmarshal(v, &roomTemp)
		}
	}
	if v, ok := da["setPoint"]; ok {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			setPoint, _ = strconv.ParseFloat(s, 64)
		} else {
			json.Unmarshal(v, &setPoint)
		}
	}

	// enabledZones: array of ints/nulls
	if v, ok := da["enabledZones"]; ok {
		var zones []json.RawMessage
		if err := json.Unmarshal(v, &zones); err == nil {
			for i := 0; i < 8 && i < len(zones); i++ {
				var n int
				if json.Unmarshal(zones[i], &n) == nil {
					bZones[i] = n == 1
				}
			}
		}
	}

	// individualZoneTemperatures_oC: guard uses enabledZones nulls (SPEC §3 Route 2 step 6)
	var enabledZonesRaw []json.RawMessage
	if v, ok := da["enabledZones"]; ok {
		json.Unmarshal(v, &enabledZonesRaw)
	}
	if v, ok := da["individualZoneTemperatures_oC"]; ok {
		var temps []json.RawMessage
		if err := json.Unmarshal(v, &temps); err == nil {
			for i := 0; i < 8 && i < len(temps); i++ {
				// Guard: check enabledZones[i] for JSON null (not the temp array)
				if i < len(enabledZonesRaw) && string(enabledZonesRaw[i]) == "null" {
					continue
				}
				var f float64
				if json.Unmarshal(temps[i], &f) == nil {
					zoneTemps[i] = f
				}
			}
		}
	}

	d := ac.ParsePostData(compressorActivity, errorCode, fanIsCont, fanSpeed,
		isOn, isInESPMode, mode, roomTemp, setPoint, bZones, zoneTemps)
	h.registry.PostData(device, d)
}

// Route 3: GET /rest/{version}/block/{device}/activate (proxy)
func (h *handler) activate(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	device := r.PathValue("device")
	userAgent := r.Header.Get("User-Agent")
	host := r.Host // hostname only per spec
	if idx := colonIndex(host); idx >= 0 {
		host = host[:idx]
	}

	token := r.URL.Query().Get("user_access_token")
	path := "/rest/" + version + "/block/" + device + "/activate?user_access_token=" + token

	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Expires", "-1")

	result := proxy.ForwardRequest("GET", userAgent, host, path)
	if result.Successful {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(result.StatusCode)
		w.Write([]byte(result.Body))
	} else {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

// Route 4: DELETE /rest/{version}/block/{device} (proxy)
func (h *handler) deleteBlock(w http.ResponseWriter, r *http.Request) {
	version := r.PathValue("version")
	device := r.PathValue("device")
	userAgent := r.Header.Get("User-Agent")
	host := r.Host
	if idx := colonIndex(host); idx >= 0 {
		host = host[:idx]
	}

	token := r.URL.Query().Get("user_access_token")
	path := "/rest/" + version + "/block/" + device + "?user_access_token=" + token

	result := proxy.ForwardRequest("DELETE", userAgent, host, path)
	if result.Successful {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(result.StatusCode)
		w.Write([]byte(result.Body))
	} else {
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func colonIndex(s string) int {
	for i, c := range s {
		if c == ':' {
			return i
		}
	}
	return -1
}
