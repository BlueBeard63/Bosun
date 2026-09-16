package config

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/bluebeard63/bosun/registry"
)

// Source produces configuration as bind-key -> JSON document. Implementations
// must be safe for repeated calls; Load is invoked on every poll.
type Source interface {
	Load(ctx context.Context) (map[string]json.RawMessage, error)
}

// --- source registration ---

var pendingSources []func(*registry.Registry) (Source, error)

// Add registers a ready-made source (file, env, or your own).
func Add(s Source) struct{} {
	pendingSources = append(pendingSources, func(*registry.Registry) (Source, error) {
		return s, nil
	})
	return struct{}{}
}

// AddSource registers a source resolved from the registry, for sources with
// injected dependencies (e.g. a *gorm.DB-backed implementation declared with
// bosun.Service[T]()).
func AddSource[T any]() struct{} {
	t := reflect.TypeOf((*T)(nil))
	pendingSources = append(pendingSources, func(reg *registry.Registry) (Source, error) {
		v, err := reg.ResolveType(t)
		if err != nil {
			return nil, err
		}
		s, ok := v.(Source)
		if !ok {
			return nil, fmt.Errorf("config: %v does not implement config.Source", t)
		}
		return s, nil
	})
	return struct{}{}
}
