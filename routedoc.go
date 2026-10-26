package bosun

import (
	"strings"
	"sync"
)

// --- route documentation ---

// MetaDoc is the Meta key bosun.Doc stores a RouteDoc under. OpenAPI
// generation reads it for the operation's summary and description.
const MetaDoc = "doc"

// RouteDoc is the human-readable description of a route, set with
// bosun.Doc and read back as rt.Meta[bosun.MetaDoc].(bosun.RouteDoc).
type RouteDoc struct {
	Summary     string // one line
	Description string // CommonMark
}

// Doc describes a route for API documentation: OpenAPI uses summary and
// description for the operation. It overrides the handler's doc comment
// (see RegisterDocComments) field by field, so Doc("Delete a note", "")
// keeps the description taken from the comment:
//
//	bosun.Delete(r, "/{id}", c.Delete, bosun.Doc("Delete a note",
//	    "Deleting a note that doesn't exist succeeds."))
//
// Like any Meta, it is accepted on typed and raw routes, groups and
// controllers; the innermost one wins.
func Doc(summary, description string) MWRef {
	return Meta(MetaDoc, RouteDoc{Summary: summary, Description: description})
}

var (
	docCommentsMu sync.RWMutex
	docComments   = map[string]string{}
)

// RegisterDocComments records doc comments for API documentation. It is
// called by the file `bosun gen registry` generates, which collects the
// comments on typed handlers and on struct types and their fields:
//
//	var _ = bosun.RegisterDocComments(map[string]string{
//	    "example.com/app/notes.(*NotesController).Get": "Get returns one note.",
//	    "example.com/app/notes.Note":                   "Note is a saved note.",
//	    "example.com/app/notes.Note.Title":             "Title is shown in lists.",
//	})
//
// Keys are the package import path ("main" for package main) followed by
// "." and the declaration: "(*T).Method" or "T.Method" for a method by
// receiver, "Func" for a function, "T" for a type (without type
// parameters) and "T.Field" for a struct field. Later registrations replace
// earlier ones for the same key. The table is process-wide.
func RegisterDocComments(comments map[string]string) struct{} {
	docCommentsMu.Lock()
	defer docCommentsMu.Unlock()
	for k, v := range comments {
		docComments[k] = strings.TrimSpace(v)
	}
	return struct{}{}
}

// DocComment returns the doc comment registered for key (see
// RegisterDocComments), or "".
func DocComment(key string) string {
	docCommentsMu.RLock()
	defer docCommentsMu.RUnlock()
	return docComments[key]
}
