package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	requestTimeout = 5 * time.Second
	dnsCacheTTL    = 60 * time.Second
	dnsURL         = "https://dns.google.com/resolve?name=%s&type=A"
)

var (
	proxyLock      sync.Mutex
	cachedIP       string
	cacheExpiry    time.Time
	staticHTTPClient = &http.Client{Timeout: requestTimeout}
)

type dnsResponse struct {
	Answer []dnsAnswer `json:"Answer"`
}

type dnsAnswer struct {
	Type int    `json:"type"` // Google DNS returns integer; compare as int (fixes C# string "1" bug)
	Data string `json:"data"`
}

// ProxyResponse carries the upstream response.
type ProxyResponse struct {
	Successful  bool
	StatusCode  int
	Body        string
}

// getTargetIP resolves the AC cloud service IP via Google DNS with 60s cache.
func getTargetIP(host string) (string, error) {
	proxyLock.Lock()
	if cachedIP != "" && time.Now().Before(cacheExpiry) {
		ip := cachedIP
		proxyLock.Unlock()
		return ip, nil
	}
	proxyLock.Unlock()

	resp, err := staticHTTPClient.Get(fmt.Sprintf(dnsURL, host))
	if err != nil {
		return "", fmt.Errorf("DNS lookup failed: %w", err)
	}
	defer resp.Body.Close()

	var dns dnsResponse
	if err := json.NewDecoder(resp.Body).Decode(&dns); err != nil {
		return "", fmt.Errorf("DNS parse failed: %w", err)
	}

	for _, ans := range dns.Answer {
		if ans.Type == 1 { // A record — compare as int (plan §9: fixes C# string comparison bug)
			proxyLock.Lock()
			cachedIP = ans.Data
			cacheExpiry = time.Now().Add(dnsCacheTTL)
			proxyLock.Unlock()
			return ans.Data, nil
		}
	}
	return "", fmt.Errorf("no A record found for %s", host)
}

func makeProxiedClient(resolvedIP string) *http.Client {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s:80", resolvedIP))
	return &http.Client{
		Timeout:   requestTimeout,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}
}

// ForwardRequest proxies a GET or DELETE to the original AC cloud service.
func ForwardRequest(method, userAgent, host, path string) ProxyResponse {
	ip, err := getTargetIP(host)
	if err != nil {
		log.Printf("proxy DNS error: %v", err)
		return ProxyResponse{Successful: false}
	}

	targetURL := "http://" + host + path
	req, err := http.NewRequest(method, targetURL, nil)
	if err != nil {
		log.Printf("proxy request build error: %v", err)
		return ProxyResponse{Successful: false}
	}
	req.Header.Set("User-Agent", userAgent)

	client := makeProxiedClient(ip)
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("proxy request error: %v", err)
		return ProxyResponse{Successful: false}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	return ProxyResponse{
		Successful: true,
		StatusCode: resp.StatusCode,
		Body:       string(body),
	}
}

// ForwardData proxies a POST /data to the original AC cloud service (fire-and-forget).
func ForwardData(host, path, body, userAgent, contentType, ninjaToken string) {
	go func() {
		ip, err := getTargetIP(host)
		if err != nil {
			log.Printf("proxy ForwardData DNS error: %v", err)
			return
		}

		targetURL := "http://" + host + path
		req, err := http.NewRequest(http.MethodPost, targetURL, strings.NewReader(body))
		if err != nil {
			log.Printf("proxy ForwardData build error: %v", err)
			return
		}
		req.Header.Set("Connection", "close")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("X-Ninja-Token", ninjaToken)
		req.Header.Set("Content-Type", contentType)

		client := makeProxiedClient(ip)
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("proxy ForwardData error: %v", err)
			return
		}
		resp.Body.Close()
	}()
}

