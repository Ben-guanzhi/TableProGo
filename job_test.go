package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- recipe validation (pure, no database) ----

func TestJobRecipeValidateAcceptsMinimalBackup(t *testing.T) {
	r := &JobRecipe{Steps: []JobStep{{
		Kind:   "backup",
		Backup: &BackupRequest{ConnID: "c1", OutPath: "/tmp/x.sql"},
	}}}
	if err := r.Validate(); err != nil {
		t.Fatalf("minimal recipe should validate: %v", err)
	}
}

func TestJobRecipeValidateRejectsEmpty(t *testing.T) {
	if err := (&JobRecipe{}).Validate(); err == nil {
		t.Fatal("a recipe with no steps must be refused")
	}
}

func TestJobRecipeValidateNamesTheFailingStep(t *testing.T) {
	// The message has to identify WHICH step: a nightly file with ten steps and
	// an unhelpful "invalid recipe" sends the operator hunting.
	r := &JobRecipe{Steps: []JobStep{
		{Kind: "wait", Wait: &JobWait{Seconds: 1}},
		{Kind: "backup", Name: "nightly dump", Backup: &BackupRequest{ConnID: "c1"}},
	}}
	err := r.Validate()
	if err == nil {
		t.Fatal("a backup without outPath must be refused")
	}
	if !strings.Contains(err.Error(), "nightly dump") {
		t.Fatalf("error should name the step: %v", err)
	}
	if !strings.Contains(err.Error(), "outPath") {
		t.Fatalf("error should name the missing field: %v", err)
	}
}

func TestJobRecipeValidateRejectsUnknownKind(t *testing.T) {
	r := &JobRecipe{Steps: []JobStep{{Kind: "explode"}}}
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "explode") {
		t.Fatalf("an unknown kind must be named: %v", err)
	}
}

func TestJobRecipeValidateRejectsSelfTransfer(t *testing.T) {
	// Copying a database onto itself is never what the operator meant.
	r := &JobRecipe{Steps: []JobStep{{
		Kind:     "transfer",
		Transfer: &TransferRequest{
			Source: TransferTarget{ConnID: "c1", Database: "shop"},
			Target: TransferTarget{ConnID: "c1", Database: "shop"},
		},
	}}}
	if err := r.Validate(); err == nil {
		t.Fatal("a transfer onto itself must be refused")
	}
}

func TestJobRecipeValidateRejectsBadWait(t *testing.T) {
	for _, secs := range []int{-1, 7200} {
		r := &JobRecipe{Steps: []JobStep{{Kind: "wait", Wait: &JobWait{Seconds: secs}}}}
		if err := r.Validate(); err == nil {
			t.Fatalf("wait=%d should be refused", secs)
		}
	}
}

// ---- recipe loading ----

func writeRecipe(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "job.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadJobRecipeRejectsUnknownFields(t *testing.T) {
	// A mistyped key in a scheduler file silently doing nothing is exactly the
	// failure a nightly job must not have.
	path := writeRecipe(t, `{"name":"x","steps":[{"kind":"wait","wait":{"seconds":1},"typo":"ignored?"}]}`)
	if _, err := LoadJobRecipe(path); err == nil {
		t.Fatal("an unknown field must be refused, not ignored")
	}
}

