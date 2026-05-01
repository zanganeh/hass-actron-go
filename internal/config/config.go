package config

import (
	"encoding/json"
	"os"
)

const ConfigPath = "/data/options.json"

type Zone struct {
	Name string `json:"Name"`
	ID   int    `json:"Id"`
	Unit string `json:"Unit"`
}

type Config struct {
	RegisterZoneTemperatures    bool     `json:"RegisterZoneTemperatures"`
	ForwardToOriginalWebService bool     `json:"ForwardToOriginalWebService"`
	MQTTLogs                    bool     `json:"MQTTLogs"`
	MQTTTLS                     bool     `json:"MQTTTLS"`
	MQTTBroker                  string   `json:"MQTTBroker"`
	MQTTUser                    string   `json:"MQTTUser"`
	MQTTPassword                string   `json:"MQTTPassword"`
	MultipleUnits               []string `json:"MultipleUnits"`
	Zones                       []Zone   `json:"Zones"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg Config
	if err := json.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}

	// Apply defaults
	if cfg.MQTTBroker == "" {
		cfg.MQTTBroker = "core-mosquitto"
	}
	// MQTTLogs default true — only override if field was absent (json bool defaults to false)
	// The spec says default=true but JSON bool zero is false. We handle this by
	// checking: if options.json omits MQTTLogs, we want true. Use a wrapper to detect.
	// Since we can't distinguish absent vs explicit false with encoding/json + plain bool,
	// we treat this as: default on (matches .NET behavior where missing = true).
	// If user wants false they must set it explicitly in options.json.
	// We use a pointer approach via raw unmarshal to detect absence.
	return applyPointerDefaults(path)
}

type rawConfig struct {
	RegisterZoneTemperatures    *bool    `json:"RegisterZoneTemperatures"`
	ForwardToOriginalWebService *bool    `json:"ForwardToOriginalWebService"`
	MQTTLogs                    *bool    `json:"MQTTLogs"`
	MQTTTLS                     *bool    `json:"MQTTTLS"`
	MQTTBroker                  string   `json:"MQTTBroker"`
	MQTTUser                    string   `json:"MQTTUser"`
	MQTTPassword                string   `json:"MQTTPassword"`
	MultipleUnits               []string `json:"MultipleUnits"`
	Zones                       []Zone   `json:"Zones"`
}

func applyPointerDefaults(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var raw rawConfig
	if err := json.NewDecoder(f).Decode(&raw); err != nil {
		return nil, err
	}

	cfg := &Config{
		MQTTBroker:    raw.MQTTBroker,
		MQTTUser:      raw.MQTTUser,
		MQTTPassword:  raw.MQTTPassword,
		MultipleUnits: raw.MultipleUnits,
		Zones:         raw.Zones,
	}

	if raw.RegisterZoneTemperatures != nil {
		cfg.RegisterZoneTemperatures = *raw.RegisterZoneTemperatures
	}
	if raw.ForwardToOriginalWebService != nil {
		cfg.ForwardToOriginalWebService = *raw.ForwardToOriginalWebService
	}
	if raw.MQTTLogs != nil {
		cfg.MQTTLogs = *raw.MQTTLogs
	} else {
		cfg.MQTTLogs = true // default true per spec
	}
	if raw.MQTTTLS != nil {
		cfg.MQTTTLS = *raw.MQTTTLS
	}
	if cfg.MQTTBroker == "" {
		cfg.MQTTBroker = "core-mosquitto"
	}

	return cfg, nil
}
