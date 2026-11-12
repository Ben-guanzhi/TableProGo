package main

import (
	"os"
	"strings"
	"testing"
)

// TestAgentSkillContentMatchesTheRealCLI is the point of generating the file
// instead of hand-writing it: every claim in SKILL.md must be true of this build.
// A skill that documents a flag which no longer exists teaches an agent to fail.
func TestAgentSkillContentMatchesTheRealCLI(t *testing.T) {
	body := agentSkillBody("tablepro-go")

	// The flags the CLI actually parses (main.go).
	for _, flag := range []string{"--job=", "--dry-run", "--mcp"} {
		if !strings.Contains(body, flag) {
			t.Fatalf("the skill does not mention %s", flag)
		}
	}
	// The exit-code table, which is the contract a scheduler reads.
	for _, code := range []string{"| 0 |", "| 1 |", "| 2 |"} {
		if !strings.Contains(body, code) {
			t.Fatalf("the exit-code table is missing %s", code)
		}
	}
	// The step kinds the recipe validator accepts.
	for _, kind := range []string{"backup", "transfer", "rowsync", "wait"} {
		if !strings.Contains(body, "`"+kind+"`") {
			t.Fatalf("the skill does not document the %q step", kind)
		}
	}
	// The credential rule: the one thing a reader must not get wrong.
	if !strings.Contains(body, "never passwords") {
		t.Fatal("the skill must state that recipes carry no credentials")
	}
	// The compact format is discoverable from the MCP section.
	if !strings.Contains(body, "compact") {
		t.Fatal("the skill should mention the compact output format")
	}
	// A valid skill frontmatter, or the agent will not index it.
	if !strings.HasPrefix(body, "---\nname: ") {
		t.Fatalf("the file does not start with skill frontmatter:\n%s", firstLines(body, 3))
	}
	if !strings.Contains(body, "description: ") {
		t.Fatal("the frontmatter has no description")
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

func TestWriteAgentSkillWritesUnderHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	path, err := WriteAgentSkill()
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	// The path is the one agents look at, not an app-private file.
	if !strings.Contains(path, ".agents") || !strings.HasSuffix(path, "SKILL.md") {
		t.Fatalf("path = %q", path)
	}
	if !strings.Contains(path, agentSkillName) {
		t.Fatalf("path = %q, want the skill name in it", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(data) < 500 {
		t.Fatalf("the skill file is suspiciously short: %d bytes", len(data))
	}
	if !strings.Contains(string(data), "--job=") {
		t.Fatal("the written file lost the recipe instructions")
	}
}

func TestWriteAgentSkillOverwritesAStaleCopy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	path, err := AgentSkillPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir+"/.agents/skills/"+agentSkillName, 0o755); err != nil {
		t.Fatal(err)
	}
	// A previous version's text, claiming a flag that does not exist.
	stale := "---\nname: tablepro-go\ndescription: old\n---\nRun --legacy-flag\n"
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := WriteAgentSkill(); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "--legacy-flag") {
		t.Fatal("the stale instruction survived: an agent would be taught a broken flag")
	}
}
