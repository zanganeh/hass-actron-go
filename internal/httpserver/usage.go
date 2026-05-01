package httpserver

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
)

// Route 5: POST /usage/log
func (h *handler) usageLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Headers", "X-Requested-With")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Content-Type", "application/json")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("POST /usage/log read error: %v", err)
		goto Cleanup
	}
	{
		var parsed map[string]interface{}
		if err := json.Unmarshal(body, &parsed); err != nil {
			log.Printf("POST /usage/log parse error: %v", err)
		} else {
			mode, _ := parsed["mode"]
			method, _ := parsed["method"]
			log.Printf("usage log: mode=%v method=%v", mode, method)
		}
	}
Cleanup:
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":200,"message":"Usage tracked","value":null}`))
}
