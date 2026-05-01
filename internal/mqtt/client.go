package mqtt

import (
	"crypto/tls"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

const (
	reconnectDelay  = 5 * time.Second
	serviceClientID = "hass-actron"
)

type subscription struct {
	topic   string
	handler func(string)
}

// Client wraps paho MQTT with QoS 2, retain, and resubscription on reconnect.
type Client struct {
	cfg    Config
	client paho.Client

	subsMu sync.Mutex
	subs   []subscription

	logEnabled bool
}

type Config struct {
	Broker   string
	User     string
	Password string
	TLS      bool
	Logs     bool
}

func NewClient(cfg Config) *Client {
	return &Client{cfg: cfg, logEnabled: cfg.Logs}
}

func (c *Client) Start() {
	opts := paho.NewClientOptions()

	host := c.cfg.Broker
	port := 0
	if idx := strings.Index(host, ":"); idx >= 0 {
		portStr := host[idx+1:]
		host = host[:idx]
		p, err := strconv.Atoi(portStr)
		if err != nil || p == 0 {
			log.Printf("MQTT invalid port in broker address %q: %v", c.cfg.Broker, err)
			return
		}
		port = p
	}

	var broker string
	if port > 0 {
		if c.cfg.TLS {
			broker = fmt.Sprintf("ssl://%s:%d", host, port)
		} else {
			broker = fmt.Sprintf("tcp://%s:%d", host, port)
		}
	} else {
		if c.cfg.TLS {
			broker = fmt.Sprintf("ssl://%s:8883", host)
		} else {
			broker = fmt.Sprintf("tcp://%s:1883", host)
		}
	}

	opts.AddBroker(broker)
	opts.SetClientID(serviceClientID)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(reconnectDelay)
	opts.SetCleanSession(true) // resubscribe on connect via OnConnect handler

	if c.cfg.User != "" {
		opts.SetUsername(c.cfg.User)
		opts.SetPassword(c.cfg.Password)
	}

	if c.cfg.TLS {
		opts.SetTLSConfig(&tls.Config{
			InsecureSkipVerify: true, // AllowUntrustedCertificates + IgnoreChainErrors
			MinVersion:         tls.VersionTLS12,
		})
	}

	opts.SetConnectionLostHandler(func(_ paho.Client, err error) {
		log.Printf("MQTT connection lost: %v", err)
	})

	opts.SetOnConnectHandler(func(_ paho.Client) {
		log.Printf("MQTT connected to %s", broker)
		c.resubscribeAll()
	})

	c.client = paho.NewClient(opts)

	// Connect asynchronously (fire-and-forget — matches StartMQTT async void)
	go func() {
		for {
			tok := c.client.Connect()
			tok.Wait()
			if tok.Error() == nil {
				return
			}
			log.Printf("MQTT connect error: %v", tok.Error())
			time.Sleep(reconnectDelay)
		}
	}()
}

// Publish sends a retained QoS-2 message.
func (c *Client) Publish(topic string, payload string) {
	if c.logEnabled {
		log.Printf("MQTT publish: %s", topic)
	}
	tok := c.client.Publish(topic, 1, true, payload)
	// Fire-and-forget: don't block on tok.Wait() to avoid stalling callers.
	// paho queues messages and delivers when connected.
	_ = tok
}

// Subscribe registers a topic handler and tracks it for resubscription on reconnect.
func (c *Client) Subscribe(topic string, handler func(string)) {
	c.subsMu.Lock()
	c.subs = append(c.subs, subscription{topic, handler})
	c.subsMu.Unlock()

	c.doSubscribe(topic, handler)
}

func (c *Client) doSubscribe(topic string, handler func(string)) {
	if c.logEnabled {
		log.Printf("MQTT subscribe: %s", topic)
	}
	tok := c.client.Subscribe(topic, 1, func(_ paho.Client, msg paho.Message) {
		payload := string(msg.Payload())
		handler(payload)
	})
	_ = tok
}

func (c *Client) resubscribeAll() {
	c.subsMu.Lock()
	subs := make([]subscription, len(c.subs))
	copy(subs, c.subs)
	c.subsMu.Unlock()

	for _, s := range subs {
		c.doSubscribe(s.topic, s.handler)
	}
}

// PublishOffline sends the service-level offline message.
// Caller must sleep 500ms after this before disconnecting (T11).
func (c *Client) PublishOffline() {
	c.client.Publish(serviceClientID+"/status", 1, true, "offline").Wait()
}

// Disconnect cleanly closes the MQTT connection.
func (c *Client) Disconnect() {
	c.client.Disconnect(500)
}
