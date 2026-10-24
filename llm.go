package main

// The AI assistant: a chat panel whose model reaches the database through the MCP
// tools (internal/mcp), never directly.
//
// The boundary matters: the model sees the same catalog the navigator shows, and
// every statement it runs goes through the same paths a user's statement does —
// Safe Mode, the row cap, the connection's own transaction. The model cannot open
// a connection the user did not open, and it cannot write without the same
// confirmation the user sees.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"tablepro-go/internal/llm"
	"tablepro-go/internal/mcp"
)

// llmSystemPrompt tells the model what it is and what it may do. It is a prompt,
// not a policy: the policy lives in the tools (read-only by default) and in Safe
// Mode. The prompt only makes the model use them well.
const llmSystemPrompt = `You are a database assistant inside a desktop SQL client.

You can look at the database through these tools:
- list_connections: the databases that are open right now
- list_schemas / list_tables / describe_table: the catalog
- query: run one read-only statement (SELECT/WITH/SHOW/DESCRIBE/EXPLAIN)

Rules:
- Look at the schema before you write SQL; do not guess table or column names.
- Use the connection id from list_connections.
- Answer in the user's language, briefly. Show the SQL you ran when it is useful.
- If the tools return an error, read it and correct yourself rather than repeating the same call.`

// llmConfigPath is where the provider settings live. They are in their own file
// with restrictive permissions: the API key must not sit in the shared settings
// file, and the file must not be world-readable.
func llmConfigPath() string { return userDataFilePath("llm.json") }

// llmStore keeps the provider configuration and the conversation history.
type llmStore struct {
	mu      sync.Mutex
	config  llm.Config
	history []llm.Message
	loaded  bool
}

// llmState is the process-wide assistant state.
var llmState = &llmStore{}

// LoadLLMConfig reads the provider settings, filling the blanks from the preset.
func LoadLLMConfig() llm.Config {
	llmState.mu.Lock()
	defer llmState.mu.Unlock()
	if !llmState.loaded {
		llmState.config = readLLMConfigFile()
		llmState.loaded = true
	}
	return llmState.config.Normalize()
}

// SaveLLMConfig writes the provider settings.
func SaveLLMConfig(cfg llm.Config) error {
	llmState.mu.Lock()
	defer llmState.mu.Unlock()
	if err := writeLLMConfigFile(cfg.Normalize()); err != nil {
		return err
	}
	llmState.config = cfg.Normalize()
	llmState.loaded = true
	return nil
}

// LLMPresets lists the providers for the settings page.
func LLMPresets() []llm.Preset { return llm.Presets }

// LLMHistory returns the conversation so far.
func LLMHistory() []llm.Message {
	llmState.mu.Lock()
	defer llmState.mu.Unlock()
	out := make([]llm.Message, len(llmState.history))
	copy(out, llmState.history)
	return out
}

// ClearLLMHistory forgets the conversation.
func ClearLLMHistory() {
	llmState.mu.Lock()
	defer llmState.mu.Unlock()
	llmState.history = nil
}

// AskLLM sends the question to the model and returns its answer.
//
// The model is given the MCP tools and a runner that executes them against this
// app's connections — the same ones the user has open. A question with no
// connection open is answered from the model's own knowledge, with the tools
// simply failing.
func (a *App) AskLLM(question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("ask something")
	}
	cfg := LoadLLMConfig()
	client := llm.New(cfg)

	llmState.mu.Lock()
	history := make([]llm.Message, len(llmState.history))
	copy(history, llmState.history)
	llmState.mu.Unlock()

	tools := a.llmTools()
	runner := a.llmRunner()
	resp, err := client.Ask(context.Background(), llmSystemPrompt, append(history, llm.Message{Role: "user", Content: question}), tools, runner)
	if err != nil {
		return "", err
	}

	llmState.mu.Lock()
	llmState.history = append(llmState.history,
		llm.Message{Role: "user", Content: question},
		llm.Message{Role: "assistant", Content: resp.Text},
	)
	// A long conversation would grow without bound; the last turns are enough for
	// a follow-up like "now by month".
	if len(llmState.history) > llmMaxHistoryTurns {
		llmState.history = llmState.history[len(llmState.history)-llmMaxHistoryTurns:]
	}
	llmState.mu.Unlock()

	return resp.Text, nil
}

// llmMaxHistoryTurns bounds the remembered conversation (user + assistant pairs).
const llmMaxHistoryTurns = 20

