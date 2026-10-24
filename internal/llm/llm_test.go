package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeProvider is a stand-in for a chat API: it records the request it received and
// answers with whatever the test queued.
type fakeProvider struct {
	// replies are returned in order; the last one repeats.
	replies []string
	// requests records the decoded request bodies.
	requests []map[string]interface{}
	// status overrides the response code.
	status int
	// server is the running test server.
	server *httptest.Server
}

func newFakeProvider(t *testing.T, replies ...string) *fakeProvider {
	t.Helper()
	p := &fakeProvider{replies: replies}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.requests = append(p.requests, body)
		if p.status != 0 {
			w.WriteHeader(p.status)
			fmt.Fprint(w, `{"error":{"message":"refused"}}`)
			return
		}
		reply := p.replies[0]
		if len(p.replies) > 1 {
			p.replies = p.replies[1:]
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(p.server.Close)
	return p
}

// openAIAnswer builds an OpenAI-shaped reply.
func openAIAnswer(text string, toolCalls ...string) string {
	if len(toolCalls) == 0 {
		return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, text)
	}
	calls := make([]string, 0, len(toolCalls))
	for _, c := range toolCalls {
		calls = append(calls, c)
	}
	return fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":%q,"tool_calls":[%s]},"finish_reason":"tool_calls"}]}`,
		text, strings.Join(calls, ","))
}

func TestConfigNormalizeFillsTheBlanks(t *testing.T) {
	cfg := Config{Provider: "deepseek"}.Normalize()
	if cfg.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("baseURL = %q", cfg.BaseURL)
	}
	if cfg.Model != "deepseek-chat" {
		t.Fatalf("model = %q", cfg.Model)
	}
	// What the user chose is never overwritten.
	cfg = Config{Provider: "openai", BaseURL: "http://local", Model: "my-model"}.Normalize()
	if cfg.BaseURL != "http://local" || cfg.Model != "my-model" {
		t.Fatalf("normalize overwrote the user's choice: %+v", cfg)
	}
}

func TestConfigValidate(t *testing.T) {
	// Off: the reason says so.
	if err := (Config{}).Validate(); err == nil || !strings.Contains(err.Error(), "off") {
		t.Fatalf("err = %v", err)
	}
	// On but no key: the message names the provider.
	err := Config{Provider: "openai", Enabled: true}.Validate()
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("err = %v", err)
	}
	// A local runtime needs no key.
	if err := (Config{Provider: "ollama", Enabled: true, BaseURL: "http://localhost:11434/v1", Model: "llama3"}).Validate(); err != nil {
		t.Fatalf("ollama must not require a key: %v", err)
	}
	// Missing model.
	if err := (Config{Provider: "ollama", Enabled: true, BaseURL: "http://x"}).Validate(); err == nil {
		t.Fatal("a missing model must be refused")
	}
}

