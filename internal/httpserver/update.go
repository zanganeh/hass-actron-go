package httpserver

import (
	"fmt"
	"log"
	"net/http"
)

// Route 6: GET /v0/AConnect
func (h *handler) aconnect(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	serial := q.Get("serial")
	mac := q.Get("mac")
	reboots := q.Get("reboots")
	uptimeMins := q.Get("uptime_mins")
	bootloader := q.Get("bootloader")
	wifi := q.Get("wifi")
	flashFam := q.Get("flash_fam")
	version := q.Get("version")
	msg := q.Get("msg")

	log.Printf("AConnect: serial=%s mac=%s reboots=%s uptime=%s bootloader=%s wifi=%s flash=%s version=%s msg=%s",
		serial, mac, reboots, uptimeMins, bootloader, wifi, flashFam, version, msg)

	w.Header().Set("Access-Control-Allow-Headers", "X-Requested-With")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "download=\nMessageLogged=%s:%s", msg, version)
}
