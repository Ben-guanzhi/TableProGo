package main

// W15 (private distribution): version identity and an opt-in update notice.
//
// There is deliberately NO WinGet/Scoop manifest and no release workflow that
// publishes anywhere: the mainline is not public. What a private build still needs
// is the ability to answer "which build is this?" and — if the owner hosts a
// version file somewhere they control — "is there a newer one?". Both are built
// around one rule: the app never phones home on its own. The update check runs
// only when a URL has been configured, and it sends nothing but a GET.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// Build identity. These are overridable at link time:
//
//	go build -ldflags "-X main.buildVersion=1.2.0 -X main.buildCommit=$(git rev-parse --short HEAD) -X main.buildDate=2026-11-12"
//
// The zero values are honest defaults for a local `go build`: "dev" and "unknown"
// say exactly what they mean instead of pretending to be a release.
var (
	buildVersion = "dev"
	buildCommit  = "unknown"
	buildDate    = "unknown"
)

// BuildInfo is what the About panel shows and the update check compares.
type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	// Go is the toolchain the binary was built with — the first thing to ask when
	// a bug will not reproduce.
	Go string `json:"go"`
	// OS/Arch explain why a "the same version" behaves differently on two machines.
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// CurrentBuild reports this binary's identity.
func CurrentBuild() BuildInfo {
	return BuildInfo{
		Version: buildVersion,
		Commit:  buildCommit,
		Date:    buildDate,
		Go:      runtime.Version(),
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
}

// UpdateCheck is the request and the answer for the opt-in update notice.
type UpdateCheck struct {
	// URL is a JSON document the owner hosts: {"version":"1.3.0","notes":"…"}.
	// Empty means the feature is off, which is the default.
	URL string `json:"url"`
	// Current, Newer and Notes are the result.
	Current string `json:"current"`
	Newer   string `json:"newer,omitempty"`
	Notes   string `json:"notes,omitempty"`
	// Checked is false when no URL was configured: the caller must not render an
	// update banner it did not actually check for.
	Checked bool   `json:"checked"`
	Error   string `json:"error,omitempty"`
}

// versionDoc is the shape of the hosted file.
type versionDoc struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
}

// CheckForUpdate reads the configured version document and compares.
//
// With no URL this returns Checked=false and no error: "not configured" is not a
// failure, and a UI that showed one would nag every user who never opted in.
func CheckForUpdate(url string) UpdateCheck {
	out := UpdateCheck{Current: buildVersion}
	url = strings.TrimSpace(url)
	if url == "" {
		return out
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		out.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		return out
	}
	var doc versionDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		out.Error = "版本文件不是合法 JSON: " + err.Error()
		return out
	}
	out.Checked = true
	if newerThan(doc.Version, buildVersion) {
		out.Newer = doc.Version
		out.Notes = doc.Notes
	}
	return out
}

// newerThan compares two dotted version numbers, ignoring any leading "v".
//
// Deliberately simple: a private build tags versions like 1.2.0 or 1.2.0-rc1, and
// a full semver implementation would be more machinery than the question needs.
// A non-numeric or shorter part counts as 0, so "1.2" < "1.2.1".
func newerThan(candidate, current string) bool {
	a := versionParts(candidate)
	b := versionParts(current)
	// A dev build is always "older": there is no published version below it, and
	// telling a developer their local build is newer than the release would be
	// noise.
	if strings.EqualFold(current, "dev") {
		return candidate != "" && !strings.EqualFold(candidate, "dev")
	}
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

// versionParts splits "v1.2.3-rc1" into [1,2,3], stopping at the first part that
// is not a number (a pre-release suffix never makes a version newer).
func versionParts(v string) []int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	// Drop any pre-release/build metadata.
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	out := []int{}
	for _, part := range strings.Split(v, ".") {
		n := 0
		got := false
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				break
			}
			n = n*10 + int(ch-'0')
			got = true
		}
		if !got {
			break
		}
		out = append(out, n)
	}
	return out
}
