// Package openapi serves an OpenAPI 3.0 spec generated from the typed routes
// of the app it is mounted on (app.TypedRoutes), with error responses merged from three layers: declared
// (bosun.Errors), scanned from handler source, and observed from traffic.
package openapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/bluebeard63/bosun"
)

type SpecController struct {
	app     *bosun.App  // injected: the app this controller is mounted on
	scanner *errScanner // injected (has injected ScanOptions itself)
}

var _ = bosun.Service[errScanner]()
var _ = bosun.Controller[SpecController]("")

func (c *SpecController) Routes(r *bosun.Router) {
	r.Get("/openapi.json", c.Spec)
}

func (c *SpecController) Spec(w http.ResponseWriter, req *http.Request) {
	schemas := map[string]any{}
	paths := map[string]map[string]any{}

	for _, rt := range c.app.TypedRoutes() {
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
			"operationId": shortHandler(rt.Handler),
			"responses":   responses,
		}

		params := []any{}
		for _, p := range pathParams {
			schema := any(map[string]any{"type": "string"})
			if sf, ok := fieldByTag(rt.In, "path", p); ok {
				schema = constrain(schemaFor(sf.Type, schemas), sf.Type, fieldRules(sf))
			}
			params = append(params, map[string]any{
				"name": p, "in": "path", "required": true, "schema": schema,
			})
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
					params = append(params, map[string]any{
						"name": name, "in": in, "required": hasRule(rules, "required"),
						"schema": constrain(schemaFor(sf.Type, schemas), sf.Type, rules),
					})
				}
			}
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if rt.Method == "POST" || rt.Method == "PUT" || rt.Method == "PATCH" {
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

		if paths[oaPath] == nil {
			paths[oaPath] = map[string]any{}
		}
		paths[oaPath][strings.ToLower(rt.Method)] = op
	}

	spec := map[string]any{
		"openapi": "3.0.3",
		"info":    map[string]any{"title": "bosun API", "version": "1.0.0"},
		"paths":   paths,
		"components": map[string]any{
			"schemas": schemas,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	writeJSON(w, spec)
}

// convertPath turns Go 1.22 patterns (/users/{id}) into OpenAPI form (same
// syntax, conveniently) and extracts param names.
func convertPath(p string) (string, []string) {
	params := []string{}
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			params = append(params, strings.Trim(seg, "{}"))
		}
	}
	return p, params
}

func shortHandler(full string) string {
	if idx := strings.LastIndex(full, "."); idx >= 0 {
		full = full[idx+1:]
	}
	return strings.TrimSuffix(full, "-fm")
}

func hasBodyFields(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if sf.IsExported() && !isParamField(sf) {
			return true
		}
	}
	return false
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

func writeJSON(w http.ResponseWriter, v any) {
	enc := jsonEncoder(w)
	_ = enc.Encode(v)
}

func jsonEncoder(w http.ResponseWriter) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc
}

// validationResponse is the 400 a typed route returns when binding or
// validation fails: ErrorResponse plus a list of field errors.
func validationResponse(schemas map[string]any) map[string]any {
	if _, ok := schemas["ValidationError"]; !ok {
		str := map[string]any{"type": "string"}
		schemas["ValidationError"] = map[string]any{
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
func errResponse(desc string, schemas map[string]any) map[string]any {
	if _, ok := schemas["ErrorResponse"]; !ok {
		schemas["ErrorResponse"] = map[string]any{
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
