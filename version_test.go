package main

// W15: the version/update surface, tested without any network.
//
// The update check is opt-in and never runs on its own, so its whole contract is
// in these tests: no URL means "not checked" (not "up to date"), a broken document
// is an error the user can read, and the comparison is the simple dotted one the
// release tags actually use.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCurrentBuildReportsRealIdentity(t *testing.T) {
	info := CurrentBuild()
	// A plain `go build` has no version stamped in; "dev" is honest, "" is not.
	if info.Version == "" {
		t.Fatal("a build must name itself")
	}
	if info.Go == "" || info.OS == "" || info.Arch == "" {
		t.Fatalf("build info incomplete: %+v", info)
	}
}

func TestNewerThanComparesDottedVersions(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"1.2.0", "1.1.9", true},
		{"1.2.0", "1.2.0", false},
		{"1.1.9", "1.2.0", false},
		// A shorter version is compared as if padded with zeros.
		{"1.2", "1.2.0", false},
		{"1.2.1", "1.2", true},
		// A leading v is tolerated: release tags carry one, the file may not.
		{"v1.3.0", "1.2.0", true},
		// A pre-release is never newer than the release it precedes.
		{"1.2.0-rc1", "1.2.0", false},
		// A local dev build is always "older": no published version sits below it.
		{"1.0.0", "dev", true},
		{"dev", "dev", false},
		// Garbage does not crash or claim an update.
		{"", "1.0.0", false},
		{"abc", "1.0.0", false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := newerThan(c.candidate, c.current); got != c.want {
			t.Fatalf("newerThan(%q, %q) = %v, want %v", c.candidate, c.current, got, c.want)
		}
	}
}

func TestVersionParts(t *testing.T) {
	cases := map[string][]int{
		"1.2.3":       {1, 2, 3},
		"v1.2.3":      {1, 2, 3},
		"V2":          {2},
		"1.2.3-rc1":   {1, 2, 3},
		"1.2.3+build5": {1, 2, 3},
		"":            {},
		"dev":         {},
	}
	for in, want := range cases {
		got := versionParts(in)
		if len(got) != len(want) {
			t.Fatalf("versionParts(%q) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("versionParts(%q) = %v, want %v", in, got, want)
			}
		}
	}
}

func TestCheckForUpdateWithoutURLDoesNotCheck(t *testing.T) {
	// The default posture: nothing is fetched, and the answer says so instead of
	// pretending the build is current (which would render a silent "no update").
	res := CheckForUpdate("  ")
	if res.Checked {
		t.Fatal("an unconfigured check must not report as checked")
	}
	if res.Error != "" {
		t.Fatalf("not configured is not an error: %q", res.Error)
	}
	if res.Current == "" {
		t.Fatal("the current version is always reported")
	}
}

func TestCheckForUpdateReadsAHostedDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A GET with no body is all this feature is allowed to send.
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		if r.ContentLength > 0 {
			t.Errorf("the check must not send a body")
		}
		_, _ = w.Write([]byte(`{"version":"99.0.0","notes":"私有构建的新版本"}`))
	}))
	defer srv.Close()

	res := CheckForUpdate(srv.URL)
	if res.Error != "" {
		t.Fatalf("check failed: %s", res.Error)
	}
	if !res.Checked {
		t.Fatal("a successful check reports Checked")
	}
	if res.Newer != "99.0.0" {
		t.Fatalf("newer = %q", res.Newer)
	}
	if res.Notes == "" {
		t.Fatal("the release notes should reach the user")
	}
}

func TestCheckForUpdateReportsAFailureReadably(t *testing.T) {
	// A 404 must be visible, not silent: a user who pointed the URL at the wrong
	// path needs to know the check never worked.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if res := CheckForUpdate(srv.URL); !strings.Contains(res.Error, "404") {
		t.Fatalf("error = %q", res.Error)
	}

	// And a non-JSON body is named as such.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer bad.Close()
	if res := CheckForUpdate(bad.URL); !strings.Contains(res.Error, "JSON") {
		t.Fatalf("error = %q", res.Error)
	}
}

func TestCheckForUpdateIsSilentWhenCurrent(t *testing.T) {
	// A local `go build` is "dev", and by design ANY published version is newer than
	// a dev build — so this case needs a stamped version to mean anything.
	// Overriding the link-time variable is the same lever a release build uses.
	prev := buildVersion
	buildVersion = "2.0.0"
	t.Cleanup(func() { buildVersion = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.0.1","notes":"old"}`))
	}))
	defer srv.Close()
	res := CheckForUpdate(srv.URL)
	if res.Error != "" {
		t.Fatalf("error = %q", res.Error)
	}
	if res.Newer != "" {
		t.Fatalf("no update should be reported, got %q", res.Newer)
	}
	if !res.Checked {
		t.Fatal("the check did run")
	}
	if res.Current != "2.0.0" {
		t.Fatalf("current = %q", res.Current)
	}
}

func TestDevBuildSeesAnyReleaseAsNewer(t *testing.T) {
	// The counterpart, pinned so nobody "fixes" the dev rule into silence: a
	// developer running a local build SHOULD be told a release exists.
	prev := buildVersion
	buildVersion = "dev"
	t.Cleanup(func() { buildVersion = prev })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"0.0.1","notes":"first release"}`))
	}))
	defer srv.Close()
	if res := CheckForUpdate(srv.URL); res.Newer != "0.0.1" {
		t.Fatalf("newer = %q, want the release", res.Newer)
	}
}
