package main

// The AI assistant's settings file. It is separate from the shared settings
// because it holds an API key: the file is written with owner-only permissions,
// and it is never sent anywhere except to the provider it is addressed to.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"tablepro-go/internal/llm"
)

// readLLMConfigFile loads the provider settings, tolerating a missing or damaged
// file: a broken settings file must not keep the app from starting.
func readLLMConfigFile() llm.Config {
	var cfg llm.Config
	data, err := os.ReadFile(llmConfigPath())
	if err != nil {
		return llm.Config{}
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return llm.Config{}
	}
	return cfg
}

// writeLLMConfigFile saves the provider settings with owner-only permissions.
//
// On Windows the permission bits are advisory, so the file is additionally kept
// out of the shared settings file and never included in an export; on Unix the
// mode is what actually protects it.
func writeLLMConfigFile(cfg llm.Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the settings: %w", err)
	}
	path := llmConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create the settings directory: %w", err)
	}
	// Write to a temporary file and rename it: a crash mid-write must not leave a
	// half-written file that no longer parses (and so silently drops the key).
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write the settings: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace the settings: %w", err)
	}
	return nil
}
