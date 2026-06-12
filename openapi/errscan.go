package openapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/amberstack/bosun"
)

// ScanOptions configures source scanning for error responses. The scanner
// walks SourceDir for handler method bodies and records every
// bosun.E(status, ...) call, so the spec documents real failure modes.
// When source isn't available (deployed binary), scanning silently yields
// nothing and the spec still includes 400/500 defaults.
type ScanOptions struct {
	SourceDir string
}

var _ = bosun.Default[*ScanOptions](func() *ScanOptions {
	return &ScanOptions{SourceDir: "."}
})

var statusNames = map[string]int{
	"StatusBadRequest":          400,
	"StatusUnauthorized":        401,
	"StatusPaymentRequired":     402,
	"StatusForbidden":           403,
	"StatusNotFound":            404,
	"StatusMethodNotAllowed":    405,
	"StatusNotAcceptable":       406,
	"StatusRequestTimeout":      408,
	"StatusConflict":            409,
	"StatusGone":                410,
	"StatusUnprocessableEntity": 422,
	"StatusTooManyRequests":     429,
	"StatusInternalServerError": 500,
	"StatusNotImplemented":      501,
	"StatusBadGateway":          502,
	"StatusServiceUnavailable":  503,
}

type errScanner struct {
	opts *ScanOptions

	once sync.Once
	// "(*AuthController).Login" -> sorted status codes
	byHandler map[string][]int
}

func (s *errScanner) statusesFor(handlerName string) []int {
	s.once.Do(s.scan)
	for key, codes := range s.byHandler {
		if strings.Contains(handlerName, key) {
			return codes
		}
	}
	return nil
}

func (s *errScanner) scan() {
	s.byHandler = map[string][]int{}
	fset := token.NewFileSet()

	// 1. embedded sources: work in deployed binaries
	for _, fsys := range srcFSes {
		_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			if data, err := fs.ReadFile(fsys, p); err == nil {
				s.scanFile(fset, p, data)
			}
			return nil
		})
	}

	// 2. on-disk sources: dev mode
	root := s.opts.SourceDir
	if root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p == root {
				return nil // never skip the root ("." starts with '.')
			}
			name := d.Name()
			if name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if data, err := os.ReadFile(p); err == nil {
			s.scanFile(fset, p, data)
		}
		return nil
	})
}

func (s *errScanner) scanFile(fset *token.FileSet, name string, data []byte) {
	file, err := parser.ParseFile(fset, name, data, 0)
	if err != nil {
		return
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil {
			continue
		}
		key := "(*" + receiverName(fd) + ")." + fd.Name.Name
		codes := map[int]bool{}
		for _, existing := range s.byHandler[key] {
			codes[existing] = true
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "E" || len(call.Args) < 1 {
				return true
			}
			if code, ok := statusArg(call.Args[0]); ok {
				codes[code] = true
			}
			return true
		})
		if len(codes) > 0 {
			out := make([]int, 0, len(codes))
			for c := range codes {
				out = append(out, c)
			}
			sort.Ints(out)
			s.byHandler[key] = out
		}
	}
}

func statusArg(e ast.Expr) (int, bool) {
	switch a := e.(type) {
	case *ast.BasicLit:
		if a.Kind == token.INT {
			n, err := strconv.Atoi(a.Value)
			return n, err == nil
		}
	case *ast.SelectorExpr:
		if code, ok := statusNames[a.Sel.Name]; ok {
			return code, true
		}
	}
	return 0, false
}

func receiverName(fd *ast.FuncDecl) string {
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
