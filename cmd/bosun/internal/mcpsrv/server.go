// Package mcpsrv is a minimal, dependency-free MCP (Model Context Protocol)
// server over stdio. It exposes the embedded Bosun documentation to AI clients
// as resources (one per doc page) and tools (search_docs, get_doc, list_topics).
// It speaks newline-delimited JSON-RPC 2.0, the MCP stdio transport.
//
// Install into Claude Code / Desktop:
//
//	claude mcp add bosun-docs -- bosun mcp
package mcpsrv

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/docsite"
)

const protocolVersion = "2024-11-05"

// Serve runs the MCP stdio loop until in reaches EOF or ctx is cancelled.
func Serve(ctx context.Context, site *docsite.Site, in io.Reader, out io.Writer, version string) error {
	s := &server{site: site, out: out, version: version}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		s.handleLine([]byte(line))
	}
	return sc.Err()
}

type server struct {
	site    *docsite.Site
	out     io.Writer
	version string
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *server) handleLine(line []byte) {
	var req rpcRequest
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeErr(nil, -32700, "parse error")
		return
	}
	// Notifications (no id) get no response.
	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		s.reply(req.ID, s.initialize(req.Params))
	case "notifications/initialized", "notifications/cancelled":
		// no-op notifications
	case "ping":
		s.reply(req.ID, map[string]any{})
	case "resources/list":
		s.reply(req.ID, s.resourcesList())
	case "resources/read":
		s.resourcesRead(req.ID, req.Params)
	case "tools/list":
		s.reply(req.ID, s.toolsList())
	case "tools/call":
		s.toolsCall(req.ID, req.Params)
	default:
		if !isNotification {
			s.writeErr(req.ID, -32601, "method not found: "+req.Method)
		}
	}
}

func (s *server) initialize(_ json.RawMessage) any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities": map[string]any{
			"resources": map[string]any{},
			"tools":     map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "bosun-docs",
			"version": s.version,
		},
	}
}

// --- resources ---

func resourceURI(slug string) string { return "bosun://docs/" + slug }

func (s *server) resourcesList() any {
	var res []map[string]any
	for _, e := range s.site.Entries() {
		res = append(res, map[string]any{
			"uri":      resourceURI(e.Slug),
			"name":     e.Title,
			"mimeType": "text/markdown",
		})
	}
	return map[string]any{"resources": res}
}

func (s *server) resourcesRead(id, params json.RawMessage) {
	var p struct {
		URI string `json:"uri"`
	}
	_ = json.Unmarshal(params, &p)
	slug := strings.TrimPrefix(p.URI, "bosun://docs/")
	md, ok := s.site.Markdown(slug)
	if !ok {
		s.writeErr(id, -32602, "unknown resource: "+p.URI)
		return
	}
	s.reply(id, map[string]any{
		"contents": []map[string]any{
			{"uri": p.URI, "mimeType": "text/markdown", "text": md},
		},
	})
}

// --- tools ---

func (s *server) toolsList() any {
	strSchema := func(prop, desc string) map[string]any {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				prop: map[string]any{"type": "string", "description": desc},
			},
			"required": []string{prop},
		}
	}
	return map[string]any{
		"tools": []map[string]any{
			{
				"name":        "search_docs",
				"description": "Full-text search the Bosun documentation. Returns ranked pages with snippets.",
				"inputSchema": strSchema("query", "Search terms"),
			},
			{
				"name":        "get_doc",
				"description": "Return the full Markdown of a Bosun documentation page by its slug (see list_topics).",
				"inputSchema": strSchema("slug", "Page slug, e.g. 'events' or 'getting-started'"),
			},
			{
				"name":        "list_topics",
				"description": "List every Bosun documentation page (slug + title).",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
			},
		},
	}
}

func (s *server) toolsCall(id, params json.RawMessage) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		s.writeErr(id, -32602, "invalid params")
		return
	}
	switch p.Name {
	case "search_docs":
		var a struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		s.reply(id, toolText(s.searchText(a.Query)))
	case "get_doc":
		var a struct {
			Slug string `json:"slug"`
		}
		_ = json.Unmarshal(p.Arguments, &a)
		md, ok := s.site.Markdown(a.Slug)
		if !ok {
			s.reply(id, toolError("no page named '"+a.Slug+"' (try list_topics)"))
			return
		}
		s.reply(id, toolText(md))
	case "list_topics":
		var b strings.Builder
		for _, e := range s.site.Entries() {
			fmt.Fprintf(&b, "- %s — %s\n", e.Slug, e.Title)
		}
		s.reply(id, toolText(b.String()))
	default:
		s.reply(id, toolError("unknown tool: "+p.Name))
	}
}

func (s *server) searchText(query string) string {
	hits := s.site.Search(query)
	if len(hits) == 0 {
		return "No matches for: " + query
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d result(s) for %q:\n\n", len(hits), query)
	for _, h := range hits {
		fmt.Fprintf(&b, "## %s (slug: %s)\n%s\n\n", h.Title, h.Slug, h.Snippet)
	}
	return b.String()
}

func toolText(text string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

// --- transport ---

func (s *server) reply(id json.RawMessage, result any) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) writeErr(id json.RawMessage, code int, msg string) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *server) write(resp rpcResponse) {
	b, err := json.Marshal(resp)
	if err != nil {
		return
	}
	// Newline-delimited: one JSON object per line.
	_, _ = s.out.Write(append(b, '\n'))
}
