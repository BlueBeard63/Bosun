package scaffold

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/registrygen"
)

// Component kinds that AddComponent generates.
const (
	KindController = "controller"
	KindService    = "service"
	KindMiddleware = "middleware"
)

// Component describes one controller, service or middleware to add to an
// existing module (`bosun add`).
type Component struct {
	Kind string // KindController, KindService or KindMiddleware
	Name string // e.g. "users" or "request-log"
	Root string // module root, the directory holding go.mod

	// Dir is the package directory relative to Root. It defaults to
	// internal/<name> for controllers and services (a feature package) and
	// internal/middleware for middleware.
	Dir string

	// Pkg is the package name. It defaults to the package of the Go files
	// already in Dir, or else to Dir's base name.
	Pkg string

	// Prefix is a controller's route prefix; it defaults to "/<name>".
	Prefix string

	// Force overwrites the component's file if it already exists.
	Force bool
}

type componentData struct {
	Component
	Type   string // Go type name, e.g. UsersController
	Camel  string // Name in CamelCase, e.g. Users
	Prefix string
}

// AddComponent writes c's source file and returns its path. It refuses to
// overwrite an existing file unless c.Force is set, and to declare a type
// the package already has. Regenerate the module's registry afterwards
// (RegenerateRegistries) so the package is imported.
func AddComponent(c Component) (string, error) {
	if err := validName(c.Kind, c.Name); err != nil {
		return "", err
	}
	camel := camelCase(c.Name)
	d := componentData{Component: c, Camel: camel, Prefix: c.Prefix}
	var tmpl, file string
	switch c.Kind {
	case KindController:
		tmpl, file, d.Type = tmplComponentController, snakeCase(c.Name)+"_controller.go", camel+"Controller"
		if d.Prefix == "" {
			d.Prefix = "/" + c.Name
		}
		if !strings.HasPrefix(d.Prefix, "/") || strings.ContainsAny(d.Prefix, "\"`\\ \t\n") {
			return "", fmt.Errorf("invalid route prefix %q: it must start with / and contain no quotes or spaces", d.Prefix)
		}
	case KindService:
		tmpl, file, d.Type = tmplComponentService, snakeCase(c.Name)+"_service.go", camel+"Service"
	case KindMiddleware:
		tmpl, file, d.Type = tmplComponentMiddleware, snakeCase(c.Name)+".go", camel
	default:
		return "", fmt.Errorf("unknown component kind %q", c.Kind)
	}
	if c.Dir == "" {
		c.Dir = filepath.Join("internal", strings.ToLower(c.Name))
		if c.Kind == KindMiddleware {
			c.Dir = filepath.Join("internal", "middleware")
		}
	}
	if filepath.IsAbs(c.Dir) {
		return "", fmt.Errorf("--dir %s must be relative to the module root %s", c.Dir, c.Root)
	}
	dir := filepath.Join(c.Root, c.Dir)
	if rel, err := filepath.Rel(c.Root, dir); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("--dir %s is outside the module root %s", c.Dir, c.Root)
	}

	existing, names, err := scanPackage(dir)
	if err != nil {
		return "", err
	}
	switch {
	case c.Pkg != "" && existing != "" && c.Pkg != existing:
		return "", fmt.Errorf("%s already holds package %s, not %s", dir, existing, c.Pkg)
	case c.Pkg == "" && existing != "":
		c.Pkg = existing
	case c.Pkg == "":
		c.Pkg = packageFromDir(dir)
	}
	if !token.IsIdentifier(c.Pkg) || token.IsKeyword(c.Pkg) {
		return "", fmt.Errorf("invalid package name %q; pass --pkg", c.Pkg)
	}
	d.Component = c

	full := filepath.Join(dir, file)
	if _, err := os.Stat(full); err == nil && !c.Force {
		return "", fmt.Errorf("%w: %s", ErrFileExists, full)
	}
	for _, t := range declaredTypes(c.Kind, d.Type, camel) {
		if f, ok := names[t]; ok && f != file {
			return "", fmt.Errorf("package %s in %s already declares %s (in %s)", c.Pkg, dir, t, f)
		}
	}
	written, err := render(dir, map[string]string{file: tmpl}, d)
	if err != nil {
		return "", err
	}
	return written[0], nil
}

