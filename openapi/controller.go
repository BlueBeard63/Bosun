// Package openapi serves an OpenAPI 3.0 spec generated from the typed routes
// of the app it is mounted on (app.TypedRoutes), with error responses merged from three layers: declared
// (bosun.Errors), scanned from handler source, and observed from traffic.
package openapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/bluebeard63/bosun"
)

type SpecController struct {
	app     *bosun.App   // injected: the app this controller is mounted on
	scanner *errScanner  // injected (has injected ScanOptions itself)
	opts    *Options     // injected: info, servers, security schemes
	docs    *DocsOptions // injected: interactive docs page

	docsPage docsPage // rendered by Docs, set in Routes

	mu     sync.Mutex
	doc    []byte // cached spec document
	docKey string // routes and observed statuses doc was built from
	builds int    // times the document was built, for tests
}

var _ = bosun.Service[errScanner]()
var _ = bosun.Controller[SpecController]("")

func (c *SpecController) Routes(r *bosun.Router) {
	r.Get(specPath, c.Spec)
	c.docsRoutes(r)
}

func (c *SpecController) Spec(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(c.document())
}

// document returns the encoded spec. The route list is complete once the
// app has started, so the spec is built on the first request and the bytes
// are reused; it is rebuilt only when the observed-status layer has
// learned a new code since.
func (c *SpecController) document() []byte {
	routes := c.app.TypedRoutes()
	var key strings.Builder
	key.WriteString(strconv.Itoa(len(routes)))
	for _, rt := range routes {
		key.WriteByte(';')
		for _, code := range c.app.ObservedStatuses(rt.Method, rt.Path) {
			if code >= 400 {
				key.WriteString(strconv.Itoa(code) + ",")
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.doc == nil || c.docKey != key.String() {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		_ = enc.Encode(c.build(routes))
		c.doc, c.docKey = buf.Bytes(), key.String()
		c.builds++
	}
	return c.doc
}

func (c *SpecController) build(routes []bosun.RouteInfo) map[string]any {
	paths := map[string]map[string]any{}

	// Sorted so de-duplicated operationIds don't depend on mount order.
	sort.SliceStable(routes, func(i, j int) bool {
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	opIDs := map[string]bool{}
	var allPerms []string // every route permission, for oauth2 scopes

	var roots []reflect.Type
	for _, rt := range routes {
		roots = append(roots, rt.In, rt.Out)
	}
	schemas := newSchemaSet(roots)

	for _, rt := range routes {
		oaPath, pathParams := convertPath(rt.Path)
		responses := map[string]any{
			"200": map[string]any{
				"description": "OK",
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": schemaRef(rt.Out, schemas),
					},
				},
			},
		}
		// Error responses come from three runtime-capable layers:
		// declared via bosun.Errors(...), discovered by scanning handler
		// source (on disk in dev, embedded via openapi.Sources in prod),
		// and observed from real traffic since process start.
		codes := map[int]bool{}
		for _, code := range rt.Declared {
			codes[code] = true
		}
		for _, code := range c.scanner.statusesFor(rt.Handler) {
			codes[code] = true
		}
		for _, code := range c.app.ObservedStatuses(rt.Method, rt.Path) {
			if code >= 400 {
				codes[code] = true
			}
		}
		for code := range codes {
			responses[strconv.Itoa(code)] = errResponse(http.StatusText(code), schemas)
		}
		if rt.In.Kind() == reflect.Struct && rt.In.NumField() > 0 {
			// Binding and validation failures carry field-level detail.
			responses["400"] = validationResponse(schemas)
		}
		if _, ok := responses["500"]; !ok {
			responses["500"] = errResponse("internal server error", schemas)
		}

		op := map[string]any{
			"operationId": uniqueID(operationID(rt), opIDs),
			"responses":   responses,
		}

		params := []any{}
		for _, p := range pathParams {
			schema := any(map[string]any{"type": "string"})
			if sf, ok := fieldByTag(rt.In, "path", p); ok {
				schema = decorate(constrain(schemaFor(sf.Type, schemas), sf.Type, fieldRules(sf)), sf, false)
			}
			param := map[string]any{
				"name": p, "in": "path", "required": true, "schema": schema,
			}
			if sf, ok := fieldByTag(rt.In, "path", p); ok {
				setDoc(param, rt.In, sf)
			}
			params = append(params, param)
		}
		// query + header params from the In type
		if rt.In.Kind() == reflect.Struct {
			for _, in := range []string{"query", "header"} {
				for i := 0; i < rt.In.NumField(); i++ {
					sf := rt.In.Field(i)
					name := sf.Tag.Get(in)
					if name == "" || sf.Tag.Get("path") != "" || (in == "header" && sf.Tag.Get("query") != "") {
						continue // binding uses the first tag in path, query, header order
					}
					rules := fieldRules(sf)
					param := map[string]any{
						"name": name, "in": in, "required": hasRule(rules, "required"),
						"schema": decorate(constrain(schemaFor(sf.Type, schemas), sf.Type, rules), sf, false),
					}
					setDoc(param, rt.In, sf)
					params = append(params, param)
				}
			}
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		// bind decodes a JSON body for any method; GET has no body semantics.
		if rt.Method == "POST" || rt.Method == "PUT" || rt.Method == "PATCH" || rt.Method == "DELETE" {
			if hasBodyFields(rt.In) {
				op["requestBody"] = map[string]any{
					"required": true,
					"content": map[string]any{
						"application/json": map[string]any{
							"schema": schemaRef(rt.In, schemas),
						},
					},
				}
			}
		}

		perms := permissions(rt)
		allPerms = append(allPerms, perms...)
		c.annotate(op, rt, perms)

		if paths[oaPath] == nil {
			paths[oaPath] = map[string]any{}
		}
		paths[oaPath][strings.ToLower(rt.Method)] = op
	}

	return c.assemble(paths, schemas, allPerms)
}

// convertPath turns Go 1.22 patterns into OpenAPI form and extracts param
// names. {name} is the same in both; the {name...} wildcard becomes {name},
// the name the router binds, and the {$} end anchor is dropped.
func convertPath(p string) (string, []string) {
	params := []string{}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if seg == "{$}" {
			segs[i] = ""
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			name := strings.TrimSuffix(seg[1:len(seg)-1], "...")
			segs[i] = "{" + name + "}"
			params = append(params, name)
		}
	}
	return strings.Join(segs, "/"), params
}

// operationID names an operation <Controller><Method>, e.g. NotesGet for
// (*NotesController).Get. Handlers that aren't methods fall back to the
// HTTP method and path: get_files_path for GET /files/{path...}.
func operationID(rt bosun.RouteInfo) string {
	if _, recv, method, ok := parseHandler(rt.Handler); ok {
		if base := strings.TrimSuffix(recv, "Controller"); base != "" {
			recv = base
		}
		return recv + method
	}
	id := strings.ToLower(rt.Method)
	for _, word := range strings.FieldsFunc(rt.Path, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		id += "_" + word
	}
	return id
}

// uniqueID returns id, or id with the first free numeric suffix (id2,
// id3, ...) when another operation already uses it.
func uniqueID(id string, used map[string]bool) string {
	out := id
	for n := 2; used[out]; n++ {
		out = id + strconv.Itoa(n)
	}
	used[out] = true
	return out
}

func hasBodyFields(t reflect.Type) bool {
	return len(jsonFields(t)) > 0
}

// fieldByTag finds the In field bound from source (path/query/header) name.
func fieldByTag(t reflect.Type, source, name string) (reflect.StructField, bool) {
	if t.Kind() != reflect.Struct {
		return reflect.StructField{}, false
	}
	for i := 0; i < t.NumField(); i++ {
		if sf := t.Field(i); sf.Tag.Get(source) == name {
			return sf, true
		}
	}
	return reflect.StructField{}, false
}

// validationResponse is the 400 a typed route returns when binding or
// validation fails: ErrorResponse plus a list of field errors.
func validationResponse(schemas *schemaSet) map[string]any {
	if _, ok := schemas.defs["ValidationError"]; !ok {
		str := map[string]any{"type": "string"}
		schemas.defs["ValidationError"] = map[string]any{
			"type":     "object",
			"required": []string{"error"},
			"properties": map[string]any{
				"error": str,
				"fields": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type":     "object",
						"required": []string{"field", "rule", "message"},
						"properties": map[string]any{
							"field":   str,
							"in":      map[string]any{"type": "string", "enum": []string{"body", "path", "query", "header", "form"}},
							"rule":    str,
							"param":   str,
							"message": str,
						},
					},
				},
			},
		}
	}
	return map[string]any{
		"description": "invalid request body or parameters",
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{"$ref": "#/components/schemas/ValidationError"},
			},
		},
	}
}

// errResponse builds an OpenAPI response object pointing at the shared
// ErrorResponse schema (the {"error": "..."} shape bosun returns).
func errResponse(desc string, schemas *schemaSet) map[string]any {
	if _, ok := schemas.defs["ErrorResponse"]; !ok {
		schemas.defs["ErrorResponse"] = map[string]any{
			"type": "object",
			"properties": map[string]any{
				"error": map[string]any{"type": "string"},
			},
		}
	}
	return map[string]any{
		"description": desc,
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{"$ref": "#/components/schemas/ErrorResponse"},
			},
		},
	}
}
