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
//
// The file also carries the module's doc comments on typed handlers and on
// struct types and fields, registered with bosun.RegisterDocComments, so the
// OpenAPI spec can describe operations and schemas. That call needs bosun
// DocsMinVersion or later; for modules that require an older bosun the
// comments are left out.
package registrygen

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
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

// DocsMinVersion is the first bosun release with bosun.RegisterDocComments.
const DocsMinVersion = "v0.8.0"

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

// PackageDocs holds the doc comments found in one package, keyed as for
// bosun.RegisterDocComments but without the package path: "(*T).Method",
// "T.Method" or "Func" for typed handlers, "T" for struct types and
// "T.Field" for their exported fields.
type PackageDocs struct {
	ImportPath string
	Name       string // package name; "main" packages are keyed as "main"
	Comments   map[string]string
}

// Module is the result of scanning a module.
type Module struct {
	Root     string // absolute module root (directory holding go.mod)
	Path     string // module path from go.mod
	Packages []Package
	Skipped  []string // registering package main dirs, which can't be imported

	// Docs lists the packages with doc comments to register, by import
	// path. DocsSupported reports whether the module's bosun has
	// RegisterDocComments: it requires DocsMinVersion or later, replaces
	// bosun (a local checkout), or is bosun itself.
	Docs          []PackageDocs
	DocsSupported bool

	ctx *build.Context // build constraints files are matched against
}

// buildContext returns the context Scan matches files against: the host's
// GOOS, GOARCH and cgo setting, as for `go build`, plus the given tags.
func buildContext(tags []string) *build.Context {
	ctx := build.Default
	ctx.BuildTags = append(append([]string(nil), ctx.BuildTags...), tags...)
	return &ctx
}

// included reports whether file name in dir is part of the package: a
// non-test, non-generated .go file whose build constraints (//go:build lines
// and _GOOS/_GOARCH name suffixes) are satisfied.
func included(ctx *build.Context, dir, name string) (bool, error) {
	if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == FileName {
		return false, nil
	}
	if ctx == nil {
		ctx = &build.Default
	}
	return ctx.MatchFile(dir, name)
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
// starting with "." or "_" are skipped; _test.go files and files excluded by
// build constraints (for the host platform plus tags) are ignored.
func Scan(root string, tags ...string) (*Module, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	modPath, err := modulePath(filepath.Join(root, "go.mod"), gomod)
	if err != nil {
		return nil, err
	}
	m := &Module{Root: root, Path: modPath, ctx: buildContext(tags),
		DocsSupported: modPath == BosunImportPath || docsSupported(gomod)}
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
		pkgName, decls, docs, err := scanDir(m.ctx, p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		importPath := modPath
		if rel != "." {
			importPath = path.Join(modPath, filepath.ToSlash(rel))
		}
		if len(docs) > 0 {
			m.Docs = append(m.Docs, PackageDocs{ImportPath: importPath, Name: pkgName, Comments: docs})
		}
		if len(decls) == 0 {
			return nil
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
	sort.Slice(m.Docs, func(i, j int) bool { return m.Docs[i].ImportPath < m.Docs[j].ImportPath })
	sort.Strings(m.Skipped)
	return m, nil
}

// modulePath returns the module directive of go.mod file gomod, read as data.
func modulePath(gomod string, data []byte) (string, error) {
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

// scanDir parses the Go files in dir that are part of the package (see
// included) and returns the package name, the registration declarations
// found at package level and the package's doc comments (see PackageDocs).
func scanDir(ctx *build.Context, dir string) (string, []string, map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, nil, err
	}
	fset := token.NewFileSet()
	var pkgName string
	var decls []string
	docs := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		ok, err := included(ctx, dir, name)
		if err != nil {
			return "", nil, nil, fmt.Errorf("%s: %w", filepath.Join(dir, name), err)
		}
		if !ok {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution|parser.ParseComments)
		if err != nil {
			return "", nil, nil, fmt.Errorf("parse %s: %w", filepath.Join(dir, name), err)
		}
		if pkgName == "" {
			pkgName = f.Name.Name
		}
		local := bosunLocalName(f)
		docComments(f, local, docs)
		if local == "" {
			continue
		}
		decls = append(decls, registrations(f, local)...)
	}
	return pkgName, decls, docs, nil
}

// docComments adds f's doc comments to docs: those of typed handlers
// (functions and methods taking a *<local>.Req[...] second parameter) and
// of struct types and their exported fields. A field's doc comment wins
// over its line comment. local is the file's name for the bosun package,
// or "" when it doesn't import it (then there are no typed handlers).
func docComments(f *ast.File, local string, docs map[string]string) {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if text := commentText(d.Doc); text != "" && local != "" && isTypedHandler(d.Type, local) {
				docs[funcKey(d)] = text
			}
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Assign.IsValid() {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				doc := ts.Doc
				if doc == nil && len(d.Specs) == 1 {
					doc = d.Doc // type T struct{...}: the comment is on the decl
				}
				if text := commentText(doc); text != "" {
					docs[ts.Name.Name] = text
				}
				for _, field := range st.Fields.List {
					text := commentText(field.Doc)
					if text == "" {
						text = commentText(field.Comment)
					}
					if text == "" {
						continue
					}
					for _, n := range field.Names {
						if n.IsExported() {
							docs[ts.Name.Name+"."+n.Name] = text
						}
					}
				}
			}
		}
	}
}