// ErrFileExists reports a component file that already exists.
var ErrFileExists = errors.New("file already exists")

// declaredTypes lists the package-level names a component's file declares.
func declaredTypes(kind, typ, camel string) []string {
	if kind == KindController {
		return []string{typ, "Get" + camel + "In", camel + "Out"}
	}
	return []string{typ}
}

// scanPackage returns the package name of the Go files in dir ("" if there
// are none or dir doesn't exist) and the file declaring each package-level
// name.
func scanPackage(dir string) (string, map[string]string, error) {
	names := map[string]string{}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return "", names, nil
	}
	if err != nil {
		return "", nil, err
	}
	pkg := ""
	fset := token.NewFileSet()
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.SkipObjectResolution)
		if err != nil {
			return "", nil, err
		}
		if pkg == "" {
			pkg = f.Name.Name
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}
			for _, spec := range gd.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					names[ts.Name.Name] = n
				}
			}
		}
	}
	return pkg, names, nil
}

// packageFromDir derives a package name from a directory's base name:
// lower case, letters and digits only.
func packageFromDir(dir string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(dir)) {
		if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// camelCase turns "request-log" or "request_log" into "RequestLog".
func camelCase(name string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' }) {
		b.WriteString(strings.ToUpper(part[:1]) + part[1:])
	}
	return b.String()
}

// snakeCase turns "request-log" into "request_log", for file names.
func snakeCase(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "-", "_"))
}

// RegenerateRegistries rewrites every generated registry file
// (zz_bosun_registry.go) in the module at root, as `bosun gen registry`
// run in its directory would, and returns their paths. It returns none when
// the module has no registry file yet.
func RegenerateRegistries(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if p != root && (name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
				return filepath.SkipDir
			}
			if p != root {
				if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
					return filepath.SkipDir // nested module
				}
			}
			return nil
		}
		if d.Name() == registrygen.FileName {
			dirs = append(dirs, filepath.Dir(p))
		}
		return nil
	})
	if err != nil || len(dirs) == 0 {
		return nil, err
	}
	mod, err := registrygen.Scan(root)
	if err != nil {
		return nil, err
	}
	var written []string
	for _, dir := range dirs {
		target, err := registrygen.TargetFor(mod, dir)
		if err != nil {
			return written, err
		}
		src, err := registrygen.Render(mod, target)
		if err != nil {
			return written, err
		}
		full := filepath.Join(dir, registrygen.FileName)
		if err := os.WriteFile(full, src, 0o644); err != nil {
			return written, err
		}
		written = append(written, full)
	}
	return written, nil
}

// FindModule resolves the module `bosun add` writes into, starting from dir.
//
// With in set, it is a directory holding go.mod, or the name (or
// workspace-relative path) of a module in the enclosing go.work, such as a
// service under services/. Otherwise it is the nearest go.mod above dir;
// inside a workspace, when that is not a module within it (say dir is the
// workspace root) or is the contracts module, which must not register
// anything, it is the workspace's only service module.
func FindModule(dir, in string) (string, error) {
	ws, err := FindWorkspace(dir)
	if err != nil {
		return "", err
	}
	if in != "" {
		if _, err := os.Stat(filepath.Join(in, "go.mod")); err == nil {
			return filepath.Abs(in)
		}
		if ws == nil {
			return "", fmt.Errorf("--in %s: no go.mod there, and not inside a Go workspace", in)
		}
		services := ws.services()
		for _, m := range services {
			rel, _ := filepath.Rel(ws.Root, m)
			if filepath.Base(m) == in || filepath.ToSlash(rel) == filepath.ToSlash(in) {
				return m, nil
			}
		}
		return "", fmt.Errorf("--in %s: no such module in the workspace at %s (have: %s)", in, ws.Root, ws.serviceList(services))
	}
	root, rootErr := registrygen.FindModuleRoot(dir)
	if ws == nil {
		return root, rootErr
	}
	if rootErr == nil && root != ws.ContractsDir {
		if rel, err := filepath.Rel(ws.Root, root); err == nil && !strings.HasPrefix(rel, "..") {
			return root, nil // a module inside the workspace (rel "." for a root module)
		}
	}
	services := ws.services()
	if len(services) == 1 {
		return services[0], nil
	}
	return "", fmt.Errorf("inside the workspace at %s: run this in a service's directory or pass --in <service> (one of: %s)",
		ws.Root, ws.serviceList(services))
}