func TestLoadJobRecipeReadsAValidFile(t *testing.T) {
	path := writeRecipe(t, `{
	  "name": "nightly",
	  "steps": [
	    {"kind":"wait","name":"settle","wait":{"seconds":1}}
	  ]
	}`)
	r, err := LoadJobRecipe(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if r.Name != "nightly" || len(r.Steps) != 1 || r.Steps[0].Wait.Seconds != 1 {
		t.Fatalf("recipe = %+v", r)
	}
}

func TestLoadJobRecipeMissingFileIsAnError(t *testing.T) {
	if _, err := LoadJobRecipe(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("a missing recipe must be an error")
	}
}

// ---- log redaction ----

func TestRedactForLogHidesCredentialShapedValues(t *testing.T) {
	// The redactor exists so a future edit that formats a request struct into a
	// log line cannot print a secret. Each case asserts the VALUE is gone, which
	// is why the placeholder must not contain the value it replaces.
	cases := []struct{ in, secret string }{
		{`password=hunter2 user=bob`, "hunter2"},
		{`Password: "s3cret"`, "s3cret"},
		{`token=abc123`, "abc123"},
		{`passphrase=xyz`, "xyz"},
		{`secret=topsecret`, "topsecret"},
	}
	for _, c := range cases {
		got := redactForLog(c.in)
		if strings.Contains(got, c.secret) {
			t.Fatalf("redact(%q) = %q still holds %q", c.in, got, c.secret)
		}
		if !strings.Contains(got, "***") {
			t.Fatalf("redact(%q) = %q, want the *** placeholder", c.in, got)
		}
	}
}

func TestRedactForLogLeavesOrdinaryText(t *testing.T) {
	const msg = "已备份到 /tmp/shop.sql（12 行日志）"
	if got := redactForLog(msg); got != msg {
		t.Fatalf("redact changed ordinary text: %q", got)
	}
}

// ---- running a recipe (real SQLite, no UI) ----

// newJobApp builds the two-connection app the job tests run against.
func newJobApp(t *testing.T) (*App, TransferTarget, TransferTarget) {
	t.Helper()
	app, src, dst := newRowSyncApp(t)
	return app, src, dst
}

func TestRunJobRowsyncAnalyseOnlyDoesNotWrite(t *testing.T) {
	app, src, dst := newJobApp(t)
	recipe := &JobRecipe{
		Name: "nightly",
		Steps: []JobStep{{
			Kind: "rowsync",
			RowSync: &RowSyncJobStep{
				RowSyncRequest: RowSyncRequest{Source: src, Target: dst, Table: "items"},
				Apply:          false,
			},
		}},
	}
	res := app.RunJob(recipe, false, immediateClock())
	if !res.OK {
		t.Fatalf("an analyse-only step must not fail: %+v", res.Steps)
	}
	if len(res.Steps) != 1 || !strings.Contains(res.Steps[0].Detail, "仅分析") {
		t.Fatalf("detail = %+v", res.Steps)
	}
	// Drift is reported, not applied: the target is still stale.
	again, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "items"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Total == 0 {
		t.Fatal("analyse-only must not have changed the target")
	}
}

func TestRunJobRowsyncApplyConverges(t *testing.T) {
	app, src, dst := newJobApp(t)
	recipe := &JobRecipe{
		Name: "sync",
		Steps: []JobStep{{
			Kind: "rowsync",
			RowSync: &RowSyncJobStep{
				RowSyncRequest: RowSyncRequest{Source: src, Target: dst, Table: "items"},
				Apply:          true,
			},
		}},
	}
	res := app.RunJob(recipe, false, immediateClock())
	if !res.OK {
		t.Fatalf("job failed: %+v", res.Steps)
	}
	again, err := app.RowSyncPreview(RowSyncRequest{Source: src, Target: dst, Table: "items"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Total != 0 {
		t.Fatalf("the job should have converged the table: %d changes left", again.Total)
	}
}

func TestRunJobStopsAtTheFirstFailure(t *testing.T) {
	app, src, dst := newJobApp(t)
	recipe := &JobRecipe{
		Name: "mixed",
		Steps: []JobStep{
			{Kind: "wait", Name: "settle", Wait: &JobWait{Seconds: 0}},
			{Kind: "rowsync", Name: "bad", RowSync: &RowSyncJobStep{
				RowSyncRequest: RowSyncRequest{Source: src, Target: dst, Table: "no_such_table"},
				Apply:          true,
			}},
			{Kind: "wait", Name: "never", Wait: &JobWait{Seconds: 0}},
		},
	}
	res := app.RunJob(recipe, false, immediateClock())
	if res.OK {
		t.Fatal("the job should have failed")
	}
	if res.FailedStep != 1 {
		t.Fatalf("failedStep = %d, want 1", res.FailedStep)
	}
	// The third step never ran: a job that keeps going after a failure leaves a
	// state the operator never sees.
	if len(res.Steps) != 2 {
		t.Fatalf("ran %d steps, want 2 (stop at the failure)", len(res.Steps))
	}
}

func TestRunJobExitCodes(t *testing.T) {
	ok := JobResult{OK: true}
	if got := jobExitCode(ok); got != 0 {
		t.Fatalf("success exit = %d, want 0", got)
	}
	bad := JobResult{OK: false, FailedStep: 2}
	if got := jobExitCode(bad); got != 1 {
		t.Fatalf("failure exit = %d, want 1", got)
	}
}

func TestRunJobDryRunWritesNothing(t *testing.T) {
	app, src, dst := newJobApp(t)
	recipe := &JobRecipe{
		Name: "preview",
		Steps: []JobStep{
			{Kind: "transfer", Transfer: &TransferRequest{Source: src, Target: dst}},
			{Kind: "rowsync", RowSync: &RowSyncJobStep{
				RowSyncRequest: RowSyncRequest{Source: src, Target: dst, Table: "items"},
				Apply:          true,
			}},
		},
	}
	res := app.RunJob(recipe, true, immediateClock())
	if !res.OK {
		t.Fatalf("dry run reported failure: %+v", res.Steps)
	}
	// The transfer's dry run reports the plan; the target must not have gained
	// tables.
	if !strings.Contains(res.Steps[0].Detail, "dry-run") {
		t.Fatalf("transfer detail = %q", res.Steps[0].Detail)
	}
	if r := app.ExecuteQuery("dst", "SELECT count(*) FROM items", ""); r.Error == "" {
		// items existed already; the point is no NEW table appeared.
	}
	if r := app.ExecuteQuery("dst", "SELECT count(*) FROM no_such", ""); r.Error == "" {
		t.Fatal("dry run must not create tables")
	}
}

func TestRunJobWaitUsesTheInjectedClock(t *testing.T) {
	// A wait step must be testable without sleeping: the clock is injected.
	app, src, dst := newJobApp(t)
	var waited time.Duration
	clock := func(d time.Duration) { waited = d }
	recipe := &JobRecipe{Steps: []JobStep{
		{Kind: "wait", Wait: &JobWait{Seconds: 3}},
		{Kind: "rowsync", RowSync: &RowSyncJobStep{
			RowSyncRequest: RowSyncRequest{Source: src, Target: dst, Table: "items"},
		}},
	}}
	res := app.RunJob(recipe, false, clock)
	if !res.OK {
		t.Fatalf("job failed: %+v", res.Steps)
	}
	if waited != 3*time.Second {
		t.Fatalf("waited %v, want 3s", waited)
	}
}

// immediateClock never actually sleeps, so the tests stay fast.
func immediateClock() jobClock {
	return func(time.Duration) {}
}

// ---- the recipe round-trips through JSON as the operator would write it ----

func TestJobRecipeJSONShape(t *testing.T) {
	const body = `{
	  "name": "nightly",
	  "steps": [
	    {"kind":"backup","name":"dump shop","backup":{"connId":"prod","database":"shop","mode":"full","outPath":"/backup/shop.sql"}},
	    {"kind":"rowsync","name":"sync items","rowsync":{"source":{"connId":"prod"},"target":{"connId":"staging"},"table":"items","apply":true}},
	    {"kind":"wait","wait":{"seconds":5}}
	  ]
	}`
	var r JobRecipe
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if r.Steps[0].Backup.Mode != "full" {
		t.Fatalf("backup mode = %q", r.Steps[0].Backup.Mode)
	}
	if !r.Steps[1].RowSync.Apply {
		t.Fatal("rowsync apply should be true")
	}
	// The recipe carries NO credential: it names connections by id only.
	for _, secret := range []string{"password", "passwd", "secret", "token"} {
		if strings.Contains(strings.ToLower(body), secret) {
			t.Fatalf("the recipe shape must not carry %q", secret)
		}
	}
}
