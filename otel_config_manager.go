package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

type OTelConfigManager struct {
	configPath string
	mu         sync.RWMutex
	
	currentConfig map[string]interface{}
}

func NewOTelConfigManager(configPath string) *OTelConfigManager {
	return &OTelConfigManager{
		configPath: configPath,
	}
}

// UpdateConfig applies new configuration to the OTel Collector
func (m *OTelConfigManager) UpdateConfig(payload string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	log.Printf("[ConfigManager] Received config update: %d bytes", len(payload))

	// Parse the payload - could be JSON or YAML
	var newConfig map[string]interface{}
	
	// Try JSON first
	if err := json.Unmarshal([]byte(payload), &newConfig); err != nil {
		// Try YAML
		if err := yaml.Unmarshal([]byte(payload), &newConfig); err != nil {
			return fmt.Errorf("failed to parse config: %v", err)
		}
	}

	// Load existing config
	existingConfig, err := m.loadConfig()
	if err != nil {
		log.Printf("[ConfigManager] Warning: Could not load existing config: %v", err)
		existingConfig = make(map[string]interface{})
	}

	// Merge configs - apply updates
	mergedConfig := m.mergeConfigs(existingConfig, newConfig)

	// Write updated config to file
	if err := m.writeConfig(mergedConfig); err != nil {
		return fmt.Errorf("failed to write config: %v", err)
	}

	m.currentConfig = mergedConfig
	log.Printf("[ConfigManager] Config file updated: %s", m.configPath)

	// In production, you would trigger OTel Collector reload here
	// For now, just log it
	log.Printf("[ConfigManager] OTel Collector should reload config now")

	return nil
}

func (m *OTelConfigManager) loadConfig() (map[string]interface{}, error) {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return nil, err
	}

	var config map[string]interface{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return config, nil
}

func (m *OTelConfigManager) writeConfig(config map[string]interface{}) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}

	// Write to temp file first, then rename (atomic operation)
	tempPath := m.configPath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return err
	}

	return os.Rename(tempPath, m.configPath)
}

func (m *OTelConfigManager) mergeConfigs(base, update map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Copy base
	for k, v := range base {
		result[k] = v
	}

	// Apply updates
	for k, v := range update {
		if existingVal, exists := result[k]; exists {
			// If both are maps, merge recursively
			if existingMap, ok := existingVal.(map[string]interface{}); ok {
				if updateMap, ok := v.(map[string]interface{}); ok {
					result[k] = m.mergeConfigs(existingMap, updateMap)
					continue
				}
			}
		}
		// Otherwise, overwrite
		result[k] = v
	}

	return result
}

func (m *OTelConfigManager) GetStatus() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := map[string]interface{}{
		"config_path": m.configPath,
		"has_config":  m.currentConfig != nil,
	}

	// Check if config file exists
	if _, err := os.Stat(m.configPath); err == nil {
		status["file_exists"] = true
		if info, err := os.Stat(m.configPath); err == nil {
			status["file_size"] = info.Size()
			status["modified"] = info.ModTime().Unix()
		}
	} else {
		status["file_exists"] = false
	}

	data, _ := json.Marshal(status)
	return string(data)
}
