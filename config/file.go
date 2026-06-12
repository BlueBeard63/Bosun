package config

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// FileSource reads a JSON document of {key: object} from disk. A missing
// file yields no values (so the file is optional); a malformed file is an
// error.
type FileSource struct {
	Path string
}

func (f FileSource) Load(ctx context.Context) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(f.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", f.Path, err)
	}
	return doc, nil
}
