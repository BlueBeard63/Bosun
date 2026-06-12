package registry

import "reflect"

// Has reports whether a provider or instance is registered for t.
func (r *Registry) Has(t reflect.Type) bool {
	_, ok := r.nodes[t]
	return ok
}

// ResolveType is Resolve for callers that only have a reflect.Type
// (used by reflection-based injectors).
func (r *Registry) ResolveType(t reflect.Type) (any, error) {
	return r.resolve(t)
}

// RegisterType registers a constructor for a runtime-known type.
func (r *Registry) RegisterType(t reflect.Type, construct func(*Registry) (any, error)) {
	if _, dup := r.nodes[t]; dup {
		panic("registry: duplicate provider for " + t.String())
	}
	r.nodes[t] = &node{
		typ:       t,
		construct: construct,
		deps:      map[reflect.Type]struct{}{},
	}
}
