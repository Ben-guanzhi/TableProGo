package main

// W10: the plugin panel's backend — list what is declared, say whether each
// executable still matches what was vouched for, and let the user trust one.
//
// The trust gate itself lives in internal/pluginreg and runs at startup
// (plugins.go Load). This file is the read/write face: it reports the same
// three states the gate acts on, so the panel can never show "trusted" for a file
// the gate would refuse.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"tablepro-go/internal/pluginreg"
)

// PluginInfo is one row of the plugins panel.
type PluginInfo struct {
	// Type is the engine the plugin provides.
	Type string `json:"type"`
	// Name is the display name (falls back to the type).
	Name string `json:"name"`
	// Path is the absolute executable path.
	Path string `json:"path"`
	// Version and Description come from the registry, when it declared the row.
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	// Source says where the row came from: "registry" or "env".
	Source string `json:"source"`
	// State is trusted / unknown / tampered.
	State string `json:"state"`
	// Detail is the one-line explanation (same text the log gets).
	Detail string `json:"detail"`
	// Expected / Actual are the digests, so the panel can show both sides of a
	// tamper.
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	// Enabled reports whether the plugin is currently registered for use.
	Enabled bool `json:"enabled"`
	// Exists is false when the declared path is gone; the panel shows that
	// instead of pretending the plugin works.
	Exists bool `json:"exists"`
}

// PluginPanel is the whole panel's data.
type PluginPanel struct {
	Plugins []PluginInfo `json:"plugins"`
	// RegistryPath is where the registry was read from (empty when none).
	RegistryPath string `json:"registryPath"`
	// TrustPath is the trust store's location, shown so a user can back it up.
	TrustPath string `json:"trustPath"`
	Warnings  []string `json:"warnings"`
}

// ListPlugins reports every declared plugin with its trust state.
//
// The registry and the environment are merged: a registry entry gives the user a
// name and description, an env declaration is the dev shortcut that already
// exists. When both name the same engine the registry row wins the display —
// it has the metadata — but the env path is what startup actually registered.
func (a *App) ListPlugins() (PluginPanel, error) {
	panel := PluginPanel{
		RegistryPath: pluginRegistryPath(),
		TrustPath:    userDataFilePath("plugin_trust.json"),
	}
	store, err := pluginreg.LoadTrustStore(panel.TrustPath)
	if err != nil {
		// A broken trust file means nothing can be verified as trusted; say so and
		// continue with an empty store, which downgrades everything to "unknown"
		// rather than showing a stale "trusted" that startup would not honour.
		panel.Warnings = append(panel.Warnings, fmt.Sprintf("信任清单不可读（全部按未信任显示）: %v", err))
		store = &pluginreg.TrustStore{}
	}
	trusted := pluginreg.TrustedMap(store)

	reg, regErr := pluginreg.LoadRegistry(panel.RegistryPath)
	if regErr != nil {
		panel.Warnings = append(panel.Warnings, fmt.Sprintf("注册表不可读: %v", regErr))
		reg = &pluginreg.Registry{Plugins: nil}
	}

	// What startup actually registered, which is the environment's view.
	plugins.mu.RLock()
	envSpecs := make(map[string]pluginSpec, len(plugins.specs))
	for typ, spec := range plugins.specs {
		envSpecs[typ] = spec
	}
	plugins.mu.RUnlock()

	// One row per engine type. The registry supplies the display metadata; the
	// environment supplies the path that is actually in use. Merging by type
	// keeps the panel from showing two rows for one plugin, which would make
	// "which one is running" unanswerable.
	rows := map[string]*PluginInfo{}
	order := make([]string, 0, len(reg.Plugins)+len(envSpecs))

	for _, e := range reg.Plugins {
		if _, seen := rows[e.Type]; seen {
			continue
		}
		info := &PluginInfo{
			Type: e.Type, Name: e.Name, Version: e.Version,
			Description: e.Description, Source: "registry", Path: e.Path,
		}
		if info.Name == "" {
			info.Name = e.Type
		}
		info.Exists = pathExists(e.Path)
		rows[e.Type] = info
		order = append(order, e.Type)
	}
	for typ, spec := range envSpecs {
		info, seen := rows[typ]
		if !seen {
			info = &PluginInfo{Type: typ, Name: typ}
			rows[typ] = info
			order = append(order, typ)
		}
		// The env declaration is what launched: its path wins, and the source is
		// recorded so the panel can say where the row came from.
		info.Path = spec.Path
		info.Source = "env"
		info.Enabled = true
		info.Exists = pathExists(spec.Path)
	}

	// Now compute the trust state for every row, from the file on disk.
	for _, typ := range order {
		info := rows[typ]
		entry := findRegistryEntry(reg, typ)
		if !info.Exists {
			info.State = string(pluginreg.StateUnknown)
			info.Detail = "可执行文件不存在"
			continue
		}
		check, verr := pluginreg.Verify(info.Path, entry.SHA256, trusted, typ)
		if verr != nil {
			info.State = string(pluginreg.StateTampered)
			info.Detail = verr.Error()
			continue
		}
		info.State = string(check.State)
		info.Detail = check.Detail()
		info.Path = check.Path
		info.Expected = check.Expected
		info.Actual = check.Actual
	}

	// A registry row that was never enabled is not "in use"; say so rather than
	// letting the panel imply it is registered.
	for _, typ := range order {
		info := rows[typ]
		if info.Source == "registry" && !findRegistryEntry(reg, typ).Enabled {
			info.Detail += "（注册表中未启用）"
		}
	}

	panel.Plugins = make([]PluginInfo, 0, len(order))
	for _, typ := range order {
		panel.Plugins = append(panel.Plugins, *rows[typ])
	}
	sort.Slice(panel.Plugins, func(i, j int) bool {
		if panel.Plugins[i].Type != panel.Plugins[j].Type {
			return panel.Plugins[i].Type < panel.Plugins[j].Type
		}
		return panel.Plugins[i].Source < panel.Plugins[j].Source
	})
	return panel, nil
}

