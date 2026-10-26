package registry

import (
	"fmt"
	"reflect"
)

// Replace makes v the provider for T, replacing whatever was registered for
// T (a constructor from Register or bosun.Service, or an earlier instance) or
// registering v if nothing was. It exists for tests: swap a fake in for a
// concrete service or a middleware type that bosun.New already registered,
// where RegisterInstance would panic as a duplicate.
//
// The replacement is a ready instance, exactly as if it had been added with
// RegisterInstance, so every dependent built afterwards receives v.
//
// Replace only rewires a graph that hasn't consumed T yet. It panics, as
// Register does on a duplicate, when:
//
//   - the registry has been shut down;
//   - T was registered with a constructor that has already run (or is
//     running) — its instance may already be held by dependents;
//   - a service that depends on T has been built or is being built.
//
// So call it after bosun.New and before Start, Validate or the first Resolve
// of anything that needs T. A value returned by an earlier direct Resolve of
// an instance node is not tracked and is unaffected.
func Replace[T any](r *Registry, v T) {
	t := typeOf[T]()
	if err := r.checkReplace(t); err != nil {
		panic(err.Error())
	}
	r.seq++
	r.nodes[t] = &node{
		typ:      t,
		instance: v,
		state:    stateReady,
		deps:     map[reflect.Type]struct{}{},
		order:    r.seq,
	}
}

func (r *Registry) checkReplace(t reflect.Type) error {
	if r.down {
		return fmt.Errorf("registry: cannot replace %v after Shutdown", t)
	}
	if n, ok := r.nodes[t]; ok && n.construct != nil && n.state != stateIdle {
		return fmt.Errorf("registry: cannot replace %v: it has already been constructed", t)
	}
	for _, n := range r.sortedTypes() {
		dep := r.nodes[n]
		if dep.state == stateIdle {
			continue
		}
		if _, uses := dep.deps[t]; uses {
			return fmt.Errorf("registry: cannot replace %v: %v already depends on it", t, n)
		}
	}
	return nil
}