// commentText returns a comment group's text without directives such as
// //go:generate, trimmed.
func commentText(g *ast.CommentGroup) string {
	if g == nil {
		return ""
	}
	return strings.TrimSpace(g.Text())
}

// isTypedHandler reports whether ft has the typed handler shape
// func(ctx, *<local>.Req[In]) (Out, error).
func isTypedHandler(ft *ast.FuncType, local string) bool {
	var params []ast.Expr
	for _, p := range ft.Params.List {
		n := len(p.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			params = append(params, p.Type)
		}
	}
	if len(params) != 2 || ft.Results == nil || ft.Results.NumFields() != 2 {
		return false
	}
	star, ok := params[1].(*ast.StarExpr)
	if !ok {
		return false
	}
	var gen ast.Expr
	switch x := star.X.(type) {
	case *ast.IndexExpr:
		gen = x.X
	case *ast.IndexListExpr:
		gen = x.X
	default:
		return false
	}
	se, ok := gen.(*ast.SelectorExpr)
	if !ok || se.Sel.Name != "Req" {
		return false
	}
	id, ok := se.X.(*ast.Ident)
	return ok && id.Name == local
}

// funcKey names a function the way the runtime does, without the package
// path or type arguments: "Func", "T.Method" or "(*T).Method".
func funcKey(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	t := d.Recv.List[0].Type
	ptr := false
	if s, ok := t.(*ast.StarExpr); ok {
		ptr, t = true, s.X
	}
	switch x := t.(type) {
	case *ast.IndexExpr:
		t = x.X
	case *ast.IndexListExpr:
		t = x.X
	}
	recv := types.ExprString(t)
	if ptr {
		return "(*" + recv + ")." + d.Name.Name
	}
	return recv + "." + d.Name.Name
}

// docsSupported reports whether a go.mod's bosun has RegisterDocComments:
// it requires DocsMinVersion or later, or replaces bosun.
func docsSupported(gomod []byte) bool {
	version, replaced := "", false
	block := ""
	for _, line := range strings.Split(string(gomod), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if block != "" && f[0] == ")" {
			block = ""
			continue
		}
		verb := block
		if block == "" {
			verb, f = f[0], f[1:]
			if len(f) == 1 && f[0] == "(" {
				block = verb
				continue
			}
		}
		if len(f) == 0 || strings.Trim(f[0], `"`) != BosunImportPath {
			continue
		}
		switch verb {
		case "require":
			if len(f) > 1 {
				version = f[1]
			}
		case "replace":
			replaced = true
		}
	}
	return replaced || versionAtLeast(version, DocsMinVersion)
}

// versionAtLeast compares semantic versions "vX.Y.Z[-pre][+build]". A
// pre-release (including a pseudo-version) of floor itself is older than floor.
func versionAtLeast(v, floor string) bool {
	parse := func(s string) (nums [3]int, pre bool, ok bool) {
		s, _, _ = strings.Cut(s, "+")
		s, _, hasPre := strings.Cut(s, "-")
		parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
		if !strings.HasPrefix(s, "v") || len(parts) != 3 {
			return nums, false, false
		}
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nums, false, false
			}
			nums[i] = n
		}
		return nums, hasPre, true
	}
	got, pre, ok := parse(v)
	want, _, _ := parse(floor)
	if !ok {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	return !pre
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
	NoDocs     bool   // leave doc comments out even when the module's bosun supports them
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
	docs := docEntries(m, opts)
	if len(docs) > 0 {
		lines = append([]string{fmt.Sprintf("\t%q\n", BosunImportPath)}, lines...)
	}
	if len(lines) > 0 {
		b.WriteString("\nimport (\n" + strings.Join(lines, "\n") + "\n)\n")
	}
	if len(docs) > 0 {
		b.WriteString("\n// Doc comments of typed handlers and of struct types and fields, for the\n")
		b.WriteString("// summaries and descriptions in the OpenAPI spec.\n")
		b.WriteString("var _ = bosun.RegisterDocComments(map[string]string{\n")
		for _, d := range docs {
			fmt.Fprintf(&b, "\t%s: %s,\n", strconv.Quote(d[0]), strconv.Quote(d[1]))
		}
		b.WriteString("})\n")
	}
	return format.Source(b.Bytes())
}

// docEntries returns the key/comment pairs to register, sorted by key, or
// nil when docs are off or the module's bosun lacks RegisterDocComments.
// Package main is only registered for the target itself (its handlers are
// named "main.<...>" at runtime); other main packages are other binaries.
func docEntries(m *Module, opts Options) [][2]string {
	if opts.NoDocs || !m.DocsSupported || opts.ImportPath == BosunImportPath {
		return nil
	}
	var out [][2]string
	for _, pd := range m.Docs {
		prefix := pd.ImportPath
		if pd.Name == "main" {
			if pd.ImportPath != opts.ImportPath {
				continue
			}
			prefix = "main"
		}
		for k, v := range pd.Comments {
			out = append(out, [2]string{prefix + "." + k, v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
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
	name, err := packageName(m.ctx, abs)
	if err != nil {
		return Options{}, err
	}
	if name == "" {
		name = filepath.Base(abs)
	}
	return Options{PkgName: name, ImportPath: importPath}, nil
}

func packageName(ctx *build.Context, dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() {
			continue
		}
		ok, err := included(ctx, dir, n)
		if err != nil {
			return "", fmt.Errorf("%s: %w", filepath.Join(dir, n), err)
		}
		if !ok {
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
