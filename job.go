package main

// W7: scheduled batch jobs, the CLI half.
//
// The workspace's clients are "start on demand" apps: no daemon runs between
// sessions, so a UI scheduler would have to invent one. The shape that fits is
// therefore the opposite — a stateless command an operator points cron or Task
// Scheduler at, which reads a RECIPE file, runs it, and exits with a code the
// scheduler can act on:
//
//	tablepro-go --job nightly.json
//
// Everything the recipe can do is something the app already does interactively:
// a native-tool backup, a whole-table transfer (W5), a row-level sync (W6). The
// recipe is a JSON document naming saved connections BY ID — never credentials,
// never a password — so the file can live in a repo without leaking anything,
// and the passwords keep living in the connections store they were sealed into.
//
// Two semantic decisions the reader should know:
//
//   - The recipe IS the confirmation. A job has no UI to answer the app's
//     confirm dialogs, so steps that would ask are marked as confirmed by the
//     fact that an operator wrote them into a file and scheduled it. There is no
//     third-party input at run time; the risk is the file, not the run.
//   - A failed step stops the job. Continuing past a failure would leave a
//     half-applied state the operator never sees — the exit code is the only
//     signal a cron mail carries, so it has to mean "everything ran".

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// JobRecipe is one scheduled unit of work.
type JobRecipe struct {
	// Name is for logs and the exit summary only.
	Name string `json:"name"`
	// Steps run in order, stopping at the first failure.
	Steps []JobStep `json:"steps"`
}

// JobStep is one operation. Exactly one of the payload fields is set, selected
// by Kind.
type JobStep struct {
	// Kind is "backup" | "transfer" | "rowsync" | "wait".
	Kind string `json:"kind"`
	// Name labels the step in logs; optional.
	Name string `json:"name,omitempty"`

	Backup   *BackupRequest    `json:"backup,omitempty"`
	Transfer *TransferRequest  `json:"transfer,omitempty"`
	RowSync  *RowSyncJobStep   `json:"rowsync,omitempty"`
	// Wait pauses before the next step (a dump that must finish before the next
	// connection opens, or a fixed gap between two syncs of the same table).
	Wait *JobWait `json:"wait,omitempty"`
}

// RowSyncJobStep is a row-level sync with the apply decision made explicit.
type RowSyncJobStep struct {
	RowSyncRequest
	// Apply runs the plan. Without it the step only ANALYSES and reports the
	// counts — a scheduled job that reports drift without touching it is a
	// legitimate and much safer first deployment.
	Apply bool `json:"apply"`
	// Seqs limits the applied changes; empty with Apply means "every change the
	// plan found". A job that syncs everything nightly is the common case.
	Seqs []int `json:"seqs,omitempty"`
}

// JobWait pauses the job.
type JobWait struct {
	Seconds int `json:"seconds"`
}

// LoadJobRecipe reads and validates a recipe file.
//
// A missing or unreadable file is an error the caller turns into exit code 2
// (usage/config), distinct from exit code 1 (a step failed): a scheduler that
// conflates "the recipe is broken" with "the database changed" sends the wrong
// alert to the wrong person.
func LoadJobRecipe(path string) (*JobRecipe, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取作业配方: %w", err)
	}
	// A Windows editor (Notepad, Windows PowerShell's Set-Content) writes a
	// UTF-8 BOM, and encoding/json refuses it as an invalid first character.
	// Stripping it is the difference between "your file is fine" and a 3am
	// failure over three invisible bytes.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var recipe JobRecipe
	dec := json.NewDecoder(bytes.NewReader(data))
	// Unknown fields are refused rather than ignored: a mistyped key in a
	// scheduler file silently doing nothing is exactly the failure a nightly job
	// must not have.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&recipe); err != nil {
		return nil, fmt.Errorf("解析作业配方 %s: %w", path, err)
	}
	if err := recipe.Validate(); err != nil {
		return nil, fmt.Errorf("作业配方 %s: %w", path, err)
	}
	return &recipe, nil
}

