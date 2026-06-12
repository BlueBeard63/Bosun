package registry

import (
	"errors"
	"fmt"
	"io"
	"sort"
)

// Validate eagerly constructs every registered service in deterministic
// order, surfacing missing providers, constructor errors, and cycles at boot.
func (r *Registry) Validate() error {
	types := r.sortedTypes()
	var errs []error
	for _, t := range types {
		if _, err := r.resolve(t); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Shutdown closes every constructed service that implements io.Closer, in
// reverse construction order — dependents are closed before the things they
// depend on (so e.g. AuthService closes before *gorm.DB).
func (r *Registry) Shutdown() error {
	ready := []*node{}
	for _, n := range r.nodes {
		if n.state == stateReady {
			ready = append(ready, n)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i].order > ready[j].order })

	var errs []error
	for _, n := range ready {
		if c, ok := n.instance.(io.Closer); ok {
			if err := c.Close(); err != nil {
				errs = append(errs, fmt.Errorf("registry: closing %v: %w", n.typ, err))
			}
		}
		n.state = stateIdle
		n.instance = nil
	}
	return errors.Join(errs...)
}