func TestAskSendsTheConversation(t *testing.T) {
	provider := newFakeProvider(t, openAIAnswer("4 rows in orders"))
	client := New(Config{Provider: "openai", BaseURL: provider.server.URL, APIKey: "k", Model: "m", Enabled: true})

	resp, err := client.Ask(context.Background(), "you are a SQL helper", []Message{
		{Role: "user", Content: "how many orders?"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if resp.Text != "4 rows in orders" {
		t.Fatalf("text = %q", resp.Text)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d", len(provider.requests))
	}
	req := provider.requests[0]
	messages := req["messages"].([]interface{})
	// The system prompt comes first, then the user's turn.
	first := messages[0].(map[string]interface{})
	if first["role"] != "system" || first["content"] != "you are a SQL helper" {
		t.Fatalf("first message = %v", first)
	}
	second := messages[1].(map[string]interface{})
	if second["role"] != "user" || second["content"] != "how many orders?" {
		t.Fatalf("second message = %v", second)
	}
}

// A model that asks for a tool gets it run, and the result is sent back so the
// model can answer with it.
func TestAskRunsToolsAndAnswers(t *testing.T) {
	provider := newFakeProvider(t,
		openAIAnswer("", `{"id":"call_1","type":"function","function":{"name":"list_tables","arguments":"{\"connection\":\"c1\"}"}}`),
		openAIAnswer("there are 3 tables"),
	)
	client := New(Config{Provider: "openai", BaseURL: provider.server.URL, APIKey: "k", Model: "m", Enabled: true})

	ran := 0
	var sawName string
	resp, err := client.Ask(context.Background(), "", []Message{{Role: "user", Content: "what is there?"}},
		[]Tool{{Name: "list_tables", Description: "list", InputSchema: map[string]interface{}{"type": "object"}}},
		func(ctx context.Context, name string, args map[string]interface{}) (string, error) {
			ran++
			sawName = name
			if args["connection"] != "c1" {
				t.Errorf("arguments = %v", args)
			}
			return "tables: a, b, c", nil
		})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if ran != 1 {
		t.Fatalf("the tool ran %d times", ran)
	}
	if sawName != "list_tables" {
		t.Fatalf("tool = %q", sawName)
	}
	if resp.Text != "there are 3 tables" {
		t.Fatalf("text = %q", resp.Text)
	}
	// The second request carries the tool's output back to the model.
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d", len(provider.requests))
	}
	messages := provider.requests[1]["messages"].([]interface{})
	last := messages[len(messages)-1].(map[string]interface{})
	if !strings.Contains(last["content"].(string), "tables: a, b, c") {
		t.Fatalf("the tool result was not sent back: %v", last)
	}
}

// A failing tool is information for the model, not a dead end.
func TestAskSurvivesAFailingTool(t *testing.T) {
	provider := newFakeProvider(t,
		openAIAnswer("", `{"id":"call_1","type":"function","function":{"name":"query","arguments":"{}"}}`),
		openAIAnswer("I could not read that"),
	)
	client := New(Config{Provider: "openai", BaseURL: provider.server.URL, APIKey: "k", Model: "m", Enabled: true})

	resp, err := client.Ask(context.Background(), "", []Message{{Role: "user", Content: "count"}},
		[]Tool{{Name: "query"}},
		func(context.Context, string, map[string]interface{}) (string, error) {
			return "", fmt.Errorf("connection refused")
		})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if resp.Text != "I could not read that" {
		t.Fatalf("text = %q", resp.Text)
	}
	messages := provider.requests[1]["messages"].([]interface{})
	last := messages[len(messages)-1].(map[string]interface{})
	if !strings.Contains(last["content"].(string), "connection refused") {
		t.Fatalf("the tool error was not reported to the model: %v", last)
	}
}

// A model that keeps asking for tools must not loop forever.
func TestAskStopsAfterTooManyToolRounds(t *testing.T) {
	call := `{"id":"call_1","type":"function","function":{"name":"query","arguments":"{}"}}`
	provider := newFakeProvider(t, openAIAnswer("", call))
	client := New(Config{Provider: "openai", BaseURL: provider.server.URL, APIKey: "k", Model: "m", Enabled: true})

	_, err := client.Ask(context.Background(), "", []Message{{Role: "user", Content: "loop"}},
		[]Tool{{Name: "query"}},
		func(context.Context, string, map[string]interface{}) (string, error) { return "x", nil })
	if err == nil {
		t.Fatal("the tool loop must end")
	}
	if len(provider.requests) > maxToolRounds+1 {
		t.Fatalf("the loop ran %d times", len(provider.requests))
	}
}

func TestAskRefusesAnUnusableConfig(t *testing.T) {
	client := New(Config{Provider: "openai", Enabled: true})
	if _, err := client.Ask(context.Background(), "", nil, nil, nil); err == nil {
		t.Fatal("an unusable configuration must be refused before any request")
	}
}

func TestProviderErrorsSurface(t *testing.T) {
	provider := newFakeProvider(t, `{}`)
	provider.status = 500
	client := New(Config{Provider: "openai", BaseURL: provider.server.URL, APIKey: "k", Model: "m", Enabled: true})
	_, err := client.Ask(context.Background(), "", []Message{{Role: "user", Content: "x"}}, nil, nil)
	if err == nil {
		t.Fatal("a provider error must surface")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
}

// Anthropic and Google speak their own dialects; the tool loop must work there too.
func TestAnthropicToolCall(t *testing.T) {
	// The provider asks for a tool once, then answers.
	provider := newFakeProvider(t,
		`{"content":[{"type":"tool_use","id":"tu_1","name":"list_tables","input":{"connection":"c1"}}]}`,
		`{"content":[{"type":"text","text":"three tables"}]}`,
	)
	client := New(Config{Provider: "anthropic", BaseURL: provider.server.URL, APIKey: "k", Model: "m", Enabled: true})

	ran := false
	_, err := client.Ask(context.Background(), "sys", []Message{{Role: "user", Content: "x"}},
		[]Tool{{Name: "list_tables"}},
		func(ctx context.Context, name string, args map[string]interface{}) (string, error) {
			ran = true
			if name != "list_tables" || args["connection"] != "c1" {
				t.Errorf("name=%q args=%v", name, args)
			}
			return "ok", nil
		})
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !ran {
		t.Fatal("the tool must run")
	}
	req := provider.requests[0]
	if req["system"] != "sys" {
		t.Fatalf("system = %v", req["system"])
	}
}

func TestGoogleAnswer(t *testing.T) {
	provider := newFakeProvider(t, `{"candidates":[{"content":{"role":"model","parts":["hello ","world"]},"finishReason":"STOP"}]}`)
	client := New(Config{Provider: "google", BaseURL: provider.server.URL, APIKey: "k", Model: "gemini-x", Enabled: true})
	resp, err := client.Ask(context.Background(), "sys", []Message{{Role: "user", Content: "x"}}, nil, nil)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if resp.Text != "hello world" {
		t.Fatalf("text = %q", resp.Text)
	}
}

func TestHistoryIsCleaned(t *testing.T) {
	out := normalizeHistory([]Message{
		{Role: "user", Content: "  "},
		{Role: "system", Content: "ignored"},
		{Role: "user", Content: "one"},
		{Role: "user", Content: "two"},
		{Role: "assistant", Content: "answer"},
	})
	if len(out) != 2 {
		t.Fatalf("history = %+v", out)
	}
	if out[0].Content != "one\ntwo" {
		t.Fatalf("consecutive turns should merge: %q", out[0].Content)
	}
}
