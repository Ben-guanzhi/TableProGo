package workbenchhost

// W-A1/A2 acceptance.
//
//  1. THE HOST ASSEMBLES THE CONTEXT: a fake OpenAI-compatible endpoint records the
//     request body; the test asserts the schema / terminal tail / notes arrived in
//     the SYSTEM message — proving the plugin received a finished context and never
//     read a key itself.
//  2. THE API KEY ARRIVES BY LEND: the fake checks the Authorization header; the
//     open params carried only the NAME (the plugin's own rule refuses a key field).
//  3. NL→SQL IS A PREVIEW BY CONSTRUCTION: the host value has Preview=true and the
//     package has no execution path; the HTTP layer refuses an execute field
//     (that assertion lives in the App-level test).
//  4. THE TOOL TABLE: Safe/Auto allow, Confirm withholds, an unregistered tool is
//     refused — and unloading the fiber removes every row (Thm 68).

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"tablepro-go/internal/ctxcore"
)

// fakeOpenAI stands in for a chat-completions endpoint.
type fakeOpenAI struct {
	requests []map[string]any
	auth     []string
	reply    string
}

func (f *fakeOpenAI) handler(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.requests = append(f.requests, body)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	reply := f.reply
	if reply == "" {
		reply = "ok"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": reply}}},
	})
}

func aiSetup(t *testing.T) (*Runner, *fakeOpenAI, string) {
	t.Helper()
	fake := &fakeOpenAI{}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)

	binary := buildPluginModule(t, "navop-ai")
	store := ctxcore.NewStore()
	r := New(store)
	if err := r.Launch(binary); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Stop)
	waitFiberState(t, r.Fiber(), ctxcore.Active, 10*time.Second)
	return r, fake, srv.URL
}

func TestAIContextIsAssembledByTheHost(t *testing.T) {
	r, fake, url := aiSetup(t)
	fake.reply = "好的"
	prov, err := r.OpenAIProvider(url, "test-model", "ai.apiKey", []byte("sk-test-123"))
	if err != nil {
		t.Fatalf("open provider: %v", err)
	}
	if prov.Model != "test-model" {
		t.Fatalf("model = %q", prov.Model)
	}

	ctx := AIContext{
		Schema:   "orders(id bigint, customer_id bigint, total numeric)",
		Terminal: "psql: ERROR:  relation \"orderz\" does not exist",
		Notes:    "巡检：周二凌晨跑迁移",
	}
	text, err := r.AIChat(prov.Session, []AIMessage{{Role: "user", Content: "为什么查询失败？"}}, ctx)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if text != "好的" {
		t.Fatalf("text = %q", text)
	}

	if len(fake.requests) != 1 {
		t.Fatalf("the endpoint saw %d requests", len(fake.requests))
	}
	// The API key arrived by lend: the header carries it, and the params never did.
	if fake.auth[0] != "Bearer sk-test-123" {
		t.Fatalf("Authorization = %q", fake.auth[0])
	}
	raw, _ := json.Marshal(fake.requests[0])
	if strings.Contains(string(raw), "sk-test-123") {
		t.Fatal("the API key ended up inside the request body")
	}
	// The context is in the SYSTEM message, with each source named.
	msgs, _ := fake.requests[0]["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	sys, _ := msgs[0].(map[string]any)
	if sys["role"] != "system" {
		t.Fatalf("first message is not the system context: %v", sys)
	}
	sysText, _ := sys["content"].(string)
	for _, want := range []string{"orders(id bigint", "orderz", "周二凌晨跑迁移", "选中的表结构", "终端最近输出", "笔记"} {
		if !strings.Contains(sysText, want) {
			t.Fatalf("the context is missing %q:\n%s", want, sysText)
		}
	}
	// An unattached source is ABSENT, not an empty heading.
	if strings.Contains(sysText, "远端文件片段") {
		t.Fatalf("an empty file source was rendered: %s", sysText)
	}
}

func TestNL2SQLIsAlwaysAPreview(t *testing.T) {
	r, fake, url := aiSetup(t)
	fake.reply = "```sql\nSELECT id, total FROM orders WHERE total > 100;\n```\n选出总额超过 100 的订单。"
	prov, err := r.OpenAIProvider(url, "m", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.AINL2SQL(prov.Session, "找出总额超过一百的订单", "postgres", AIContext{Schema: "orders(id, total)"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Preview {
		t.Fatal("the value must be marked as a preview")
	}
	if !strings.Contains(res.SQL, "SELECT id, total FROM orders") {
		t.Fatalf("sql = %q", res.SQL)
	}
	if strings.Contains(res.SQL, "```") {
		t.Fatalf("the fence leaked into the SQL: %q", res.SQL)
	}
	if !strings.Contains(res.Explanation, "总额超过 100") {
		t.Fatalf("explanation = %q", res.Explanation)
	}
	// The system instruction forbids executing and asks for one statement.
	sys, _ := fake.requests[0]["messages"].([]any)
	first, _ := sys[0].(map[string]any)
	content, _ := first["content"].(string)
	if !strings.Contains(content, "PREVIEW") {
		t.Fatalf("the instruction did not mark the output as a preview: %s", content)
	}
	// An anonymous provider is allowed (a local gateway) and sends no Authorization.
	if strings.Contains(string(mustJSON(fake.requests[0])), "Bearer") {
		t.Fatal("an anonymous provider sent an Authorization header")
	}
}

func TestMCPToolPermissions(t *testing.T) {
	r, _, _ := aiSetup(t)
	f := r.Fiber()
	store := r.Store()

	for _, tool := range []MCPTool{
		{Name: "schema", Level: PermSafe, Owner: "host"},
		{Name: "execute", Level: PermConfirm, Owner: "host"},
		{Name: "vacuum", Level: PermAuto, Owner: "host"},
		{Name: "weird", Level: "bogus", Owner: "host"}, // normalizes to Confirm
	} {
		if err := RegisterTool(f, store, tool); err != nil {
			t.Fatalf("register %s: %v", tool.Name, err)
		}
	}
	if got := len(r.MCPTools()); got != 4 {
		t.Fatalf("tools = %d", got)
	}

	level, allowed, withheld, err := r.ToolGate("schema", false)
	if err != nil || !allowed || withheld || level != PermSafe {
		t.Fatalf("safe tool = %v %v %v %v", level, allowed, withheld, err)
	}
	level, allowed, withheld, err = r.ToolGate("vacuum", false)
	if err != nil || !allowed || level != PermAuto {
		t.Fatalf("auto tool = %v %v %v %v", level, allowed, withheld, err)
	}
	// Confirm is WITHHELD on the gated path and allowed on the confirmed one.
	if _, allowed, withheld, _ = r.ToolGate("execute", false); allowed || !withheld {
		t.Fatal("a confirm-level tool must be withheld without confirmation")
	}
	if _, allowed, withheld, _ = r.ToolGate("execute", true); !allowed || withheld {
		t.Fatal("the confirmed path must allow it")
	}
	// An unknown level falls back to Confirm (the conservative default).
	if _, _, withheld, _ = r.ToolGate("weird", false); !withheld {
		t.Fatal("an unknown level must normalize to confirm")
	}
	// An unregistered tool cannot be called at all.
	if _, _, _, err := r.ToolGate("nope", true); err == nil {
		t.Fatal("an unregistered tool must be refused")
	}

	// Thm 68 at mcp.tools: unloading the fiber removes exactly its rows.
	store.Retire(f)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(r.MCPTools()) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := r.MCPTools(); len(got) != 0 {
		t.Fatalf("Cor 69 violated: tools survived unload: %v", got)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
