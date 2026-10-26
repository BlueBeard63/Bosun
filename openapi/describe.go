package openapi

import (
	"sort"

	"github.com/bluebeard63/bosun"
)

// annotate adds the operation-level fields that come from route metadata
// and Options rather than from the handler's types: security requirements
// and x-permissions.
func (c *SpecController) annotate(op map[string]any, rt bosun.RouteInfo, perms []string) {
	if len(perms) > 0 {
		op["x-permissions"] = perms
		if sec := c.opts.security(perms); len(sec) > 0 {
			op["security"] = sec
		}
	}
}

// assemble wraps the paths and schemas in the top-level document.
func (c *SpecController) assemble(paths map[string]map[string]any, schemas *schemaSet, perms []string) map[string]any {
	components := map[string]any{"schemas": schemas.defs}
	if ss := c.opts.securitySchemes(dedupe(perms)); ss != nil {
		components["securitySchemes"] = ss
	}
	doc := map[string]any{
		"openapi":    "3.0.3",
		"info":       c.opts.info(),
		"paths":      paths,
		"components": components,
	}
	if servers := c.opts.servers(); len(servers) > 0 {
		doc["servers"] = servers
	}
	return doc
}

// dedupe returns the distinct values of ss, sorted.
func dedupe(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
