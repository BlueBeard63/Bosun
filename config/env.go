package config

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
)

// EnvSource builds config from environment variables (or any KEY=VALUE list,
// e.g. a parsed .env file). For a bind key "ratelimit", variables prefixed
// RATELIMIT_ are collected: RATELIMIT_PERMINUTE=100 becomes
// {"PERMINUTE": 100}, which encoding/json matches case-insensitively to the
// PerMinute field. Numeric and boolean values are typed automatically.
type EnvSource struct {
	Environ func() []string // defaults to os.Environ
}

func (e EnvSource) Load(ctx context.Context) (map[string]json.RawMessage, error) {
	environ := e.Environ
	if environ == nil {
		environ = os.Environ
	}
	vars := environ()
	out := map[string]json.RawMessage{}
	for _, b := range bindings {
		prefix := strings.ToUpper(b.key) + "_"
		fields := map[string]json.RawMessage{}
		for _, kv := range vars {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || !strings.HasPrefix(k, prefix) {
				continue
			}
			fields[strings.TrimPrefix(k, prefix)] = typedJSON(v)
		}
		if len(fields) > 0 {
			doc, _ := json.Marshal(fields)
			out[b.key] = doc
		}
	}
	return out, nil
}

func typedJSON(v string) json.RawMessage {
	if _, err := strconv.ParseInt(v, 10, 64); err == nil {
		return json.RawMessage(v)
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return json.RawMessage(v)
	}
	if v == "true" || v == "false" {
		return json.RawMessage(v)
	}
	doc, _ := json.Marshal(v)
	return doc
}