// Validate checks the recipe's structure without touching a database, so a
// broken schedule fails at 3am with a message about the file rather than a
// half-run job.
func (r *JobRecipe) Validate() error {
	if len(r.Steps) == 0 {
		return fmt.Errorf("作业没有任何步骤")
	}
	for i, step := range r.Steps {
		label := step.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i+1)
		}
		switch step.Kind {
		case "backup":
			if step.Backup == nil {
				return fmt.Errorf("步骤 %s: kind=backup 但缺少 backup 配置", label)
			}
			if step.Backup.ConnID == "" {
				return fmt.Errorf("步骤 %s: 备份缺少 connId", label)
			}
			if step.Backup.OutPath == "" {
				return fmt.Errorf("步骤 %s: 备份缺少 outPath（写到哪里）", label)
			}
		case "transfer":
			if step.Transfer == nil {
				return fmt.Errorf("步骤 %s: kind=transfer 但缺少 transfer 配置", label)
			}
			if step.Transfer.Source.ConnID == "" || step.Transfer.Target.ConnID == "" {
				return fmt.Errorf("步骤 %s: 传输需要 source.connId 与 target.connId", label)
			}
			if step.Transfer.Source.ConnID == step.Transfer.Target.ConnID &&
				step.Transfer.Source.Database == step.Transfer.Target.Database &&
				step.Transfer.Source.Schema == step.Transfer.Target.Schema {
				return fmt.Errorf("步骤 %s: 传输的源与目标是同一个库", label)
			}
		case "rowsync":
			if step.RowSync == nil {
				return fmt.Errorf("步骤 %s: kind=rowsync 但缺少 rowsync 配置", label)
			}
			if step.RowSync.Source.ConnID == "" || step.RowSync.Target.ConnID == "" {
				return fmt.Errorf("步骤 %s: 行级同步需要 source.connId 与 target.connId", label)
			}
			if strings.TrimSpace(step.RowSync.Table) == "" {
				return fmt.Errorf("步骤 %s: 行级同步缺少 table", label)
			}
		case "wait":
			if step.Wait == nil {
				return fmt.Errorf("步骤 %s: kind=wait 但缺少 wait 配置", label)
			}
			if step.Wait.Seconds < 0 || step.Wait.Seconds > 3600 {
				return fmt.Errorf("步骤 %s: wait.seconds 必须在 0–3600 之间", label)
			}
		case "":
			return fmt.Errorf("步骤 %s: 缺少 kind（backup / transfer / rowsync / wait）", label)
		default:
			return fmt.Errorf("步骤 %s: 未知的 kind %q", label, step.Kind)
		}
	}
	return nil
}

// JobStepResult is one step's outcome.
type JobStepResult struct {
	Index  int    `json:"index"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// JobResult is the whole run's outcome.
type JobResult struct {
	Name    string           `json:"name"`
	Steps   []JobStepResult  `json:"steps"`
	OK      bool             `json:"ok"`
	Elapsed string           `json:"elapsed"`
	// FailedStep identifies where the run stopped (0-based); -1 when every step
	// ran. The exit code and the log both key off it.
	FailedStep int `json:"failedStep"`
}

// stepLabel is the display name for a step, defaulting to its kind.
func stepLabel(step JobStep, i int) string {
	if step.Name != "" {
		return step.Name
	}
	return fmt.Sprintf("%s #%d", step.Kind, i+1)
}

// redactForLog keeps a log line safe to mail: the recipe's connection ids and
// database names are fine, but nothing that looks like a credential may ride
// along.
//
// The scan finds a credential KEY, then consumes the separator run (`=`, `:`,
// spaces), an optional opening quote, and the value up to the next delimiter —
// including the closing quote. Stopping at the separator would leave
// `Password: "s3cret"` intact, which is exactly the shape a struct dump
// produces.
//
// This is a blunt filter rather than a parser on purpose: it runs on strings
// this file builds, and its job is to make sure a future edit that formats a
// request struct into a log line cannot print a secret.
func redactForLog(s string) string {
	keys := []string{"password", "passwd", "pwd", "passphrase", "secret", "token"}
	var out strings.Builder
	out.Grow(len(s))
	i := 0
	for i < len(s) {
		lower := strings.ToLower(s[i:])
		hit := ""
		hitAt := -1
		for _, key := range keys {
			if at := strings.Index(lower, key); at >= 0 && (hitAt < 0 || at < hitAt) {
				hit, hitAt = key, at
			}
		}
		if hitAt < 0 {
			out.WriteString(s[i:])
			break
		}
		// Everything before the key is safe.
		out.WriteString(s[i : i+hitAt])
		out.WriteString(s[i+hitAt : i+hitAt+len(hit)])
		pos := i + hitAt + len(hit)

		// Separator run: `=`, `:`, spaces, and at most one opening quote.
		for pos < len(s) && (s[pos] == '=' || s[pos] == ':' || s[pos] == ' ' || s[pos] == '\t') {
			pos++
		}
		if pos < len(s) && (s[pos] == '"' || s[pos] == '\'') {
			pos++
		}
		// The value runs to the next delimiter.
		for pos < len(s) && !strings.ContainsRune(" \t\n\",'", rune(s[pos])) {
			pos++
		}
		out.WriteString("***")
		i = pos
	}
	return out.String()
}

// jobClock is the wait implementation, injectable so tests do not sleep.
type jobClock func(d time.Duration)

// defaultJobClock is the real one.
var defaultJobClock jobClock = func(d time.Duration) { time.Sleep(d) }
