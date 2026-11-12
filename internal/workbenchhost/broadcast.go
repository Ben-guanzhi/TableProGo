package workbenchhost

// broadcast.go — W-T2's broadcast, with the acquisition/emission boundary made
// operational (AD-3 / §6.1).
//
// Writing the same input frame to N terminal channels is an EMISSION: once the
// bytes are in the channel they are idΓ — no inverse exists, Theorem 80 does not
// speak of them. The only protection is WITHHOLDING (output commit): the broadcast
// does not run until the operator confirms, and the confirmation is a separate
// ENDPOINT, never a request flag (the host's Safe Mode convention — "跳过确认不能
// 是请求 flag").
//
// The withhold decision reads the safe.mode INTERCEPTION key (Σinter, Def 26/27).
// It is a derived realization: nothing in the shared table changes when the policy
// changes, so flipping Safe Mode triggers NO reload of any fiber — AD-3's "改策略
// 不触发重载". Right-bias note: Def 27's merge is component-declared ⊕ context-
// carried with the context winning; today only the host writes this key, so the
// host's metadata IS the effective policy. When a plugin starts declaring
// safe-mode metadata of its own, the right-biased merge lands HERE.

import (
	"sort"

	"tablepro-go/internal/ctxcore"
)

// BroadcastResult is the per-channel outcome of one broadcast.
type BroadcastResult struct {
	Withheld bool              `json:"withheld,omitempty"`
	Results  map[string]string `json:"results,omitempty"` // channelID → output
	Errors   map[string]string `json:"errors,omitempty"`  // channelID → error
}

// safeModeOn reads the interception metadata. Absent = off: a deployment that has
// never touched Safe Mode broadcasts without a prompt, which is the same default
// the SQL editor has (Safe Mode is an opt-in discipline).
func (r *Runner) safeModeOn() bool {
	v := r.store.InterceptOf(ctxcore.KeySafeMode)
	b, ok := v.(bool)
	return ok && b
}

// WithholdBroadcast decides — from the interception key alone — whether the
// broadcast needs a confirmation. Public so the API layer can call it before
// building a response.
func (r *Runner) WithholdBroadcast() bool {
	return r.safeModeOn()
}

// SetSafeMode flips the safe.mode interception key. A derived realization: no
// effect is tracked, no fiber reloads, nothing in the key surface moves — only
// what the NEXT broadcast reads changes.
func (r *Runner) SetSafeMode(on bool) {
	r.store.Intercept(ctxcore.KeySafeMode, on)
}

// Broadcast writes the same input to N channels, in id order. Sequential on
// purpose: the plugin process serves requests one at a time anyway, and a
// deterministic order makes the per-channel results reproducible.
//
// EMISSION: no inverse is tracked, nothing is recorded in the key surface, and a
// failure on one channel does not undo the others (there is no undo — the plan's
// §4 obligation: "发射不可撤回").
func (r *Runner) Broadcast(ids []string, input string, maxBytes int) BroadcastResult {
	res := BroadcastResult{Results: map[string]string{}, Errors: map[string]string{}}
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	for _, id := range ordered {
		out, _, err := r.WriteChannel(id, input, maxBytes)
		if err != nil {
			res.Errors[id] = err.Error()
			continue
		}
		res.Results[id] = out
	}
	return res
}

// SessionInfo is one entry of the workbench state snapshot the UI renders:
// the nav nodes (tabs), the open channels, and the Safe Mode policy. It reads the
// key tables through σ (ACTIVE fibers only), so a dead plugin's bindings are
// already gone by the time the UI asks.
type SessionInfo struct {
	PluginID    string        `json:"pluginId"`
	ProviderUID string        `json:"providerUid"`
	Active      bool          `json:"active"`
	NavNodes    []NodeInfo    `json:"navNodes"`
	Channels    []ChannelInfo `json:"channels"`
	SafeModeOn  bool          `json:"safeModeOn"`
}

// NodeInfo is one nav.nodes binding: the tag identifies it (unique, stable across
// restarts), the payload is whatever the plugin declared (label/icon).
type NodeInfo struct {
	Tag     string `json:"tag"`
	Label   string `json:"label,omitempty"`
	Payload any    `json:"payload,omitempty"`
}

// ChannelInfo is one terminal.channels binding.
type ChannelInfo struct {
	Tag     string `json:"tag"`
	Payload any    `json:"payload,omitempty"`
}

// State renders the current workbench surface for the UI: tabs, channels, policy.
func (r *Runner) State() SessionInfo {
	var out SessionInfo
	f := r.Fiber()
	sigma := r.store.Sigma()
	for _, b := range sigma[ctxcore.KeyNavNodes] {
		n := NodeInfo{Tag: string(b.Tag), Payload: b.Value, Label: string(b.Tag)}
		if m, ok := b.Value.(map[string]any); ok {
			if l, ok := m["label"].(string); ok {
				n.Label = l
			}
		}
		out.NavNodes = append(out.NavNodes, n)
	}
	for _, b := range sigma[ctxcore.KeyTerminalChannels] {
		out.Channels = append(out.Channels, ChannelInfo{Tag: string(b.Tag), Payload: b.Value})
	}
	out.SafeModeOn = r.safeModeOn()
	if f != nil {
		out.ProviderUID = f.UID()
		out.Active = f.State() == ctxcore.Active
		out.PluginID = f.ID()
	}
	return out
}
