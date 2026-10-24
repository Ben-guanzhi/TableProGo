// Package llm talks to a chat model so the user can ask about their database in
// plain language.
//
// The model is not given the database: it is given the MCP tools from
// internal/mcp, and it reaches the schema through them. That keeps the same
// boundary the MCP server enforces — the model reads the catalog and runs
// statements through the same audited, capped, Safe-Mode-checked paths.
//
// Providers are addressed through one small interface so adding an engine is one
// function, not a branch in the request path.

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is one turn of the conversation.
type Message struct {
	Role    string `json:"role"` // "user" | "assistant"
	Content string `json:"content"`
}

// Config is the user's provider settings. The API key never leaves this process
// except to the provider it is addressed to.
type Config struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"baseURL"`
	APIKey   string `json:"apiKey"`
	Model    string `json:"model"`
	Enabled  bool   `json:"enabled"`
	// AllowProposals lets the assistant propose statements that change data. The
	// proposal waits for the user, who sees the statement and decides; the
	// assistant never runs one itself.
	AllowProposals bool `json:"allowProposals"`
}

// Preset is one provider's defaults, so the settings page can offer a choice
// without the user typing a base URL.
type Preset struct {
	ID             string
	Label          string
	DefaultBaseURL string
	DefaultModel   string
	// RequiresKey is false for local runtimes (Ollama) that need no secret.
	RequiresKey bool
}

// Presets are the supported providers, in the order the settings page shows them.
var Presets = []Preset{
	{ID: "openai", Label: "OpenAI", DefaultBaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-4o-mini", RequiresKey: true},
	{ID: "deepseek", Label: "DeepSeek", DefaultBaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat", RequiresKey: true},
	{ID: "ollama", Label: "Ollama (local)", DefaultBaseURL: "http://localhost:11434/v1", DefaultModel: "llama3", RequiresKey: false},
	{ID: "anthropic", Label: "Anthropic (Claude)", DefaultBaseURL: "https://api.anthropic.com/v1", DefaultModel: "claude-3-5-sonnet-latest", RequiresKey: true},
	{ID: "google", Label: "Google (Gemini)", DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta", DefaultModel: "gemini-2.0-flash", RequiresKey: true},
	{ID: "custom", Label: "Custom (OpenAI-compatible)", DefaultModel: "", RequiresKey: true},
}

// PresetByID finds a provider's defaults.
func PresetByID(id string) (Preset, bool) {
	for _, p := range Presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// Normalize fills the blanks from the provider's preset without overwriting what
// the user has chosen.
func (c Config) Normalize() Config {
	if c.Provider == "" {
		c.Provider = "openai"
	}
	if p, ok := PresetByID(c.Provider); ok {
		if c.BaseURL == "" {
			c.BaseURL = p.DefaultBaseURL
		}
		if c.Model == "" {
			c.Model = p.DefaultModel
		}
	}
	return c
}

// Validate reports why the configuration cannot be used, or nil.
func (c Config) Validate() error {
	if !c.Enabled {
		return fmt.Errorf("the AI assistant is off: turn it on and set an API key in the settings")
	}
	preset, _ := PresetByID(c.Provider)
	if preset.RequiresKey && strings.TrimSpace(c.APIKey) == "" {
		return fmt.Errorf("no API key is configured for %s", preset.Label)
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return fmt.Errorf("no base URL is configured")
	}
	if strings.TrimSpace(c.Model) == "" {
		return fmt.Errorf("no model is configured")
	}
	return nil
}

// Tool is a callable the model may use. The shape matches MCP's tool definition,
// so the same list serves both the chat and the MCP server.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// ToolCall is a request from the model to run a tool.
type ToolCall struct {
	ID        string                 `json:"id"`
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// Response is one answer from the model.
type Response struct {
	Text      string
	ToolCalls []ToolCall
	// StopReason says why the model stopped: "end", "tool_calls" or "max_tokens".
	StopReason string
}

// Runner executes a tool the model asked for and returns the result as text. It
// is the client's job: the model never touches the database directly.
type Runner func(ctx context.Context, name string, arguments map[string]interface{}) (string, error)

// Client is one configured chat backend.
type Client struct {
	cfg  Config
	http *http.Client
}

// New returns a client for the configuration.
func New(cfg Config) *Client {
	return &Client{
		cfg: cfg.Normalize(),
		// A generous but finite timeout: a local model can be slow, and a hung
		// request must not hold the UI forever.
		http: &http.Client{Timeout: 120 * time.Second},
	}
}

// Config returns the client's configuration.
func (c *Client) Config() Config { return c.cfg }

// Ask sends the conversation and returns the model's answer. When the model asks
// for tools, they are run through runner and the results are sent back, up to
// maxRounds times — a model that keeps asking for tools must not loop forever.
func (c *Client) Ask(ctx context.Context, system string, history []Message, tools []Tool, runner Runner) (Response, error) {
	if err := c.cfg.Validate(); err != nil {
		return Response{}, err
	}
	if runner == nil {
		runner = func(context.Context, string, map[string]interface{}) (string, error) {
			return "", fmt.Errorf("this client has no tool runner")
		}
	}

	messages := normalizeHistory(history)
	conversation := append([]Message{}, messages...)

	for round := 0; round < maxToolRounds; round++ {
		resp, err := c.complete(ctx, system, conversation, tools)
		if err != nil {
			return Response{}, err
		}
		if len(resp.ToolCalls) == 0 {
			return resp, nil
		}
		// The model wants to look at something: run the tools and answer again.
		conversation = append(conversation, Message{Role: "assistant", Content: resp.Text})
		for _, call := range resp.ToolCalls {
			out, err := runner(ctx, call.Name, call.Arguments)
			if err != nil {
				// A failed tool is information for the model, not a dead end: it
				// can correct itself and try another way.
				out = "tool error: " + err.Error()
			}
			conversation = append(conversation, Message{
				Role:    "user",
				Content: fmt.Sprintf("tool %s returned:\n%s", call.Name, out),
			})
		}
	}
	return Response{}, fmt.Errorf("the model kept asking for tools after %d rounds", maxToolRounds)
}

// maxToolRounds bounds the tool loop.
const maxToolRounds = 8

// normalizeHistory cleans the conversation: empty turns are dropped, unknown roles
// are discarded, and two consecutive turns by the same speaker are merged (several
// providers reject those).
func normalizeHistory(history []Message) []Message {
	out := make([]Message, 0, len(history))
	for _, m := range history {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content += "\n" + content
			continue
		}
		out = append(out, Message{Role: role, Content: content})
	}
	return out
}

// complete performs one round trip with the configured provider.
func (c *Client) complete(ctx context.Context, system string, messages []Message, tools []Tool) (Response, error) {
	switch c.cfg.Provider {
	case "anthropic":
		return c.completeAnthropic(ctx, system, messages, tools)
	case "google":
		return c.completeGoogle(ctx, system, messages, tools)
	default:
		// OpenAI and everything that speaks its dialect, including DeepSeek,
		// Ollama and a custom base URL.
		return c.completeOpenAI(ctx, system, messages, tools)
	}
}

// ---------- OpenAI-compatible ----------

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiRequest struct {
	Model    string       `json:"model"`
	Messages []oaiMessage `json:"messages"`
	Tools    []oaiTool    `json:"tools,omitempty"`
}

type oaiResponse struct {
	Choices []struct {
		Message      oaiMessage `json:"message"`
		FinishReason string     `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) completeOpenAI(ctx context.Context, system string, messages []Message, tools []Tool) (Response, error) {
	payload := oaiRequest{Model: c.cfg.Model}
	if system != "" {
		payload.Messages = append(payload.Messages, oaiMessage{Role: "system", Content: system})
	}
	for _, m := range messages {
		payload.Messages = append(payload.Messages, oaiMessage{Role: m.Role, Content: m.Content})
	}
	for _, tool := range tools {
		payload.Tools = append(payload.Tools, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}

	body, status, err := doJSON(ctx, c.http, c.cfg.BaseURL+"/chat/completions", c.cfg.APIKey, payload)
	if err != nil {
		return Response{}, err
	}
	if status != http.StatusOK {
		return Response{}, fmt.Errorf("the provider answered %d: %s", status, truncate(string(body), 300))
	}
	var parsed oaiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Response{}, fmt.Errorf("decode the provider's answer: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("the provider refused: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("the provider returned no answer")
	}
	choice := parsed.Choices[0]
	out := Response{Text: choice.Message.Content, StopReason: choice.FinishReason}
	for _, call := range choice.Message.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: parseArguments(call.Function.Arguments),
		})
	}
	if len(out.ToolCalls) > 0 && out.StopReason == "" {
		out.StopReason = "tool_calls"
	}
	return out, nil
}

