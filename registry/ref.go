package registry

import (
	"fmt"
	"reflect"
)

// Ref is a lazy, type-safe handle to a service — useful as a struct field on
// dependency structs. The R field must point at the owning Registry (set it
// directly or via Inject).
type Ref[T any] struct {
	R *Registry
}

// Get resolves the service. Panics if unresolvable — pair with Validate() at
// boot so this can never fail at request time.
func (ref Ref[T]) Get() T {
	return MustResolve[T](ref.R)
}

// Inject populates every Ref[...] field of the struct pointed to by depsPtr
// with the given registry. Non-Ref fields are left untouched.
func Inject(r *Registry, depsPtr any) error {
	v := reflect.ValueOf(depsPtr)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("registry: Inject expects a pointer to struct, got %T", depsPtr)
	}
	v = v.Elem()
	regVal := reflect.ValueOf(r)
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() != reflect.Struct {
			continue
		}
		rField := f.FieldByName("R")
		if rField.IsValid() && rField.CanSet() && rField.Type() == regVal.Type() {
			rField.Set(regVal)
		}
	}
	return nil
}
