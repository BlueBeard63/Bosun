package openapi

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

// schemaSet collects components/schemas while one spec is built. Named
// struct types are given their component names up front, from the whole
// set of types the routes reach, so a name never depends on which route
// happened to mention its type first.
type schemaSet struct {
	defs  map[string]any          // component name -> schema
	names map[reflect.Type]string // assigned component names
	used  map[string]bool
}

// builtinSchemas are the component names the generator defines itself.
var builtinSchemas = []string{"ErrorResponse", "ValidationError"}

func newSchemaSet(roots []reflect.Type) *schemaSet {
	s := &schemaSet{defs: map[string]any{}, used: map[string]bool{}}
	s.names = schemaNames(namedStructs(roots))
	for _, n := range builtinSchemas {
		s.used[n] = true
	}
	for _, n := range s.names {
		s.used[n] = true
	}
	return s
}

// nameOf returns t's component name. Types newSchemaSet didn't see get the
// first free name on demand.
func (s *schemaSet) nameOf(t reflect.Type) string {
	if n, ok := s.names[t]; ok {
		return n
	}
	n := uniqueID(schemaName(t, 1), s.used)
	s.names[t] = n
	return n
}

// namedStructs returns every named struct type reachable from roots
// through pointers, slices, arrays, maps and struct fields.
func namedStructs(roots []reflect.Type) []reflect.Type {
	seen := map[reflect.Type]bool{}
	var out []reflect.Type
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice ||
			t.Kind() == reflect.Array || t.Kind() == reflect.Map {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || t == reflect.TypeOf(time.Time{}) || seen[t] {
			return
		}
		seen[t] = true
		if t.Name() != "" {
			out = append(out, t)
		}
		for i := 0; i < t.NumField(); i++ {
			walk(t.Field(i).Type)
		}
	}
	for _, t := range roots {
		walk(t)
	}
	return out
}

// schemaNames assigns each type a component name: its package's last
// import path element plus its type name, users.Out -> UsersOut. Types in
// package main keep their bare name. When two types would share a name,
// both are qualified with more path elements (v1/users.Out -> V1UsersOut);
// a collision that remains is broken with a numeric suffix in import path
// order. The result depends only on the set of types, not their order.
func schemaNames(types []reflect.Type) map[reflect.Type]string {
	sort.Slice(types, func(i, j int) bool { return fullName(types[i]) < fullName(types[j]) })
	depth := make([]int, len(types))
	for i := range depth {
		depth[i] = 1
	}
	reserved := map[string]bool{}
	for _, n := range builtinSchemas {
		reserved[n] = true
	}
	for {
		groups := map[string][]int{}
		for i, t := range types {
			n := schemaName(t, depth[i])
			groups[n] = append(groups[n], i)
		}
		changed := false
		for n, idx := range groups {
			if len(idx) < 2 && !reserved[n] {
				continue
			}
			for _, i := range idx {
				if depth[i] < pathDepth(types[i]) {
					depth[i]++
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	names := map[reflect.Type]string{}
	used := map[string]bool{}
	for n := range reserved {
		used[n] = true
	}
	for i, t := range types {
		names[t] = uniqueID(schemaName(t, depth[i]), used)
	}
	return names
}

func fullName(t reflect.Type) string { return t.PkgPath() + "." + t.Name() }

func pathDepth(t reflect.Type) int {
	if t.PkgPath() == "main" {
		return 0
	}
	return len(strings.Split(t.PkgPath(), "/"))
}

// typeArgPath matches the import path part of a type argument, such as
// "example.com/app/" in "Page[example.com/app/users.Out]".
var typeArgPath = regexp.MustCompile(`[^\[\],*\s]*/`)

// schemaName builds a component name from the last depth elements of t's
// import path and its type name, keeping only letters and digits:
// Page[example.com/app/users.Out] in package api is ApiPageUsersOut.
func schemaName(t reflect.Type, depth int) string {
	var prefix []string
	if t.PkgPath() != "main" {
		segs := strings.Split(t.PkgPath(), "/")
		if depth > len(segs) {
			depth = len(segs)
		}
		prefix = segs[len(segs)-depth:]
	}
	name := typeArgPath.ReplaceAllString(t.Name(), "")
	return identFromText(strings.Join(append(prefix, name), "/"))
}
