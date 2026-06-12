package config

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/amberstack/bosun"
	"github.com/amberstack/bosun/registry"
)

// --- bindings ---

type binding struct {
	key   string
	apply func(reg *registry.Registry, raw json.RawMessage) error
}

var bindings []binding

// Bind associates a config key with option type T. On every change the key's
// JSON document is unmarshalled into a fresh *T and swapped into the app's
// *bosun.Dynamic[T].
func Bind[T any](key string) struct{} {
	dynType := reflect.TypeOf((**bosun.Dynamic[T])(nil)).Elem()
	bindings = append(bindings, binding{
		key: key,
		apply: func(reg *registry.Registry, raw json.RawMessage) error {
			v := new(T)
			if err := json.Unmarshal(raw, v); err != nil {
				return fmt.Errorf("config key %q: %w", key, err)
			}
			d, err := reg.ResolveType(dynType)
			if err != nil {
				return fmt.Errorf("config key %q: %w", key, err)
			}
			d.(*bosun.Dynamic[T]).Set(v)
			return nil
		},
	})
	return struct{}{}
}
