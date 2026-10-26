package config

import (
	"context"
	"encoding/json"
	"fmt"
)

// KVGetter is the minimal contract for key-value backed config storage —
// implement it over a gorm table, Redis hash, etcd prefix, or anything else.
type KVGetter interface {
	All(ctx context.Context) (map[string][]byte, error)
}

// KVSource reads config from a key-value store. If Decrypt is set, every
// value is passed through it first, along with the row's key — pair with
// SecretBox.Open for AES-GCM encrypted-at-rest config bound to its key.
type KVSource struct {
	Store   KVGetter
	Decrypt func(key string, value []byte) ([]byte, error)
}

func (s KVSource) Load(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.Store.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(rows))
	for k, v := range rows {
		if s.Decrypt != nil {
			plain, err := s.Decrypt(k, v)
			if err != nil {
				return nil, fmt.Errorf("decrypting config key %q: %w", k, err)
			}
			v = plain
		}
		out[k] = json.RawMessage(v)
	}
	return out, nil
}
