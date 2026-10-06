// Package registrygen implements `bosun gen registry`: it finds every package
// in a Go module that self-registers with Bosun (package-level
// `var _ = bosun.Controller[...]`, Service, Middleware, Default, DefaultBind or
// DefaultDynamic declarations) and renders a Go file that blank-imports them,
// so main.go no longer needs a hand-maintained list of side-effect imports.
//
// Go only initializes packages that are imported, so registrations in an
// unimported package never run. Discovery therefore has to happen at build
// time; this generator does it by parsing source with the standard library
// (go/parser), with no reflection or runtime filesystem scanning.
package registrygen

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// BosunImportPath is the package whose registration calls are detected.
const BosunImportPath = "github.com/bluebeard63/bosun"

// FileName is the default name of the generated file.
const FileName = "zz_bosun_registry.go"

// registrationFuncs are the bosun functions that register something at init.
var registrationFuncs = map[string]bool{
	"Controller":     true,
	"Service":        true,
	"Middleware":     true,
	"Default":        true,
	"DefaultBind":    true,
	"DefaultDynamic": true,
}

// Package is one discovered registering package.
type Package struct {
	ImportPath string
	Dir        string
	Decls      []string // e.g. "Controller[UsersController]", in source order
}

// Module is the result of scanning a module.
type Module struct {
	Root     string // absolute module root (directory holding go.mod)
	Path     string // module path from go.mod
	Packages []Package
	Skipped  []string // registering package main dirs, which can't be imported
}

// FindModuleRoot walks up from dir to the nearest directory holding go.mod.
func FindModuleRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("no go.mod found in %s or any parent", abs)
		}
	}
}

// Scan parses every package under root (a module root) and returns those
// that register with Bosun. Nested modules, vendor, testdata and directories
// starting with "." or "_" are skipped; _test.go files are ignored.
func Scan(root string) (*Module, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	modPath, err := readModulePath(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	m := &Module{Root: root, Path: modPath}
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p != root {
			name := d.Name()
			if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir // nested module: not part of this one
			}
		}
		pkgName, decls, err := scanDir(p)
		if err != nil {
			return err
		}
		if len(decls) == 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		importPath := modPath
		if rel != "." {
			importPath = path.Join(modPath, filepath.ToSlash(rel))
		}
		if pkgName == "main" {
			m.Skipped = append(m.Skipped, importPath)
			return nil
		}
		m.Packages = append(m.Packages, Package{ImportPath: importPath, Dir: p, Decls: decls})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(m.Packages, func(i, j int) bool { return m.Packages[i].ImportPath < m.Packages[j].ImportPath })
	sort.Strings(m.Skipped)
	return m, nil
}

func readModulePath(gomod string) (string, error) {
	data, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module") {
			mod := strings.TrimSpace(strings.TrimPrefix(line, "module"))
			if unq, err := strconv.Unquote(mod); err == nil {
				mod = unq
			}
			if mod != "" {
				return mod, nil
			}
		}
	}
	return "", fmt.Errorf("%s: no module directive", gomod)
}

// scanDir parses the non-test Go files in dir and returns the package name
// and the registration declarations found at package level.
func scanDir(dir string) (string, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	fset := token.NewFileSet()
	var pkgName string
	var decls []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == FileName {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return "", nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, name), err)
		}
		if pkgName == "" {
			pkgName = f.Name.Name
		}
		local := bosunLocalName(f)
		if local == "" {
			continue
		}
		decls = append(decls, registrations(f, local)...)
	}
	return pkgName, decls, nil
}

// bosunLocalName returns the identifier the file uses for the bosun package,
// or "" if the file doesn't import it (or dot/blank imports it).
func bosunLocalName(f *ast.File) string {
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		if p != BosunImportPath {
			continue
		}
		if imp.Name == nil {
			return "bosun"
		}
		if imp.Name.Name == "." || imp.Name.Name == "_" {
			return ""
		}
		return imp.Name.Name
	}
	return ""
}

