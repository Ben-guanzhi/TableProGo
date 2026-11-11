package main

// W4-a: the HTTP face of internal/sshconfig. The parser lives in that package
// (pure, table-tested); this file only decides what the dialog sees.
//
// The shape returned is the frontend's SSHConfig, not the parser's Host: the
// dialog binds straight to these fields, and mapping here keeps the alias-vs-
// address distinction (Alias/HostName) from leaking into the UI model.

import (
	"log"

	"tablepro-go/internal/sshconfig"
)

// SSHResolveResult is what the dialog gets for one host alias.
type SSHResolveResult struct {
	// Found is false when the config has no block for the host; the dialog then
	// shows the alias unchanged and says so, instead of pretending a mapping
	// exists.
	Found     bool     `json:"found"`
	Alias     string   `json:"alias"`
	HostName  string   `json:"hostName"`
	User      string   `json:"user"`
	Port      int      `json:"port"`
	KeyFiles  []string `json:"keyFiles"`
	// Rejected and Warnings are surfaced, not swallowed: "why did my
	// ProxyJump not take effect" is answerable from the dialog.
	Rejected []sshconfig.Rejected `json:"rejected"`
	Warnings []string             `json:"warnings"`
	// Path is the file that answered, so the dialog can say where it looked.
	Path string `json:"path"`
}

// ResolveSSHConfig reads ~/.ssh/config and answers for one host.
func (a *App) ResolveSSHConfig(host string) (SSHResolveResult, error) {
	file, err := sshconfig.LoadDefault()
	if err != nil {
		return SSHResolveResult{}, err
	}
	h := file.Resolve(host)
	return SSHResolveResult{
		Found:    h.Pattern != "",
		Alias:    h.Alias,
		HostName: h.HostName,
		User:     h.User,
		Port:     h.Port,
		KeyFiles: h.IdentityFiles,
		Rejected: file.Rejected,
		Warnings: file.Warnings,
	}, nil
}

// SSHConfigSummary is the whole file, for a "pick an alias" list.
type SSHConfigSummary struct {
	Hosts    []SSHHostSummary       `json:"hosts"`
	Rejected []sshconfig.Rejected   `json:"rejected"`
	Warnings []string               `json:"warnings"`
}

// SSHHostSummary is one selectable alias.
type SSHHostSummary struct {
	Alias    string `json:"alias"`
	HostName string `json:"hostName"`
	User     string `json:"user"`
	Port     int    `json:"port"`
}

// LoadSSHConfig lists the config's hosts. Literal patterns are offered as
// aliases; wildcard patterns are skipped because they are rules, not hosts.
func (a *App) LoadSSHConfig() (SSHConfigSummary, error) {
	file, err := sshconfig.LoadDefault()
	if err != nil {
		return SSHConfigSummary{}, err
	}
	out := SSHConfigSummary{Rejected: file.Rejected, Warnings: file.Warnings}
	seen := map[string]bool{}
	for _, pattern := range file.Patterns() {
		if seen[pattern] {
			continue
		}
		seen[pattern] = true
		h := file.Resolve(pattern)
		out.Hosts = append(out.Hosts, SSHHostSummary{
			Alias:    pattern,
			HostName: h.HostName,
			User:     h.User,
			Port:     h.Port,
		})
	}
	log.Printf("ssh config: %d hosts, %d refused directives", len(out.Hosts), len(file.Rejected))
	return out, nil
}
