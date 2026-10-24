package openapi

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/bluebeard63/bosun"
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

// statusNames maps net/http's Status* constant names to their codes. It is
// derived from http.StatusText, so every code net/http knows is covered.
var statusNames = buildStatusNames()

func buildStatusNames() map[string]int {
	names := map[string]int{}
	for code := 100; code <= 599; code++ {
		if text := http.StatusText(code); text != "" {
			names["Status"+identFromText(text)] = code
		}
	}
	// Constants whose names don't follow their status text.
	names["StatusNonAuthoritativeInfo"] = http.StatusNonAuthoritativeInfo
	names["StatusProxyAuthRequired"] = http.StatusProxyAuthRequired
	names["StatusTeapot"] = http.StatusTeapot
	return names
}

// identFromText joins the letter-and-digit runs of text, capitalising
// each: "Unsupported Media Type" becomes "UnsupportedMediaType" and
// "Multi-Status" becomes "MultiStatus", the way net/http names constants.
func identFromText(text string) string {
	var b strings.Builder
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		r := []rune(word)
		b.WriteString(string(unicode.ToUpper(r[0])) + string(r[1:]))
	}
	return b.String()
}

type errScanner struct {
	opts *ScanOptions

	once sync.Once
	// "example.com/app/users.(*Users).Get" -> sorted status codes, for
	// methods whose package import path is known.
	byPath map[string][]int
	// "users.(*Users).Get" -> sorted status codes, keyed by package name.
	// Used only for handlers in packages that weren't scanned by import
	// path; a key claimed by two different import paths is ambiguous and
	// never used.
	byName    map[string][]int
	nameOwner map[string]string
	ambiguous map[string]bool
	scanned   map[string]bool   // import paths with at least one scanned file
	modules   map[string]string // directory -> import path, "" if none
}

// statusesFor returns the scanned statuses for the handler with the given
// runtime function name, such as "example.com/app/users.(*Users).Get-fm".
// Matching is exact on package, receiver type and method.
func (s *errScanner) statusesFor(handlerName string) []int {
	s.once.Do(s.scan)
	pkg, recv, method, ok := parseHandler(handlerName)
	if !ok {
		return nil
	}
	key := methodKey(recv, method)
	if s.scanned[pkg] {
		return s.byPath[pkg+"."+key]
	}
	nameKey := path.Base(pkg) + "." + key
	if s.ambiguous[nameKey] {
		return nil
	}
	return s.byName[nameKey]
}

// parseHandler splits a runtime function name for a method value into its
// package import path, receiver type and method name. Generic receivers
// ("(*Repo[...]).Get") are reduced to the type name. ok is false for plain
// functions and closures, which the scanner doesn't record.
func parseHandler(name string) (pkg, recv, method string, ok bool) {
	pkg, rest := splitFuncName(name)
	rest = strings.TrimSuffix(strings.ReplaceAll(rest, "[...]", ""), "-fm")
	if strings.HasPrefix(rest, "(*") {
		i := strings.Index(rest, ").")
		if i < 0 {
			return "", "", "", false
		}
		recv, method = rest[2:i], rest[i+2:]
	} else if i := strings.Index(rest, "."); i >= 0 {
		recv, method = rest[:i], rest[i+1:] // value receiver: "Users.Get"
	}
	if pkg == "" || recv == "" || method == "" || strings.Contains(method, ".") {
		return "", "", "", false
	}
	return pkg, recv, method, true
}

// splitFuncName splits "example.com/app/users.(*Users).Get" at the end of
// the package path. The linker escapes dots in the last path element as
// %2e; they are restored so the path matches go.mod and import paths.
func splitFuncName(name string) (pkg, rest string) {
	slash := strings.LastIndex(name, "/")
	dot := strings.Index(name[slash+1:], ".")
	if dot < 0 {
		return "", name
	}
	pkg = name[:slash+1+dot]
	return strings.ReplaceAll(pkg, "%2e", "."), name[slash+1+dot+1:]
}