// registrations finds package-level `var ... = <local>.Func[...](...)`.
func registrations(f *ast.File, local string) []string {
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, v := range vs.Values {
				call, ok := v.(*ast.CallExpr)
				if !ok {
					continue
				}
				if name, ok := registrationName(call.Fun, local); ok {
					out = append(out, name)
				}
			}
		}
	}
	return out
}

func registrationName(fun ast.Expr, local string) (string, bool) {
	var sel ast.Expr = fun
	var typeArgs []ast.Expr
	switch x := fun.(type) {
	case *ast.IndexExpr:
		sel, typeArgs = x.X, []ast.Expr{x.Index}
	case *ast.IndexListExpr:
		sel, typeArgs = x.X, x.Indices
	}
	se, ok := sel.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	id, ok := se.X.(*ast.Ident)
	if !ok || id.Name != local || !registrationFuncs[se.Sel.Name] {
		return "", false
	}
	if len(typeArgs) == 0 {
		return se.Sel.Name, true
	}
	args := make([]string, len(typeArgs))
	for i, a := range typeArgs {
		args[i] = types.ExprString(a)
	}
	return se.Sel.Name + "[" + strings.Join(args, ", ") + "]", true
}

// --- rendering ---

// Options controls Render.
type Options struct {
	PkgName    string // package clause of the generated file
	ImportPath string // import path of the package the file is written into
}

// ErrInternalImport reports packages the target may not import under Go's
// internal-package rule.
var ErrInternalImport = errors.New("registry: package not importable from target")

// Render produces the generated Go source for m, written into the package
// described by opts. The target package itself is never imported.
func Render(m *Module, opts Options) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("// Code generated by bosun gen registry. DO NOT EDIT.\n\n")
	b.WriteString("// This file blank-imports every package in " + m.Path + " that registers\n")
	b.WriteString("// with Bosun, so their declarations run before bosun.New(). Regenerate\n")
	b.WriteString("// with `go generate` after adding or removing such a package.\n\n")
	fmt.Fprintf(&b, "package %s\n", opts.PkgName)

	var lines []string
	var bad []string
	for _, p := range m.Packages {
		if p.ImportPath == opts.ImportPath {
			continue
		}
		if !internalAllowed(opts.ImportPath, p.ImportPath) {
			bad = append(bad, p.ImportPath)
			continue
		}
		lines = append(lines, fmt.Sprintf("\t_ %q // %s", p.ImportPath, strings.Join(p.Decls, ", ")))
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%w %s: %s (move the generated file higher in the tree, e.g. to the module root or main package)",
			ErrInternalImport, opts.ImportPath, strings.Join(bad, ", "))
	}
	if len(lines) > 0 {
		b.WriteString("\nimport (\n" + strings.Join(lines, "\n") + "\n)\n")
	}
	return format.Source(b.Bytes())
}

// internalAllowed applies Go's internal-package rule: a/b/internal/c may
// only be imported by packages rooted at a/b.
func internalAllowed(importer, imported string) bool {
	parts := strings.Split(imported, "/")
	for i, part := range parts {
		if part != "internal" {
			continue
		}
		parent := strings.Join(parts[:i], "/")
		if importer != parent && !strings.HasPrefix(importer, parent+"/") {
			return false
		}
	}
	return true
}

// TargetFor resolves the import path and package name for directory dir
// inside module m. The package name is read from existing Go files in dir;
// when there are none, the directory's base name is used.
func TargetFor(m *Module, dir string) (Options, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Options{}, err
	}
	rel, err := filepath.Rel(m.Root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return Options{}, fmt.Errorf("%s is outside module root %s", abs, m.Root)
	}
	importPath := m.Path
	if rel != "." {
		importPath = path.Join(m.Path, filepath.ToSlash(rel))
	}
	name, err := packageName(abs)
	if err != nil {
		return Options{}, err
	}
	if name == "" {
		name = filepath.Base(abs)
	}
	return Options{PkgName: name, ImportPath: importPath}, nil
}

func packageName(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") || n == FileName {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.PackageClauseOnly)
		if err != nil {
			return "", err
		}
		return f.Name.Name, nil
	}
	return "", nil
}
