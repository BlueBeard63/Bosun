package openapi

import (
	"io/fs"
	"runtime"
)

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
var srcFSes []srcFS

// srcFS is one registered tree and the import path of the package that
// registered it, which scanned methods are keyed under.
type srcFS struct {
	fsys fs.FS
	pkg  string
}

// Sources registers an embedded source tree for error scanning. Files at
// the root of fsys belong to the calling package; files in subdirectories
// belong to the packages below it.
func Sources(fsys fs.FS) struct{} {
	pkg := ""
	if pc, _, _, ok := runtime.Caller(1); ok {
		if f := runtime.FuncForPC(pc); f != nil {
			pkg, _ = splitFuncName(f.Name())
		}
	}
	srcFSes = append(srcFSes, srcFS{fsys: fsys, pkg: pkg})
	return struct{}{}
}