func methodKey(recv, method string) string { return "(*" + recv + ")." + method }

func (s *errScanner) scan() {
	s.byPath = map[string][]int{}
	s.byName = map[string][]int{}
	s.nameOwner = map[string]string{}
	s.ambiguous = map[string]bool{}
	s.scanned = map[string]bool{}
	s.modules = map[string]string{}
	fset := token.NewFileSet()

	// 1. embedded sources: work in deployed binaries
	for _, src := range srcFSes {
		_ = fs.WalkDir(src.fsys, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			if data, err := fs.ReadFile(src.fsys, p); err == nil {
				s.scanFile(fset, p, data, embeddedPkg(src.pkg, path.Dir(p)))
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
			s.scanFile(fset, p, data, s.importPath(filepath.Dir(p)))
		}
		return nil
	})
}

// embeddedPkg is the import path of a directory inside an embedded tree
// registered by package regPkg, or "" when it can't be known (a main
// package's subdirectories have no import path to derive).
func embeddedPkg(regPkg, dir string) string {
	switch {
	case regPkg == "" || dir == ".":
		return regPkg
	case regPkg == "main":
		return ""
	}
	return regPkg + "/" + dir
}

// importPath resolves an on-disk directory to its import path using the
// nearest go.mod, or "" when there is none.
func (s *errScanner) importPath(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	if p, ok := s.modules[abs]; ok {
		return p
	}
	p := ""
	if mod := modulePath(filepath.Join(abs, "go.mod")); mod != "" {
		p = mod
	} else if parent := filepath.Dir(abs); parent != abs {
		if pp := s.importPath(parent); pp != "" {
			p = pp + "/" + filepath.Base(abs)
		}
	}
	s.modules[abs] = p
	return p
}

// modulePath reads the module directive from a go.mod file.
func modulePath(gomod string) string {
	data, err := os.ReadFile(gomod)
	if err != nil {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && f[0] == "module" {
			return strings.Trim(f[1], `"`)
		}
	}
	return ""
}

// scanFile records the statuses of bosun.E calls in each method of one
// file. pkgPath is the file's import path, or "" when unknown; methods in
// package main are always keyed under "main", as the runtime names them.
func (s *errScanner) scanFile(fset *token.FileSet, name string, data []byte, pkgPath string) {
	file, err := parser.ParseFile(fset, name, data, 0)
	if err != nil {
		return
	}
	if file.Name.Name == "main" {
		pkgPath = "main"
	}
	if pkgPath != "" {
		s.scanned[pkgPath] = true
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv == nil || fd.Body == nil {
			continue
		}
		recv := receiverName(fd)
		if recv == "" {
			continue
		}
		codes := map[int]bool{}
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
		if len(codes) == 0 {
			continue
		}
		key := methodKey(recv, fd.Name.Name)
		if pkgPath != "" {
			mergeCodes(s.byPath, pkgPath+"."+key, codes)
		}
		nameKey := file.Name.Name + "." + key
		if owner, seen := s.nameOwner[nameKey]; seen && owner != pkgPath {
			s.ambiguous[nameKey] = true
		}
		s.nameOwner[nameKey] = pkgPath
		mergeCodes(s.byName, nameKey, codes)
	}
}

// mergeCodes adds codes to m[key], keeping the slice sorted. The same
// method can be seen twice when its file is both embedded and on disk.
func mergeCodes(m map[string][]int, key string, codes map[int]bool) {
	set := map[int]bool{}
	for _, c := range m[key] {
		set[c] = true
	}
	for c := range codes {
		set[c] = true
	}
	out := make([]int, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Ints(out)
	m[key] = out
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

// receiverName returns the receiver's type name, without pointer or type
// parameters: "Users" for (u *Users), "Repo" for (r *Repo[K, V]).
func receiverName(fd *ast.FuncDecl) string {
	t := fd.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch g := t.(type) {
	case *ast.IndexExpr:
		t = g.X
	case *ast.IndexListExpr:
		t = g.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