// TrustPlugin records one executable's current digest, so the next startup will
// accept it. It refuses to trust a file that does not exist — vouching for a
// missing path would produce a trust entry that can never match.
func (a *App) TrustPlugin(path, pluginType string) (PluginInfo, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil || strings.TrimSpace(path) == "" {
		return PluginInfo{}, fmt.Errorf("插件路径无效")
	}
	if _, err := os.Stat(abs); err != nil {
		return PluginInfo{}, fmt.Errorf("插件文件不存在: %w", err)
	}
	store, err := pluginreg.LoadTrustStore(userDataFilePath("plugin_trust.json"))
	if err != nil {
		return PluginInfo{}, err
	}
	entry, err := pluginreg.Trust(store, abs, pluginType)
	if err != nil {
		return PluginInfo{}, err
	}
	if err := savePluginTrustStore(store); err != nil {
		return PluginInfo{}, fmt.Errorf("保存信任清单: %w", err)
	}
	// Echo the state the gate would now see, so the panel updates without a
	// second round trip.
	check, verr := pluginreg.Verify(abs, "", pluginreg.TrustedMap(store), pluginType)
	info := PluginInfo{
		Type: pluginType, Name: pluginType, Path: abs,
		State: string(pluginreg.StateTrusted), Enabled: true, Exists: true,
		Actual: entry.SHA256, Expected: entry.SHA256,
		Detail: "已加入信任清单",
	}
	if verr == nil {
		info.State = string(check.State)
		info.Detail = check.Detail()
	}
	return info, nil
}

// UntrustPlugin removes a path from the store. The plugin keeps working for this
// session (it is already registered); the next startup sees it as "unknown",
// which is a warning rather than a refusal.
func (a *App) UntrustPlugin(path string) error {
	store, err := pluginreg.LoadTrustStore(userDataFilePath("plugin_trust.json"))
	if err != nil {
		return err
	}
	if !pluginreg.Untrust(store, path) {
		return fmt.Errorf("该路径不在信任清单中")
	}
	return savePluginTrustStore(store)
}

// ensureDefaultPluginRegistry writes a starter registry the first time the panel
// opens, so a user has a file to edit rather than a blank panel with no
// explanation of the format.
func (a *App) ensureDefaultPluginRegistry() error {
	path := pluginRegistryPath()
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	reg := &pluginreg.Registry{
		Schema: pluginreg.RegistrySchema,
		Plugins: []pluginreg.Entry{{
			Type:        "example",
			Name:        "示例插件（把 path 换成你的插件可执行文件）",
			Path:        "plugins/example.exe",
			Description: "这个条目是格式示例：path 相对注册表所在目录；sha256 可选，填了就按摘要校验",
			Enabled:     false,
		}},
	}
	return pluginreg.SaveRegistry(path, reg)
}

// pluginRegistryPath is where the registry lives: next to the other state files,
// so "the plugins this installation knows" travels with the profile.
func pluginRegistryPath() string {
	return userDataFilePath("plugin_registry.json")
}

// pathExists reports whether a declared executable is present.
func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// findRegistryEntry returns the registry row for a type (zero value when absent).
func findRegistryEntry(reg *pluginreg.Registry, typ string) pluginreg.Entry {
	for _, e := range reg.Plugins {
		if e.Type == typ {
			return e
		}
	}
	return pluginreg.Entry{}
}
