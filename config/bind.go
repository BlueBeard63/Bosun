package config

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/bluebeard63/bosun"
	"github.com/bluebeard63/bosun/registry"
)

// --- bindings ---

type binding struct {
	key   string
	typ   reflect.Type // the bound option type T; EnvSource decodes against it
	apply func(reg *registry.Registry, raw json.RawMessage) error
	// snapshot captures the Dynamic's current value and returns a func that
	// restores it; the watcher calls it before a key's first apply so the
	// default comes back when the key disappears from every source.
	snapshot func(reg *registry.Registry) (restore func(), err error)
}

var bindings []binding

// Bind associates a config key with option type T. On every change the key's
// JSON document is unmarshalled into a fresh *T and swapped into the app's
// *bosun.Dynamic[T]. When the key is later removed from every source, the
// value the Dynamic held before config was first applied is restored.
//
// A document is rejected, and the previous value kept, when it has a field T
// doesn't declare (a typo such as "PerMinite") or when T implements
// Validator and Validate returns an error.
func Bind[T any](key string) struct{} {
	dynType := reflect.TypeOf((**bosun.Dynamic[T])(nil)).Elem()
	resolve := func(reg *registry.Registry) (*bosun.Dynamic[T], error) {
		d, err := reg.ResolveType(dynType)
		if err != nil {
			return nil, fmt.Errorf("config key %q: %w", key, err)
		}
		return d.(*bosun.Dynamic[T]), nil
	}
	bindings = append(bindings, binding{
		key: key,
		typ: reflect.TypeOf((*T)(nil)).Elem(),
		apply: func(reg *registry.Registry, raw json.RawMessage) error {
			v := new(T)
			if err := decodeStrict(raw, v); err != nil {
				return fmt.Errorf("config key %q: %w", key, err)
			}
			if vd, ok := any(v).(Validator); ok {
				if err := vd.Validate(); err != nil {
					return fmt.Errorf("config key %q: invalid: %w", key, err)
				}
			}
			d, err := resolve(reg)
			if err != nil {
				return err
			}
			d.Set(v)
			return nil
		},
		snapshot: func(reg *registry.Registry) (func(), error) {
			d, err := resolve(reg)
			if err != nil {
				return nil, err
			}
			def := d.Get()
			return func() {
				v := new(T)
				if def != nil {
					*v = *def // shallow copy: each restore swaps in a fresh *T
				}
				d.Set(v)
			}, nil
		},
	})
	return struct{}{}
}
