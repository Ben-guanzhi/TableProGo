package main

// The AI assistant, end to end: a fake provider asks for tools, the tools run
// against a real database, and the answer comes back.
//
// The point is the boundary: the model reaches the database only through the MCP
// tools, so it sees exactly what the user sees — and it cannot write.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"

	"tablepro-go/internal/llm"
)

// fakeChat is a chat provider that answers with a scripted list of tool calls and
// then a final text.
type fakeChat struct {
	toolCalls []string
	final     string
	requests  []map[string]interface{}
}

func (f *fakeChat) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.requests = append(f.requests, body)
		if len(f.toolCalls) > 0 {
			call := f.toolCalls[0]
			f.toolCalls = f.toolCalls[1:]
			fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[%s]},"finish_reason":"tool_calls"}]}`, call)
			return
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, f.final)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// toolCallJSON builds one OpenAI-shaped tool call.
func toolCallJSON(id, name, args string) string {
	return fmt.Sprintf(`{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}`, id, name, args)
}

func TestLLMReachesTheDatabaseThroughTheTools(t *testing.T) {
	conn := sqliteDB(t, "llm1", "scratch")
	createPartsTable(t, conn)
	if _, err := conn.DB.Exec(`INSERT INTO parts (id, name) VALUES (1, 'gear')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	a := searchApp(conn)

	chat := &fakeChat{
		toolCalls: []string{
			toolCallJSON("c1", "list_connections", `{}`),
			toolCallJSON("c2", "list_tables", `{"connection":"llm1"}`),
			toolCallJSON("c3", "describe_table", `{"connection":"llm1","table":"parts"}`),
			toolCallJSON("c4", "query", `{"connection":"llm1","sql":"SELECT name FROM parts"}`),
		},
		final: "the parts table has one row: gear",
	}
	srv := chat.server(t)

	if err := SaveLLMConfig(llm.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m", Enabled: true}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	ClearLLMHistory()

	answer, err := a.AskLLM("what is in parts?")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !strings.Contains(answer, "gear") {
		t.Fatalf("answer = %q", answer)
	}
	// Four tool calls plus the final answer: five round trips.
	if len(chat.requests) != 5 {
		t.Fatalf("requests = %d", len(chat.requests))
	}
	// The tool output reached the model: the request that follows the query call
	// carries the row.
	joined := fmt.Sprintf("%v", chat.requests[4]["messages"])
	if !strings.Contains(joined, "gear") {
		t.Fatalf("the row was not sent back to the model: %s", joined)
	}
}

// The model must not be able to write: the assistant's tools are the read-only
// ones, and a write tool name is refused.
func TestLLMHasNoWriteTool(t *testing.T) {
	conn := sqliteDB(t, "llm2", "scratch")
	createPartsTable(t, conn)
	a := searchApp(conn)

	tools := a.llmTools()
	for _, tool := range tools {
		if tool.Name == "execute" {
			t.Fatal("the assistant must not be offered the write tool")
		}
	}
	// Calling it by hand is refused too.
	runner := a.llmRunner()
	if _, err := runner(context.Background(), "execute", map[string]interface{}{"connection": "llm2", "sql": "DELETE FROM parts"}); err == nil {
		t.Fatal("the write tool must be refused")
	}
}

// A query tool call goes through the same statement path as a user's, so Safe Mode
// and the row cap apply.
func TestLLMQueryIsCapped(t *testing.T) {
	conn := sqliteDB(t, "llm3", "scratch")
	createPartsTable(t, conn)
	for i := 0; i < 100; i++ {
		if _, err := conn.DB.Exec(`INSERT INTO parts (id, name) VALUES (?, ?)`, i, fmt.Sprintf("p%d", i)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	a := searchApp(conn)

	runner := a.llmRunner()
	out, err := runner(context.Background(), "query", map[string]interface{}{
		"connection": "llm3",
		"sql":        "SELECT id, name FROM parts",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	// The cap is the assistant's, not the user's: 50 rows, not the result limit.
	// The header line and the truncation note are not rows.
	rows := 0
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" || line == "(truncated)" || strings.Contains(line, " | ") && rows == 0 {
			continue
		}
		rows++
	}
	if rows > llmQueryRowCap {
		t.Fatalf("the tool returned %d rows, the cap is %d", rows, llmQueryRowCap)
	}
	if !strings.Contains(out, "truncated") {
		t.Fatalf("the truncation must be visible to the model: %q", out)
	}
}

// A question with no database open is still answered: the tools simply report that
// nothing is connected.
func TestLLMWithNoConnections(t *testing.T) {
	a := searchApp()
	chat := &fakeChat{
		toolCalls: []string{toolCallJSON("c1", "list_connections", `{}`)},
		final:     "nothing is open",
	}
	srv := chat.server(t)
	if err := SaveLLMConfig(llm.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m", Enabled: true}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	answer, err := a.AskLLM("what can you see?")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if answer != "nothing is open" {
		t.Fatalf("answer = %q", answer)
	}
	// The model was told nothing is open.
	joined := fmt.Sprintf("%v", chat.requests[1]["messages"])
	if !strings.Contains(joined, "no database is open") {
		t.Fatalf("the model was not told: %s", joined)
	}
}

// The history is remembered, so a follow-up has context.
func TestLLMRemembersTheConversation(t *testing.T) {
	conn := sqliteDB(t, "llm4", "scratch")
	a := searchApp(conn)
	chat := &fakeChat{final: "first answer"}
	srv := chat.server(t)
	if err := SaveLLMConfig(llm.Config{Provider: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m", Enabled: true}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	ClearLLMHistory()

	if _, err := a.AskLLM("one"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if _, err := a.AskLLM("two"); err != nil {
		t.Fatalf("ask: %v", err)
	}
	history := LLMHistory()
	if len(history) != 4 {
		t.Fatalf("history = %+v", history)
	}
	// The second request carries the first exchange.
	joined := fmt.Sprintf("%v", chat.requests[1]["messages"])
	if !strings.Contains(joined, "one") {
		t.Fatalf("the follow-up has no context: %s", joined)
	}
}

// The provider settings are written to their own file with owner-only permissions.
func TestLLMConfigFileIsPrivate(t *testing.T) {
	if err := SaveLLMConfig(llm.Config{Provider: "openai", APIKey: "secret", Model: "m", Enabled: true}); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(llmConfigPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Windows has no POSIX permission bits: Go reports 0666 for a file whose ACL
	// allows writing, whatever mode the caller passed. There the separation is the
	// file itself, not its mode.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatalf("the settings file is readable by others: %v", info.Mode().Perm())
	}
	// And it is not in the shared settings file.
	shared, err := os.ReadFile(settingsFilePath())
	if err == nil && strings.Contains(string(shared), "secret") {
		t.Fatal("the API key must not end up in the shared settings file")
	}
}
