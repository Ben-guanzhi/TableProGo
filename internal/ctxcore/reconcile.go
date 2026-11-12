package ctxcore

// P0 item 3: declarative configuration reconciliation (ch09 §5.2.1, Def 81).
//
// An Entry declares ONE fiber: id (stable reconciliation key), Path (the plugin
// executable — what Cordis calls url), Enabled, Config (bound into the load), and
// Digest (the file's SHA-256 as recorded by pluginreg). The loader responds to a
// change in an entry's fields by adjusting the fiber — and the binding runs both
// ways: a component that revises its own config has the change written back to its
// entry (ch09: "the binding runs in both directions").
//
// Per-field dispatch (Table: Def 81 → loader behaviour):
//
//	id/path  changed → REBUILD: retire the old fiber, instantiate a fresh one. The
//	           identity of the entry is what survives; the fiber is the identity of
//	           one enablement.
//	config   changed → handed to the component; reload only on a MATERIAL change.
//	           Here the materiality check is the plugin's own diff (the component's
//	           Load receives the new config and the host compares fibers by id), so
//	           a config change reloads the fiber and hands the new value in.
//	enabled  changed → unload when set, reload when cleared. RE-ENABLEMENT MUST
//	           instantiate a FRESH fiber (§4.4 configuration: re-enablement takes
//	           the composite, never a rule writing τ back to ⊥ — Theorem 80 rests
//	           on a retired fiber staying retired, Lemma 59(5)).
//	digest   changed → TRANSACTIONAL reload (§5.2.2 HMR phase 3): instantiate the
//	           new component; on failure restore the previous one. "The system
//	           never enters a half-reloaded state."
//
// Why fresh fibers matter (Thm 80): the quiescent state is a function of the
// final configuration ALONE. A retired fiber staying retired is an invariant the
// confluence proof leans on (Lemma 59(5)); resurrecting τ would break it. The
// composite retire→deactivate→remove→reinsert lands exactly where a from-scratch
// load of the new configuration would have (Thm 80's canonical form).

import "fmt"

// Entry is Def 81's declarative record for ONE workbench plugin fiber.
type Entry struct {
	ID      string         `json:"id"`
	Path    string         `json:"path"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config,omitempty"`
	Digest  string         `json:"digest,omitempty"`
}

// ComponentOf builds the Component a fiber instantiates from an entry. The load
// iterator itself is supplied by the caller (the plugin host), since actually
// launching a child process is W-T1's concern; here the fiber gets a no-op load so
// the reconciliation semantics are testable without processes.
func (e Entry) ComponentOf(load func(*Store, *Fiber) Iterator) Component {
	return Component{
		ID:       "entry:" + e.ID,
		Provides: nil,
		Depends:  nil,
		Load:     load,
	}
}

// ReconcileReport is what one reconciliation pass did, for the ops UI and the tests.
type ReconcileReport struct {
	Loaded    []string // entries instantiated (fresh fibers)
	Unloaded  []string // entries retired
	Reloaded  []string // entries rebuilt for a material change
	Restored  []string // transactional reloads that failed and rolled back
	Unchanged []string // entries whose fields produced no fiber action
	Errors    []string
}

// LoaderFactory builds the component for an entry. It is a function so the
// transactional reload can rebuild from the PREVIOUS definition when the new one
// fails to load (Algorithm 10's backup/restore).
type LoaderFactory func(e Entry) (Component, error)