// services returns the workspace's modules other than contracts.
func (w *Workspace) services() []string {
	var out []string
	for _, m := range w.Modules {
		if m != w.ContractsDir {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

func (w *Workspace) serviceList(services []string) string {
	if len(services) == 0 {
		return "none"
	}
	names := make([]string, len(services))
	for i, m := range services {
		rel, _ := filepath.Rel(w.Root, m)
		names[i] = filepath.ToSlash(rel)
	}
	return strings.Join(names, ", ")
}

const tmplComponentController = `package {{.Pkg}}

import (
	"context"

	"github.com/bluebeard63/bosun"
)

// {{.Type}} serves the {{.Prefix}} routes.
type {{.Type}} struct {
	// Fields whose types are registered with Bosun (bosun.Service, a
	// module's services) are injected; add them here, e.g.
	//
	//	{{.Camel}} *{{.Camel}}Service
}

var _ = bosun.Controller[{{.Type}}]("{{.Prefix}}")

// Routes mounts the controller's routes under {{.Prefix}}.
func (c *{{.Type}}) Routes(r *bosun.Router) {
	bosun.Get(r, "/:id", c.Get)
}

// Get{{.Camel}}In is the request for GET {{.Prefix}}/:id.
type Get{{.Camel}}In struct {
	ID string ` + "`path:\"id\"`" + `
}

// {{.Camel}}Out is the response body for GET {{.Prefix}}/:id.
type {{.Camel}}Out struct {
	ID string ` + "`json:\"id\"`" + `
}

// Get returns one item by ID.
func (c *{{.Type}}) Get(ctx context.Context, req *bosun.Req[Get{{.Camel}}In]) ({{.Camel}}Out, error) {
	return {{.Camel}}Out{ID: req.Body.ID}, nil
}
`

const tmplComponentService = `package {{.Pkg}}

import "github.com/bluebeard63/bosun"

// {{.Type}} is an injectable singleton: any controller or service with a
// field of type *{{.Type}} gets the one instance.
type {{.Type}} struct {
	// Fields whose types are registered with Bosun (other services, a
	// module's services) are injected; add them here, e.g.
	//
	//	Other *OtherService
}

var _ = bosun.Service[{{.Type}}]()
`

const tmplComponentMiddleware = `package {{.Pkg}}

import (
	"net/http"

	"github.com/bluebeard63/bosun"
)

// {{.Type}} is HTTP middleware.
type {{.Type}} struct {
	// Fields whose types are registered with Bosun are injected; add them
	// here, e.g.
	//
	//	Users *users.UsersService
}

// Attach it to every route with
//
//	bosun.New(bosun.WithMiddleware(bosun.Use[{{.Pkg}}.{{.Type}}]()))
//
// to a controller with bosun.Controller[T]("/prefix", bosun.Use[{{.Pkg}}.{{.Type}}]()),
// or to one route by passing bosun.Use[{{.Pkg}}.{{.Type}}]() after its handler.
var _ = bosun.Middleware[{{.Type}}]()

// Handle wraps next.
func (m *{{.Type}}) Handle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}
`
