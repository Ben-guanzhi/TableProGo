package main

// W13: the agent skill file.
//
// A shell-capable agent (Claude Code, Codex, OpenCode, …) discovers skills by
// reading ~/.agents/skills/<name>/SKILL.md. Writing that file from the app means
// the agent learns how to drive THIS binary — the job recipe, the exit codes, the
// MCP mode — from a document that travels with the installation instead of from a
// README the user has to find and paste into a prompt.
//
// The file is generated, not hand-maintained: the recipe shape and the exit-code
// table live in the same commit as the code that implements them, so a skill file
// that describes a flag which no longer exists cannot survive a release.

import (
	"os"
	"path/filepath"
	"strings"
)

// agentSkillName is the directory (and skill) name. Lower-case and stable: an
// agent may pin it in a config.
const agentSkillName = "tablepro-go"

// AgentSkillPath is where the skill file goes: the shared agent-skills directory
// under the user's home, which is where the CLI agents look.
func AgentSkillPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agents", "skills", agentSkillName, "SKILL.md"), nil
}

// WriteAgentSkill writes the skill file and returns its path.
//
// It overwrites: the file is a generated artifact describing this build, and a
// stale copy would tell an agent about flags that changed. A user who wants to
// keep their own notes should put them in a different skill.
func WriteAgentSkill() (string, error) {
	path, err := AgentSkillPath()
	if err != nil {
		return "", err
	}
	body := agentSkillBody(exeName())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// exeName is the binary's name as the skill file should spell it: argv[0]'s base
// if available, else the product name.
func exeName() string {
	if len(os.Args) > 0 && os.Args[0] != "" {
		base := filepath.Base(os.Args[0])
		// Strip the .exe so the examples read the same on every platform.
		base = strings.TrimSuffix(base, ".exe")
		if base != "" && !strings.HasPrefix(base, "go-build") {
			return base
		}
	}
	return "tablepro-go"
}

// agentSkillBody renders the SKILL.md content.
func agentSkillBody(exe string) string {
	return `---
name: ` + agentSkillName + `
description: Drive the ` + exe + ` database client from a shell — run scheduled jobs (backup / transfer / row-level sync), or expose its MCP server to an agent. Use when a task needs to move or compare data between databases headlessly.
---

# ` + exe + `

` + exe + ` is a desktop database client that also runs headless:
` + "`" + exe + " --job=<recipe.json>`" + ` executes a batch of database operations and exits, and
` + "`" + exe + " --mcp`" + ` serves its catalog and a read-only query surface over the
Model Context Protocol.

## Job recipes (the headless path)

A recipe is JSON naming saved connections BY ID — never credentials. Run it with:

    ` + exe + ` --job=/path/to/nightly.json

Validate without writing anything (transfers and syncs are analysed, not applied):

    ` + exe + ` --job=/path/to/nightly.json --dry-run

Shape:

    {
      "name": "nightly",
      "steps": [
        {"kind": "rowsync", "name": "report drift",
         "rowsync": {"source": {"connId": "prod"}, "target": {"connId": "staging"},
                     "table": "items"}},
        {"kind": "backup", "name": "dump prod",
         "backup": {"connId": "prod", "database": "shop", "mode": "full",
                    "outPath": "/backup/shop.sql"}},
        {"kind": "wait", "wait": {"seconds": 5}},
        {"kind": "transfer", "name": "copy to staging",
         "transfer": {"source": {"connId": "prod"}, "target": {"connId": "staging"}}},
        {"kind": "rowsync", "name": "sync rows",
         "rowsync": {"source": {"connId": "prod"}, "target": {"connId": "staging"},
                     "table": "items", "apply": true,
                     "ignoreColumns": ["updated_at"]}}
      ]
    }

Step kinds: ` + "`backup`" + ` (native dump tool), ` + "`transfer`" + ` (whole tables: structure +
data, in foreign-key dependency order), ` + "`rowsync`" + ` (row-level: INSERT/UPDATE/DELETE by
primary key so the target converges on the source), ` + "`wait`" + `.

` + "`rowsync`" + ` with ` + "`apply: false`" + ` (the default) only REPORTS drift: how many rows would
be inserted, updated or deleted. Drift is not a failure. ` + "`apply: true`" + ` runs the plan in
one transaction.

## Exit codes — read these before parsing output

| code | meaning | who should look |
|---|---|---|
| 0 | every step ran | nobody |
| 1 | a step failed (connection, schema drift, tool error) | the data owner |
| 2 | the recipe itself was unusable (missing file, bad JSON, unknown field, missing required key) | whoever wrote the recipe |

A recipe is validated BEFORE any connection opens, so a typo never looks like a
database failure. The per-step report goes to stdout as JSON; diagnostics go to
stderr and are redacted.

## MCP mode

    ` + exe + ` --mcp

Speaks JSON-RPC over stdio. Read-only by default: the tools are
` + "`list_connections`" + `, ` + "`list_schemas`" + `, ` + "`list_tables`" + `, ` + "`describe_table`" + `,
` + "`sample_table`" + ` and ` + "`query`" + `. ` + "`query`" + ` accepts ` + "`format: \"compact\"`" + ` for tab-separated
text instead of JSON — use it when a result set is large and the context budget
matters.

Result cells are data-masked before they leave the machine: sensitive column names
and PII shapes arrive as ` + "`a***@e***.com`" + `-style values, with a ` + "`masked`" + ` flag.

## Rules a job must respect

- The recipe carries connection ids, never passwords. Credentials stay in the
  app's own store; a migrated or imported connection has no password until
  someone types it once.
- A job has no UI, so the recipe IS the confirmation. Treat changing a recipe as
  changing a production permission.
- Every step stops the job on failure. Do not expect later steps to run after a
  failure.
- Nothing in a plan executes the statement: ` + "`EXPLAIN`" + ` and the transfer/rowsync
  previews are read-only.
`
}
