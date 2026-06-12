package bosun

import (
	"fmt"
	"reflect"
	"unsafe"

	"github.com/amberstack/bosun/registry"
)

// --- reflective construction ---

func buildInjected(r *registry.Registry, t reflect.Type) (any, error) {
	st := t.Elem()
	if st.Kind() != reflect.Struct {
		return nil, fmt.Errorf("bosun: %v is not a struct type", st)
	}
	v := reflect.New(st)
	elem := v.Elem()

	for i := 0; i < st.NumField(); i++ {
		sf := st.Field(i)
		if sf.Anonymous || sf.Tag.Get("inject") == "-" {
			continue
		}
		if !r.Has(sf.Type) {
			continue // plain data field — leave zero-valued
		}
		dep, err := r.ResolveType(sf.Type)
		if err != nil {
			return nil, fmt.Errorf("injecting %s.%s: %w", st.Name(), sf.Name, err)
		}
		f := elem.Field(i)
		if !f.CanSet() {
			f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
		}
		f.Set(reflect.ValueOf(dep))
	}

	inst := v.Interface()
	if in, ok := inst.(initer); ok {
		if err := in.Init(); err != nil {
			return nil, fmt.Errorf("%s.Init: %w", st.Name(), err)
		}
	}
	return inst, nil
}
