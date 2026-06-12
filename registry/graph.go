package registry

import (
	"fmt"
	"sort"
	"strings"
)

// Graph returns the discovered dependency graph as an adjacency list keyed by
// type name. Complete after Validate() (or after all services have been
// resolved at least once).
func (r *Registry) Graph() map[string][]string {
	out := make(map[string][]string, len(r.nodes))
	for _, t := range r.sortedTypes() {
		n := r.nodes[t]
		deps := make([]string, 0, len(n.deps))
		for d := range n.deps {
			deps = append(deps, d.String())
		}
		sort.Strings(deps)
		out[t.String()] = deps
	}
	return out
}

// DOT renders the dependency graph in Graphviz DOT format.
// Pipe into `dot -Tsvg` (or paste into an online viewer) to visualise.
func (r *Registry) DOT() string {
	var b strings.Builder
	b.WriteString("digraph services {\n")
	b.WriteString("  rankdir=LR;\n")
	b.WriteString("  node [shape=box, fontname=\"JetBrains Mono\"];\n")
	for _, t := range r.sortedTypes() {
		n := r.nodes[t]
		if n.construct == nil {
			// pre-built instance (e.g. *gorm.DB) — style as external resource
			fmt.Fprintf(&b, "  %q [style=filled, fillcolor=lightgrey];\n", t.String())
		} else {
			fmt.Fprintf(&b, "  %q;\n", t.String())
		}
		deps := make([]string, 0, len(n.deps))
		for d := range n.deps {
			deps = append(deps, d.String())
		}
		sort.Strings(deps)
		for _, d := range deps {
			fmt.Fprintf(&b, "  %q -> %q;\n", t.String(), d)
		}
	}
	b.WriteString("}\n")
	return b.String()
}
