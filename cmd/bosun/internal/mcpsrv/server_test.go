package mcpsrv

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/docsite"
)

// drive feeds newline-delimited JSON-RPC requests through Serve and returns the
// parsed responses.
func drive(t *testing.T, reqs ...string) []map[string]any {
	t.Helper()
	site, err := docsite.Load()
	if err != nil {
		t.Fatalf("load site: %v", err)
	}
	in := strings.NewReader(strings.Join(reqs, "\n") + "\n")
	var out strings.Builder
	if err := Serve(context.Background(), site, in, &out, "test"); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var responses []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("bad response line %q: %v", line, err)
		}
		responses = append(responses, m)
	}
	return responses
}

func result(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	r, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", resp)
	}
	return r
}

func TestInitialize(t *testing.T) {
	r := drive(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if len(r) != 1 {
		t.Fatalf("expected 1 response, got %d", len(r))
	}
	res := result(t, r[0])
	if res["protocolVersion"] != protocolVersion {
		t.Fatalf("protocol version = %v", res["protocolVersion"])
	}
	info := res["serverInfo"].(map[string]any)
	if info["name"] != "bosun-docs" {
		t.Fatalf("serverInfo.name = %v", info["name"])
	}
}

func TestNotificationHasNoResponse(t *testing.T) {
	// A notification (no id) must not produce a response line.
	r := drive(t,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`,
	)
	if len(r) != 1 {
		t.Fatalf("expected only the ping response, got %d responses", len(r))
	}
}

func TestToolsListAndSearch(t *testing.T) {
	r := drive(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"search_docs","arguments":{"query":"middleware"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_doc","arguments":{"slug":"getting-started"}}}`,
	)
	tools := result(t, r[0])["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}
	// search_docs returns text content mentioning the middleware page.
	sc := result(t, r[1])
	txt := sc["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(strings.ToLower(txt), "middleware") {
		t.Fatalf("search text missing 'middleware': %q", txt)
	}
	// get_doc returns the raw markdown.
	gc := result(t, r[2])
	gtxt := gc["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(gtxt, "#") {
		t.Fatalf("get_doc did not return markdown")
	}
}

func TestResources(t *testing.T) {
	r := drive(t,
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"bosun://docs/getting-started"}}`,
	)
	res := result(t, r[0])["resources"].([]any)
	if len(res) == 0 {
		t.Fatal("no resources listed")
	}
	first := res[0].(map[string]any)
	if !strings.HasPrefix(first["uri"].(string), "bosun://docs/") {
		t.Fatalf("bad resource uri %v", first["uri"])
	}
	read := result(t, r[1])["contents"].([]any)[0].(map[string]any)
	if read["mimeType"] != "text/markdown" || read["text"] == "" {
		t.Fatalf("resource read wrong: %v", read)
	}
}

func TestUnknownMethodErrors(t *testing.T) {
	r := drive(t, `{"jsonrpc":"2.0","id":1,"method":"does/not/exist"}`)
	if _, ok := r[0]["error"]; !ok {
		t.Fatalf("expected error for unknown method, got %v", r[0])
	}
}
