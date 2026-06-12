// Package registry is a type-keyed dependency injection container with
// automatic graph discovery, cycle detection, and lifecycle management.
package registry

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

type nodeState int

const (
	stateIdle nodeState = iota
	stateResolving
	stateReady
)

type node struct {
	typ       reflect.Type
	construct func(*Registry) (any, error)
	instance  any
	state     nodeState
	deps      map[reflect.Type]struct{} // edges discovered during construction
	order     int                       // completion sequence, used for shutdown ordering
}

// Registry holds service providers and built singletons.
type Registry struct {
	nodes map[reflect.Type]*node
	stack []*node // active resolution chain: cycle detection + edge recording
	seq   int
}

// New creates an empty Registry.
func New() *Registry {
	return &Registry{nodes: map[reflect.Type]*node{}}
}

// typeOf returns the reflect.Type of T, working for interfaces too.
func typeOf[T any]() reflect.Type {
	return reflect.TypeOf((*T)(nil)).Elem()
}

// Register adds a lazy constructor for T. T may be an interface type bound to
// a concrete implementation, or a concrete (usually pointer) type.
// Panics on duplicate registration — that is always a programming error.
func Register[T any](r *Registry, construct func(*Registry) (T, error)) {
	t := typeOf[T]()
	if _, dup := r.nodes[t]; dup {
		panic(fmt.Sprintf("registry: duplicate provider for %v", t))
	}
	r.nodes[t] = &node{
		typ: t,
		construct: func(reg *Registry) (any, error) {
			return construct(reg)
		},
		deps: map[reflect.Type]struct{}{},
	}
}

// RegisterInstance adds an already-constructed value as a ready singleton —
// ideal for externally created resources like *gorm.DB, *http.Client,
// loggers, or configuration structs. Instances are leaf nodes in the graph.
func RegisterInstance[T any](r *Registry, v T) {
	t := typeOf[T]()
	if _, dup := r.nodes[t]; dup {
		panic(fmt.Sprintf("registry: duplicate provider for %v", t))
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

// Resolve returns the singleton of T, constructing it (and, recursively, its
// dependencies) on first use. Calling Resolve inside a constructor is how the
// dependency graph is discovered.
func Resolve[T any](r *Registry) (T, error) {
	v, err := r.resolve(typeOf[T]())
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}

// MustResolve is Resolve but panics on error. Intended for use after a
// successful Validate(), when failure is impossible.
func MustResolve[T any](r *Registry) T {
	v, err := Resolve[T](r)
	if err != nil {
		panic(err)
	}
	return v
}

func (r *Registry) resolve(t reflect.Type) (any, error) {
	n, ok := r.nodes[t]
	if !ok {
		return nil, fmt.Errorf("registry: no provider registered for %v%s", t, r.chainSuffix())
	}

	// Record the edge parent -> t while a construction is in flight.
	if len(r.stack) > 0 {
		r.stack[len(r.stack)-1].deps[t] = struct{}{}
	}

	switch n.state {
	case stateReady:
		return n.instance, nil
	case stateResolving:
		return nil, fmt.Errorf("registry: dependency cycle detected: %s", r.cycleString(t))
	}

	n.state = stateResolving
	r.stack = append(r.stack, n)
	inst, err := n.construct(r)
	r.stack = r.stack[:len(r.stack)-1]

	if err != nil {
		n.state = stateIdle
		return nil, fmt.Errorf("registry: constructing %v: %w", t, err)
	}

	n.instance = inst
	n.state = stateReady
	r.seq++
	n.order = r.seq
	return inst, nil
}

func (r *Registry) chainSuffix() string {
	if len(r.stack) == 0 {
		return ""
	}
	parts := make([]string, len(r.stack))
	for i, n := range r.stack {
		parts[i] = n.typ.String()
	}
	return " (required by: " + strings.Join(parts, " -> ") + ")"
}

func (r *Registry) cycleString(repeat reflect.Type) string {
	parts := []string{}
	for _, n := range r.stack {
		parts = append(parts, n.typ.String())
	}
	parts = append(parts, repeat.String())
	return strings.Join(parts, " -> ")
}

func (r *Registry) sortedTypes() []reflect.Type {
	types := make([]reflect.Type, 0, len(r.nodes))
	for t := range r.nodes {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool { return types[i].String() < types[j].String() })
	return types
}
