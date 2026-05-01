package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"hass-actron/internal/ac"
	"hass-actron/internal/config"
	"hass-actron/internal/httpserver"
	mqttclient "hass-actron/internal/mqtt"
)

func main() {
	configPath := flag.String("config", config.ConfigPath, "path to options.json")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Printf("ERROR: failed to load config from %s: %v", *configPath, err)
		os.Exit(1)
	}

	// Start MQTT FIRST — ac.Configure sends discovery + subscribes (SPEC §1 CRITICAL ORDER)
	mqttCfg := mqttclient.Config{
		Broker:   cfg.MQTTBroker,
		User:     cfg.MQTTUser,
		Password: cfg.MQTTPassword,
		TLS:      cfg.MQTTTLS,
		Logs:     cfg.MQTTLogs,
	}
	mqttClient := mqttclient.NewClient(mqttCfg)
	mqttClient.Start()

	// Configure AC units (publishes HA discovery, subscribes topics)
	registry := ac.Configure(cfg, mqttClient)

	// Start background timers
	ctx, cancel := context.WithCancel(context.Background())
	registry.StartMQTTTimer(ctx)
	registry.StartPollWatchers(ctx)

	// Start HTTP server
	srv := httpserver.New(registry, cfg.ForwardToOriginalWebService)
	go func() {
		log.Printf("HTTP server listening on :180")
		if err := srv.ListenAndServe(); err != nil {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("Shutting down...")

	// Stop timers and goroutines
	cancel()

	// Publish offline before disconnecting (T11: 500ms sleep required)
	mqttClient.PublishOffline()
	time.Sleep(500 * time.Millisecond) // BLOCKER — flush retained message before disconnect

	mqttClient.Disconnect()

	// Graceful HTTP shutdown
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutCancel()
	srv.Shutdown(shutCtx)

	log.Printf("Shutdown complete")
}
