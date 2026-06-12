package openapi

import "io/fs"

// srcFSes holds embedded source filesystems registered by application
// packages. Embedding lets the error scanner work at RUNTIME in deployed
// binaries with no source on disk:
//
//	//go:embed *.go
//	var src embed.FS
//	var _ = openapi.Sources(src)
//
// Note: this ships your handler source inside the binary (extractable with
// `strings`). For closed-source binaries prefer bosun.Errors(...) declarations.
var srcFSes []fs.FS

// Sources registers an embedded source tree for error scanning.
func Sources(fsys fs.FS) struct{} {
	srcFSes = append(srcFSes, fsys)
	return struct{}{}
}
