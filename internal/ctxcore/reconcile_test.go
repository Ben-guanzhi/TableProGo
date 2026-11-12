package ctxcore

// P0 item 3 acceptance: Def 81 field-level reconciliation.
//
// The invariants under test are the ones the plan names:
//   - enabled 清除 → unload；重新启用 → **新纤维**（τ 不复活，保 Thm 80）；
//   - digest 变更 → 事务性重载（备份 + 失败回滚），"never a half-reloaded state"；
//   - config 变更 → 物质变更才重载，新 config 绑定进 load；
//   - 消失的条目 → retire（配置树是唯一真相）；
//   - 全程收敛（Settle 驱动，Thm 73）。

import (
	"fmt"
	"strings"
	"testing"
)

// countingLoader builds a LoaderFactory that yields components whose load
// registers one tag per instance, and counts how many components it BUILT —
// the transactional reload test needs to tell "rebuilt" from "restored".
func countingLoader(built *int, failFor map[string]bool) LoaderFactory {
	return func(e Entry) (Component, error) {
		*built++
		if failFor[e.Digest] {
			return Component{}, fmt.Errorf("broken module %s", e.Digest)
		}
		uid := e.ID + "/" + e.Digest
		comp := Component{
			ID: "entry:" + e.ID,
			Load: func(s *Store, f *Fiber) Iterator {
				var next func() (Stage, error)
				next = func() (Stage, error) {
					return Stage{
						Effect: Effect{Name: "register", Forward: func(st *Store) (Inverse, error) {
							tag := Tag(uid)
							if err := st.table(KeyNavNodes).insert(Binding{Tag: tag, Provider: f.uid, Value: e.Config}); err != nil {
								return nil, err
							}
							st.notify(KeyNavNodes)
							return func(st2 *Store) error {
								return st2.table(KeyNavNodes).remove(tag)
							}, nil
						}},
						Done: true,
					}, nil
				}
				return next
			},
		}
		return comp, nil
	}
}

func entryTags(s *Store, e Entry) []string {
	var out []string
	for _, b := range s.table(KeyNavNodes).snapshot() {
		if strings.HasPrefix(string(b.Tag), e.ID+"/") {
			out = append(out, string(b.Tag))
		}
	}
	return out
}

func TestReconcileEnableDisableFreshFiber(t *testing.T) {
	// 重新启用必须新纤维：卸载后再启用，uid 必须不同（τ 不复活，Thm 80）。
	s := NewStore()
	built := 0
	make := countingLoader(&built, nil)
	uid1, uid2 := "", ""

	e := Entry{ID: "ssh", Path: "/plugins/ssh", Enabled: true, Digest: "d1"}
	rep := s.Reconcile([]Entry{e}, make)
	if len(rep.Loaded) != 1 || len(rep.Errors) != 0 {
		t.Fatalf("initial load: %+v", rep)
	}
	f1 := s.fiberOf("ssh")
	if f1 == nil || f1.State() != Active {
		t.Fatalf("fiber state = %v", f1)
	}
	uid1 = f1.UID()

	// Disable → unload.
	e.Enabled = false
	rep = s.Reconcile([]Entry{e}, make)
	if len(rep.Unloaded) != 1 {
		t.Fatalf("disable: %+v", rep)
	}
	if f1.State() != Inactive || !f1.Retired() {
		t.Fatalf("after disable: state=%s retired=%v", f1.State(), f1.Retired())
	}

	// Re-enable → a FRESH fiber with a different uid; the retired one stays retired.
	e.Enabled = true
	rep = s.Reconcile([]Entry{e}, make)
	if len(rep.Loaded) != 1 {
		t.Fatalf("re-enable: %+v", rep)
	}
	f2 := s.fiberOf("ssh")
	if f2 == nil || f2.State() != Active {
		t.Fatalf("re-enabled fiber = %v", f2)
	}
	uid2 = f2.UID()
	if uid1 == uid2 {
		t.Fatalf("Thm 80 violated: re-enablement resurrected fiber uid %s (τ must stay retired)", uid1)
	}
	if !f1.Retired() || f1.State() != Inactive {
		t.Fatalf("the old fiber was resurrected: state=%s retired=%v", f1.State(), f1.Retired())
	}
}