// llmTools returns the tools the model may call: the MCP catalog tools, read-only.
func (a *App) llmTools() []llm.Tool {
	server := a.mcpServer()
	// The tool list is what the MCP server advertises; asking it keeps the two in
	// step, so a tool the model sees is always a tool that exists.
	raw := server.ToolsForLLM()
	out := make([]llm.Tool, 0, len(raw)+1)
	for _, tool := range raw {
		out = append(out, llm.Tool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	// The propose tool is the assistant's own, not the MCP server's: the server is
	// for external clients, and its write path needs a confirmation the assistant
	// answers differently (the user, in this app's UI).
	if propose, ok := a.llmProposeTool(a.llmProposalAllowed()); ok {
		out = append(out, propose)
	}
	return out
}

// llmRunner executes a tool the model asked for.
//
// The write tool is deliberately absent: the model gets the read-only tools, so it
// cannot propose a statement that changes data. A user who wants a change asks for
// it in the editor, where Safe Mode and the review dialog apply.
func (a *App) llmRunner() llm.Runner {
	catalog := mcpCatalog{app: a}
	return func(ctx context.Context, name string, arguments map[string]interface{}) (string, error) {
		switch name {
		case "list_connections":
			return a.llmListConnections()
		case "list_schemas":
			connID, _ := arguments["connection"].(string)
			if connID == "" {
				return "", fmt.Errorf("the connection argument is required")
			}
			schemas, err := catalog.Schemas(connID)
			if err != nil {
				return "", err
			}
			return joinLines(schemas), nil
		case "list_tables":
			connID, _ := arguments["connection"].(string)
			if connID == "" {
				return "", fmt.Errorf("the connection argument is required")
			}
			database, _ := arguments["database"].(string)
			schema, _ := arguments["schema"].(string)
			tables, err := catalog.Tables(connID, database, schema)
			if err != nil {
				return "", err
			}
			names := make([]string, 0, len(tables))
			for _, t := range tables {
				names = append(names, t.Schema+"."+t.Name+" ("+t.Type+")")
			}
			return joinLines(names), nil
		case "describe_table":
			connID, _ := arguments["connection"].(string)
			table, _ := arguments["table"].(string)
			if connID == "" || table == "" {
				return "", fmt.Errorf("the connection and table arguments are required")
			}
			database, _ := arguments["database"].(string)
			schema, _ := arguments["schema"].(string)
			columns, err := catalog.Columns(connID, database, schema, table)
			if err != nil {
				return "", err
			}
			names := make([]string, 0, len(columns))
			for _, c := range columns {
				line := c.Name + " " + c.Type
				if c.IsPrimaryKey {
					line += " PRIMARY KEY"
				}
				if !c.Nullable {
					line += " NOT NULL"
				}
				names = append(names, line)
			}
			return joinLines(names), nil
		case "propose_statement":
			if !a.llmProposalAllowed() {
				return "", fmt.Errorf("the assistant may not propose statements: turn on “let the assistant write” in the settings")
			}
			return a.runProposeTool(arguments)
		case "query":
			connID, _ := arguments["connection"].(string)
			sql, _ := arguments["sql"].(string)
			if connID == "" || strings.TrimSpace(sql) == "" {
				return "", fmt.Errorf("the connection and sql arguments are required")
			}
			database, _ := arguments["database"].(string)
			result, err := catalog.Query(connID, database, sql, llmQueryRowCap)
			if err != nil {
				return "", err
			}
			return formatQueryResult(result), nil
		default:
			return "", fmt.Errorf("unknown tool %q", name)
		}
	}
}

// llmQueryRowCap is how many rows one tool call may bring back. The model reads
// samples, not dumps: a whole table would fill its context for nothing.
const llmQueryRowCap = 50

// llmListConnections describes the open connections for the model.
func (a *App) llmListConnections() (string, error) {
	conns := mcpCatalog{app: a}.Connections()
	if len(conns) == 0 {
		return "no database is open", nil
	}
	lines := make([]string, 0, len(conns))
	for _, c := range conns {
		lines = append(lines, fmt.Sprintf("id=%s name=%s engine=%s database=%s", c.ID, c.Name, c.Type, c.DBName))
	}
	return joinLines(lines), nil
}

// formatQueryResult renders rows as text the model can read.
func formatQueryResult(result *mcp.QueryResult) string {
	if result == nil || len(result.Rows) == 0 {
		return "(no rows)"
	}
	var b strings.Builder
	b.WriteString(strings.Join(result.Columns, " | "))
	b.WriteString("\n")
	for _, row := range result.Rows {
		cells := make([]string, len(row))
		for i, cell := range row {
			cells[i] = fmt.Sprintf("%v", cell)
		}
		b.WriteString(strings.Join(cells, " | "))
		b.WriteString("\n")
	}
	if result.Truncated {
		b.WriteString("(truncated)\n")
	}
	return b.String()
}

// joinLines renders a list for the model, one item per line.
func joinLines(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	return strings.Join(items, "\n")
}
