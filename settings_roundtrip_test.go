package main

import (
	"path/filepath"
	"testing"
)

// SaveSettings writes the full AppSettings; loadSettings must read every field
// back. The failure mode this pins: a load path that only knows the three
// original fields silently resets SafeMode/MCP settings on every restart.
func TestSettingsRoundTripAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	writer := &App{settingsPath: path}
	if err := writer.SaveSettings(AppSettings{
		ShowColumnComments: true,
		ResultLimit:        0, // explicit 0 = no limit, must survive
		Theme:              "dark",
		SafeMode:           "blockNoWhere",
		MCPAllowWrites:     true,
		MCPMaxRows:         250,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	reader := &App{settingsPath: path}
	reader.loadSettings()
	got := reader.GetSettings()
	switch {
	case !got.ShowColumnComments:
		t.Error("showColumnComments lost on reload")
	case got.ResultLimit != 0:
		t.Errorf("resultLimit = %d, want 0 (explicit no-limit)", got.ResultLimit)
	case got.Theme != "dark":
		t.Errorf("theme = %q, want dark", got.Theme)
	case got.SafeMode != "blockNoWhere":
		t.Errorf("safeMode = %q, want blockNoWhere", got.SafeMode)
	case !got.MCPAllowWrites:
		t.Error("mcpAllowWrites lost on reload")
	case got.MCPMaxRows != 250:
		t.Errorf("mcpMaxRows = %d, want 250", got.MCPMaxRows)
	}
}

// A settings file from before SafeMode/MCP existed must still load its known
// fields and fall back to defaults for the rest.
func TestSettingsLegacyFileFallsBackToDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := `{"showColumnComments": true, "resultLimit": 500, "theme": "light"}`
	if err := writeFileAtomic(path, []byte(legacy)); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	app := &App{settingsPath: path}
	app.loadSettings()
	got := app.GetSettings()
	switch {
	case !got.ShowColumnComments:
		t.Error("showColumnComments = false, want true")
	case got.ResultLimit != 500:
		t.Errorf("resultLimit = %d, want 500", got.ResultLimit)
	case got.Theme != "light":
		t.Errorf("theme = %q, want light", got.Theme)
	case got.SafeMode != safeModeOff:
		t.Errorf("safeMode = %q, want off (default)", got.SafeMode)
	case got.MCPAllowWrites:
		t.Error("mcpAllowWrites = true, want default off")
	}
}