// parseArguments reads a tool's arguments, which providers send as a JSON string.
func parseArguments(raw string) map[string]interface{} {
	out := map[string]interface{}{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// A malformed argument object is reported to the model as empty rather
		// than failing the whole turn: the tool itself will say what is missing.
		return map[string]interface{}{}
	}
	return out
}

// ---------- Anthropic ----------

type antRequest struct {
	Model     string       `json:"model"`
	System    string       `json:"system,omitempty"`
	Messages  []oaiMessage `json:"messages"`
	Tools     []antTool    `json:"tools,omitempty"`
	MaxTokens int          `json:"max_tokens"`
}

type antTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

type antResponse struct {
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) completeAnthropic(ctx context.Context, system string, messages []Message, tools []Tool) (Response, error) {
	payload := antRequest{Model: c.cfg.Model, System: system, MaxTokens: 4096}
	for _, m := range messages {
		payload.Messages = append(payload.Messages, oaiMessage{Role: m.Role, Content: m.Content})
	}
	for _, tool := range tools {
		payload.Tools = append(payload.Tools, antTool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		})
	}

	body, status, err := doJSON(ctx, c.http, c.cfg.BaseURL+"/messages", c.cfg.APIKey, payload)
	if err != nil {
		return Response{}, err
	}
	if status != http.StatusOK {
		return Response{}, fmt.Errorf("the provider answered %d: %s", status, truncate(string(body), 300))
	}
	var parsed antResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Response{}, fmt.Errorf("decode the provider's answer: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("the provider refused: %s", parsed.Error.Message)
	}
	out := Response{StopReason: parsed.StopReason}
	for _, block := range parsed.Content {
		switch block.Type {
		case "text":
			out.Text += block.Text
		case "tool_use":
			args := map[string]interface{}{}
			if len(block.Input) > 0 {
				_ = json.Unmarshal(block.Input, &args)
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{ID: block.ID, Name: block.Name, Arguments: args})
		}
	}
	return out, nil
}

