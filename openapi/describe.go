package openapi

import (
	"reflect"
	"sort"
	"strings"
	"unicode"

	"github.com/bluebeard63/bosun"
)

// annotate adds the operation-level fields that come from route metadata
// and Options rather than from the handler's types: summary and
// description, security requirements and x-permissions.
func (c *SpecController) annotate(op map[string]any, rt bosun.RouteInfo, perms []string) {
	summary, description := operationDoc(rt)
	if summary != "" {
		op["summary"] = summary
	}
	if description != "" {
		op["description"] = description
	}
	if len(perms) > 0 {
		op["x-permissions"] = perms
		if sec := c.opts.security(perms); len(sec) > 0 {
			op["security"] = sec
		}
	}
}

// operationDoc returns the route's summary and description: the first
// sentence and the rest of the handler's doc comment (registered by the
// file bosun gen registry writes), each replaced by bosun.Doc's when set.
func operationDoc(rt bosun.RouteInfo) (summary, description string) {
	summary, description = splitDoc(bosun.DocComment(handlerKey(rt.Handler)))
	if d, ok := rt.Meta[bosun.MetaDoc].(bosun.RouteDoc); ok {
		if d.Summary != "" {
			summary = d.Summary
		}
		if d.Description != "" {
			description = d.Description
		}
	}
	return summary, description
}

// splitDoc splits a doc comment into its first sentence, on one line, and
// the rest. The first sentence ends at the first period followed by space
// and not by a lower-case letter ("e.g. this" doesn't end it), or at the
// end of the first paragraph.
func splitDoc(text string) (first, rest string) {
	text = strings.TrimSpace(text)
	para, after, _ := strings.Cut(text, "\n\n")
	end := len(para)
	for i := 0; i+1 < len(para); i++ {
		if para[i] != '.' || !unicode.IsSpace(rune(para[i+1])) {
			continue
		}
		next := strings.TrimLeftFunc(para[i+1:], unicode.IsSpace)
		if next == "" || !unicode.IsLower([]rune(next)[0]) {
			end = i + 1
			break
		}
	}
	first = strings.Join(strings.Fields(para[:end]), " ")
	rest = strings.TrimSpace(para[end:])
	if after = strings.TrimSpace(after); after != "" {
		if rest != "" {
			rest += "\n\n"
		}
		rest += after
	}
	return first, rest
}

// handlerKey turns a handler's runtime name into its RegisterDocComments
// key: "example.com/app/notes.(*NotesController).Get-fm" becomes
// "example.com/app/notes.(*NotesController).Get".
func handlerKey(name string) string {
	pkg, rest := splitFuncName(name)
	if pkg == "" {
		return ""
	}
	return pkg + "." + strings.TrimSuffix(strings.ReplaceAll(rest, "[...]", ""), "-fm")
}

// typeKey is the RegisterDocComments key of a named type, without type
// arguments: "example.com/app/notes.Page" for notes.Page[Note].
func typeKey(t reflect.Type) string {
	name := t.Name()
	if i := strings.IndexByte(name, '['); i >= 0 {
		name = name[:i]
	}
	if name == "" || t.PkgPath() == "" {
		return ""
	}
	return t.PkgPath() + "." + name
}

// fieldDoc returns the doc comment of field sf declared in struct owner.
func fieldDoc(owner reflect.Type, sf reflect.StructField) string {
	if k := typeKey(owner); k != "" {
		return bosun.DocComment(k + "." + sf.Name)
	}
	return ""
}

// setDoc sets a parameter's description from its field's doc comment.
func setDoc(param map[string]any, owner reflect.Type, sf reflect.StructField) {
	if doc := fieldDoc(owner, sf); doc != "" {
		param["description"] = doc
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