func TestReconcileDigestChangeIsTransactional(t *testing.T) {
	// digest 变更 → 事务性重载；新模块加载失败 → 恢复旧组件（Algorithm 10），
	// "never a half-reloaded state"。
	s := NewStore()
	built := 0
	failFor := map[string]bool{"d2": true} // the NEW digest is a broken module
	make := countingLoader(&built, failFor)

	e := Entry{ID: "ssh", Path: "/plugins/ssh", Enabled: true, Digest: "d1"}
	if rep := s.Reconcile([]Entry{e}, make); len(rep.Errors) != 0 {
		t.Fatalf("initial: %+v", rep)
	}
	if got := entryTags(s, e); len(got) != 1 {
		t.Fatalf("initial tags = %v", got)
	}

	// A digest whose component FAILS to BUILD: the loader builds BEFORE touching
	// the running fiber, so the old one keeps serving UNINTERRUPTED and no
	// rollback action is needed. This is deliberately STRONGER than Cordis's
	// Algorithm 10 (dispose → import → restore caches): the paper must dispose
	// first because importing invalidates the module caches the old fiber still
	// needs; a Go factory is a pure function, so "build first, swap only on
	// success" buys zero downtime. Either way the endpoint is the same (Thm 80):
	// the previous component serving, with an outcome recorded on the report.
	e.Digest = "d2"
	rep := s.Reconcile([]Entry{e}, make)
	if len(rep.Errors) != 1 {
		t.Fatalf("the build failure must be reported: %+v", rep)
	}
	if len(rep.Restored) != 0 || len(rep.Unloaded) != 0 {
		t.Fatalf("a refused rebuild must not disturb the running fiber: %+v", rep)
	}
	if got := entryTags(s, Entry{ID: "ssh", Digest: "d1"}); len(got) != 1 {
		t.Fatalf("the old fiber's binding was lost: %v", got)
	}
	if f := s.fiberOf("ssh"); f == nil || f.State() != Active {
		t.Fatalf("the old fiber must keep serving after a failed reload: %v", f)
	}

	// A digest whose component loads: rebuild in place.
	e.Digest = "d3"
	rep = s.Reconcile([]Entry{e}, make)
	if len(rep.Reloaded) != 1 || len(rep.Restored) != 0 {
		t.Fatalf("rebuild: %+v", rep)
	}
	if got := entryTags(s, e); len(got) != 1 || !strings.Contains(got[0], "d3") {
		t.Fatalf("after rebuild tags = %v, want the d3 binding", got)
	}
}

func TestReconcileConfigChangeReloadsMaterially(t *testing.T) {
	// config 变更 → 物质变更才重载；新 config 绑定进 load（可见于注册的值）。
	s := NewStore()
	built := 0
	make := countingLoader(&built, nil)
	e := Entry{ID: "ssh", Path: "/plugins/ssh", Enabled: true, Digest: "d1",
		Config: map[string]any{"host": "a"}}
	if rep := s.Reconcile([]Entry{e}, make); len(rep.Errors) != 0 {
		t.Fatalf("initial: %+v", rep)
	}
	before := built

	// The SAME config → Unchanged, no rebuild.
	rep := s.Reconcile([]Entry{e}, make)
	if len(rep.Unchanged) != 1 || built != before {
		t.Fatalf("same config rebuilt the fiber: %+v built=%d", rep, built)
	}

	// A materially different config → reload with the new value.
	e.Config = map[string]any{"host": "b"}
	rep = s.Reconcile([]Entry{e}, make)
	if len(rep.Reloaded) != 1 {
		t.Fatalf("config change: %+v", rep)
	}
	val, ok := s.Get(KeyNavNodes, Tag("ssh/d1"))
	if !ok {
		t.Fatal("the reloaded fiber's binding is missing")
	}
	cfg := val.(map[string]any)
	if cfg["host"] != "b" {
		t.Fatalf("the fiber runs with stale config: %v", cfg)
	}
}

func TestReconcileRemovedEntryRetires(t *testing.T) {
	// 消失的条目 → retire：配置树是唯一真相（Cor 69：离场纤维的贡献为零）。
	s := NewStore()
	built := 0
	make := countingLoader(&built, nil)
	e := Entry{ID: "ssh", Path: "/p", Enabled: true, Digest: "d1"}
	if rep := s.Reconcile([]Entry{e}, make); len(rep.Loaded) != 1 {
		t.Fatalf("initial: %+v", rep)
	}
	rep := s.Reconcile(nil, make) // the entry vanished from the configuration
	if len(rep.Unloaded) != 1 {
		t.Fatalf("vanish: %+v", rep)
	}
	if !s.Quiescent() {
		t.Fatal("did not quiesce after the entry vanished")
	}
	if got := entryTags(s, e); len(got) != 0 {
		t.Fatalf("Cor 69 violated: tags left behind = %v", got)
	}
}

func TestReconcileConfluenceAcrossSchedules(t *testing.T) {
	// Thm 80 applied to the loader: reconciling to the same final configuration
	// via DIFFERENT routes (one-step swap vs disable-then-change-then-enable)
	// must land on ≃-equal key surfaces.
	target := Entry{ID: "ssh", Path: "/p", Enabled: true, Digest: "d2",
		Config: map[string]any{"host": "b"}}

	route1 := NewStore()
	make1 := countingLoader(new(int), nil)
	e1 := Entry{ID: "ssh", Path: "/p", Enabled: true, Digest: "d1", Config: map[string]any{"host": "a"}}
	if rep := route1.Reconcile([]Entry{e1}, make1); len(rep.Errors) != 0 {
		t.Fatalf("route1 initial: %+v", rep)
	}
	if rep := route1.Reconcile([]Entry{target}, make1); len(rep.Errors) != 0 {
		t.Fatalf("route1 swap: %+v", rep)
	}

	route2 := NewStore()
	make2 := countingLoader(new(int), nil)
	if rep := route2.Reconcile([]Entry{e1}, make2); len(rep.Errors) != 0 {
		t.Fatalf("route2 initial: %+v", rep)
	}
	// The long route: disable, change digest+config while off, re-enable.
	off := e1
	off.Enabled = false
	if rep := route2.Reconcile([]Entry{off}, make2); len(rep.Unloaded) != 1 {
		t.Fatalf("route2 disable: %+v", rep)
	}
	changed := target
	if rep := route2.Reconcile([]Entry{changed}, make2); len(rep.Loaded) != 1 {
		t.Fatalf("route2 re-enable: %+v", rep)
	}

	a, b := entryTags(route1, target), entryTags(route2, target)
	if strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("Thm 80 violated across loader routes:\nroute1=%v\nroute2=%v", a, b)
	}
}