// ---------- Google ----------

type gemRequest struct {
	Contents          []gemContent `json:"contents"`
	SystemInstruction *gemContent  `json:"systemInstruction,omitempty"`
	Tools             []gemTool    `json:"tools,omitempty"`
}

type gemContent struct {
	Role  string   `json:"role,omitempty"`
	Parts []string `json:"parts"`
}

type gemTool struct {
	FunctionDeclarations []struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Parameters  interface{} `json:"parameters"`
	} `json:"functionDeclarations"`
}

type gemResponse struct {
	Candidates []struct {
		Content gemContent `json:"content"`
		Finish  string     `json:"finishReason"`
	} `json:"candidates"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (c *Client) completeGoogle(ctx context.Context, system string, messages []Message, tools []Tool) (Response, error) {
	payload := gemRequest{}
	if system != "" {
		payload.SystemInstruction = &gemContent{Parts: []string{system}}
	}
	for _, m := range messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		payload.Contents = append(payload.Contents, gemContent{Role: role, Parts: []string{m.Content}})
	}
	if len(tools) > 0 {
		decl := gemTool{}
		for _, tool := range tools {
			decl.FunctionDeclarations = append(decl.FunctionDeclarations, struct {
				Name        string      `json:"name"`
				Description string      `json:"description"`
				Parameters  interface{} `json:"parameters"`
			}{Name: tool.Name, Description: tool.Description, Parameters: tool.InputSchema})
		}
		payload.Tools = []gemTool{decl}
	}

	url := c.cfg.BaseURL + "/models/" + c.cfg.Model + ":generateContent"
	body, status, err := doJSON(ctx, c.http, url, c.cfg.APIKey, payload)
	if err != nil {
		return Response{}, err
	}
	if status != http.StatusOK {
		return Response{}, fmt.Errorf("the provider answered %d: %s", status, truncate(string(body), 300))
	}
	var parsed gemResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Response{}, fmt.Errorf("decode the provider's answer: %w", err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return Response{}, fmt.Errorf("the provider refused: %s", parsed.Error.Message)
	}
	if len(parsed.Candidates) == 0 {
		return Response{}, fmt.Errorf("the provider returned no answer")
	}
	out := Response{StopReason: parsed.Candidates[0].Finish}
	for _, part := range parsed.Candidates[0].Content.Parts {
		out.Text += part
	}
	return out, nil
}

// ---------- transport ----------

// doJSON posts a JSON body and returns the response body and status.
func doJSON(ctx context.Context, client *http.Client, url, apiKey string, payload interface{}) ([]byte, int, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, fmt.Errorf("encode the request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, 0, fmt.Errorf("build the request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("reach the provider: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read the provider's answer: %w", err)
	}
	return body, resp.StatusCode, nil
}

// truncate shortens a message for an error, so a long provider response does not
// fill the UI.
func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