// Reconcile brings the running fibers in line with the desired entries
// (§5.2.1: incremental, justified by Thm 80/73/Cor 69/70).
//
// desired is the full desired set (a whole snapshot, not a delta): the loader
// diffs entries by id, dispatches per changed field, and leaves everything else
// untouched. Entries the host tracks but that are NOT in desired are retired —
// the configuration tree is the source of truth.
func (s *Store) Reconcile(desired []Entry, factory LoaderFactory) ReconcileReport {
	var rep ReconcileReport

	// Index the desired entries by id and detect duplicate ids (the reconciliation
	// key must be unique).
	byID := map[string]Entry{}
	for _, e := range desired {
		if e.ID == "" {
			rep.Errors = append(rep.Errors, "entry with empty id")
			continue
		}
		if _, dup := byID[e.ID]; dup {
			rep.Errors = append(rep.Errors, "duplicate entry id "+e.ID)
			continue
		}
		byID[e.ID] = e
	}

	// Retire tracked fibers whose entry vanished or was disabled.
	s.mu.Lock()
	tracked := make([]string, 0, len(s.entries))
	for id := range s.entries {
		tracked = append(tracked, id)
	}
	s.mu.Unlock()
	for _, id := range tracked {
		e, want := byID[id]
		f := s.fiberOf(id)
		if f == nil {
			continue
		}
		if !want || !e.Enabled {
			if f.State() != Inactive || !f.Retired() {
				s.Retire(f)
				rep.Unloaded = append(rep.Unloaded, id)
			}
			// Record the disabled state so a later re-enable is dispatched as an
			// ENABLED TRANSITION, not read as "unchanged" (Def 81: disabled is a
			// field the loader dispatches on, in both directions).
			if want {
				s.setEntry(e)
			} else {
				s.removeEntry(id)
			}
		}
	}

	// Instantiate / adjust the desired entries.
	for _, e := range desired {
		if e.ID == "" || !e.Enabled {
			continue // disabled entries hold no fiber
		}
		cur, haveCur := s.entryOf(e.ID)
		f := s.fiberOf(e.ID)

		if !haveCur || f == nil || (haveCur && !cur.Enabled) {
			// Fresh entry, or RE-ENABLEMENT of a disabled one: instantiate a NEW
			// fiber. Re-enablement is reinsertion at the same name with a fresh
			// fiber — τ never flips back (§4.4 configuration).
			if err := s.instantiate(e, factory, &rep); err != nil {
				rep.Errors = append(rep.Errors, e.ID+": "+err.Error())
			}
			continue
		}

		if cur.Path != e.Path || cur.Digest != e.Digest {
			// id/url/digest changed → REBUILD, transactionally (Algorithm 10):
			// build the new component FIRST; on a build failure keep the old fiber
			// — never a half-reloaded state. The digest change is exactly HMR's
			// "module changed on disk".
			//
			// Deliberate deviation from Algorithm 10, recorded here: the paper
			// disposes the old fiber BEFORE importing the new module, because the
			// import invalidates the caches the old fiber still needs, so a failed
			// import must RESTORE the old component from its cache backup. A Go
			// factory is a pure function with no caches to invalidate, so building
			// first means a refused rebuild leaves the old fiber UNTOUCHED — zero
			// downtime, and the same endpoint (Thm 80). The insert-stage rollback
			// below keeps the paper's restore path for failures that happen after
			// the old fiber was retired.
			newComp, err := factory(e)
			if err != nil {
				rep.Errors = append(rep.Errors, e.ID+": rebuild refused: "+err.Error())
				// The entry records what failed; the old fiber keeps running.
				s.setEntry(e)
				continue
			}
			s.Retire(f)
			rep.Unloaded = append(rep.Unloaded, e.ID)
			if err := s.insertEntryFiber(e, newComp); err != nil {
				// Rollback: restore the PREVIOUS component (backup[entry.url]).
				rep.Errors = append(rep.Errors, e.ID+": rebuild failed, restoring previous: "+err.Error())
				rep.Restored = append(rep.Restored, e.ID)
				if oldComp, oerr := factory(cur); oerr == nil {
					if err2 := s.insertEntryFiber(cur, oldComp); err2 != nil {
						rep.Errors = append(rep.Errors, e.ID+": rollback failed: "+err2.Error())
					}
				}
				s.setEntry(e) // the file changed even if the reload failed
				continue
			}
			rep.Reloaded = append(rep.Reloaded, e.ID)
			s.setEntry(e)
			continue
		}

		if !configEqual(cur.Config, e.Config) {
			// config changed → handed to the component (a material change reloads
			// the fiber with the new config bound into its load).
			s.Retire(f)
			if err := s.instantiate(e, factory, &rep); err != nil {
				rep.Errors = append(rep.Errors, e.ID+": config reload failed: "+err.Error())
				// Rollback to the previous config.
				if oldComp, oerr := factory(cur); oerr == nil {
					_ = s.insertEntryFiber(cur, oldComp)
				}
				rep.Restored = append(rep.Restored, e.ID)
			} else {
				rep.Reloaded = append(rep.Reloaded, e.ID)
				s.setEntry(e)
			}
			continue
		}

		rep.Unchanged = append(rep.Unchanged, e.ID)
	}
	return rep
}

// instantiate retires any leftover fiber for the entry, then inserts a fresh one.
func (s *Store) instantiate(e Entry, factory LoaderFactory, rep *ReconcileReport) error {
	comp, err := factory(e)
	if err != nil {
		return err
	}
	if err := s.insertEntryFiber(e, comp); err != nil {
		return err
	}
	rep.Loaded = append(rep.Loaded, e.ID)
	return nil
}

// insertEntryFiber records the entry and Uses a fresh fiber for it.
//
// A revision is the §4.4 configuration COMPOSITE: retire the fiber, let the
// lifecycle deactivate it, REMOVE the vestigial entry (children before parent —
// an entry fiber has none), and reinsert at the same name with the new component.
// O-Insert's premise n ∉ dom(F_γ) requires the remove: a retired-but-present
// entry would refuse the reinsertion. The composite is held to its ENDPOINT —
// by Thm 80 the system quiesces where a load of the revised configuration from
// scratch would have left it.
func (s *Store) insertEntryFiber(e Entry, comp Component) error {
	if old := s.fiberOf(e.ID); old != nil {
		if !old.Retired() {
			s.Retire(old)
		}
		if err := s.Remove(old); err != nil {
			return fmt.Errorf("remove the previous fiber: %w", err)
		}
	}
	f, _, err := s.Use(comp, "root")
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.entries == nil {
		s.entries = map[string]Entry{}
	}
	s.entries[e.ID] = e
	s.entryFibers[e.ID] = f
	s.mu.Unlock()
	return nil
}

// fiberOf resolves the live fiber of a tracked entry, if any.
func (s *Store) fiberOf(id string) *Fiber {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entryFibers[id]
}

func (s *Store) entryOf(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[id]
	return e, ok
}

func (s *Store) setEntry(e Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = map[string]Entry{}
	}
	s.entries[e.ID] = e
}

func (s *Store) removeEntry(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, id)
	delete(s.entryFibers, id)
}

// configEqual compares two config maps field by field. A nil and an empty map are
// equal (both mean "no config").
func configEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		switch av := va.(type) {
		case string:
			bv, ok := vb.(string)
			if !ok || av != bv {
				return false
			}
		case bool:
			bv, ok := vb.(bool)
			if !ok || av != bv {
				return false
			}
		case float64:
			bv, ok := vb.(float64)
			if !ok || av != bv {
				return false
			}
		default:
			if fmt.Sprint(va) != fmt.Sprint(vb) {
				return false
			}
		}
	}
	return true
}
