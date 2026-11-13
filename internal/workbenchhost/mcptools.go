package workbenchhost

// mcptools.go — W-A2: the `mcp.tools` aggregate table and the three-level
// permission gate.
//
// WHAT CHANGED, AND WHY IT MATTERS: the existing MCP bridge (mcp.go) registers its
// tools with a hand-written lifecycle — the list is a literal and its "is this
// allowed" answer is a boolean on the settings struct. That works, but it is the
// R2 shape the study criticizes: a rule enforced by convention, whose withdrawal is
// whatever the code remembers to do at shutdown.
//
// Migrated into the context model, a tool is a ROW at mcp.tools (tag = tool name),
// installed as a tracked effect and reclaimed by the owning fiber's accumulator. Two
// consequences the plan names:
//
//   - exact recovery (Thm 68): unloading the fiber removes exactly its tools, no
//     matter whether the process died or the operator closed the panel;
//   - the permission LEVEL is data, not a branch: Safe (read-only, always
//     answerable), Confirm (the call is WITHHELD until the operator confirms, the
//     same separate-endpoint discipline as broadcast/publish), Auto (allowed).
//
// The host consults the table; a tool that is not there cannot be called at all,
// which is what makes "卸载即撤" enforceable rather than aspirational.

import (
	"fmt"
	"sync"

	"tablepro-go/internal/ctxcore"
)

// ToolPermission is one of the three levels. The zero value is NOT Auto: an
// unknown level falls back to Confirm, because the safe default for an unknown tool
// is to ask.
type ToolPermission string

const (
	PermSafe    ToolPermission = "safe"    // read-only, runs without asking
	PermConfirm ToolPermission = "confirm" // withheld until confirmed
	PermAuto    ToolPermission = "auto"    // runs without asking (mutating)
)

// Normalize maps anything unknown onto Confirm (the conservative default).
func (p ToolPermission) Normalize() ToolPermission {
	switch p {
	case PermSafe, PermConfirm, PermAuto:
		return p
	default:
		return PermConfirm
	}
}

// MCPTool is one row of the aggregate table.
type MCPTool struct {
	Name        string         `json:"name"`
	Level       ToolPermission `json:"level"`
	Description string         `json:"description,omitempty"`
	Owner       string         `json:"owner,omitempty"` // plugin id / "host" / "mcp"
}

// RegisterTool installs one tool as a tracked effect on the fiber. A duplicate name
// is refused (the table's tags are unique): two tools with one name would make the
// permission ambiguous, which is exactly the confusion the level field exists to
// avoid.
func RegisterTool(f *ctxcore.Fiber, store *ctxcore.Store, tool MCPTool) error {
	if f == nil {
		return fmt.Errorf("no fiber to register tool %s on", tool.Name)
	}
	if tool.Name == "" {
		return fmt.Errorf("tool name must not be empty")
	}
	tool.Level = tool.Level.Normalize()
	tag := ctxcore.Tag(tool.Name)
	return store.Track(f, "mcp-tool:"+tool.Name, func(st *ctxcore.Store) (ctxcore.Inverse, error) {
		if err := st.TableInsert(ctxcore.KeyMCPTools, tag, f.UID(), map[string]any{
			"level":       string(tool.Level),
			"description": tool.Description,
			"owner":       tool.Owner,
		}); err != nil {
			return nil, err
		}
		st.Notify(ctxcore.KeyMCPTools)
		return func(st2 *ctxcore.Store) error {
			if err := st2.TableRemove(ctxcore.KeyMCPTools, tag); err != nil {
				return err
			}
			st2.Notify(ctxcore.KeyMCPTools)
			return nil
		}, nil
	})
}

// MCPTools lists the aggregate table, name-sorted.
func (r *Runner) MCPTools() []MCPTool {
	sigma := r.store.Sigma()
	out := make([]MCPTool, 0, len(sigma[ctxcore.KeyMCPTools]))
	for _, b := range sigma[ctxcore.KeyMCPTools] {
		t := MCPTool{Name: string(b.Tag)}
		if m, ok := b.Value.(map[string]any); ok {
			if lv, ok := m["level"].(string); ok {
				t.Level = ToolPermission(lv)
			}
			if d, ok := m["description"].(string); ok {
				t.Description = d
			}
			if o, ok := m["owner"].(string); ok {
				t.Owner = o
			}
		}
		out = append(out, t)
	}
	sortTools(out)
	return out
}

// ToolGate answers whether one call may proceed. It returns (allowed, withheld):
// a withheld call must be re-posted through the CONFIRMED path, exactly like a
// broadcast or a publish. An UNKNOWN tool is refused outright — the table is the
// authority, so "not registered" means "not callable".
func (r *Runner) ToolGate(name string, confirmed bool) (level ToolPermission, allowed, withheld bool, err error) {
	for _, t := range r.MCPTools() {
		if t.Name != name {
			continue
		}
		switch t.Level.Normalize() {
		case PermSafe, PermAuto:
			return t.Level, true, false, nil
		case PermConfirm:
			if confirmed {
				return t.Level, true, false, nil
			}
			return t.Level, false, true, nil
		}
	}
	return "", false, false, fmt.Errorf("未知工具 %q（mcp.tools 是权威：未注册即不可调用）", name)
}

func sortTools(list []MCPTool) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].Name < list[j-1].Name; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// toolRegistry is the host's own registrar for tools the HOST provides (the MCP
// bridge's query/execute pair). It exists so the bridge's tools appear in the same
// table as plugin tools — one aggregate surface, one permission model.
type toolRegistry struct {
	mu sync.Mutex
}

var hostTools = &toolRegistry{}

// RegisterHostTools installs the bridge's tools on the given fiber with their
// levels. Called once at startup; the rows are reclaimed with the fiber like any
// other effect.
func RegisterHostTools(f *ctxcore.Fiber, store *ctxcore.Store) error {
	hostTools.mu.Lock()
	defer hostTools.mu.Unlock()
	tools := []MCPTool{
		{Name: "query", Level: PermSafe, Owner: "mcp", Description: "只读查询（限行数）"},
		{Name: "execute", Level: PermConfirm, Owner: "mcp", Description: "写入语句（需确认）"},
	}
	for _, t := range tools {
		if err := RegisterTool(f, store, t); err != nil {
			// "query" already registered on a reload is not fatal: the tag is the
			// identity, and the accumulator of the OLD fiber removes it on unload.
			if !isDuplicateTag(err) {
				return err
			}
		}
	}
	return nil
}

// isDuplicateTag reports the one error a reload legitimately produces.
func isDuplicateTag(err error) bool {
	return err != nil && contains(err.Error(), "already bound")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
