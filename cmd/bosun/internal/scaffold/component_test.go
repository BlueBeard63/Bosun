package scaffold_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/scaffold"
)

func addComponent(t *testing.T, c scaffold.Component) string {
	t.Helper()
	path, err := scaffold.AddComponent(c)
	if err != nil {
		t.Fatalf("add %s %s: %v", c.Kind, c.Name, err)
	}
	mustParse(t, path)
	return path
}

func TestAddComponent(t *testing.T) {
	root := t.TempDir()
	if _, err := scaffold.WriteMono(root, scaffold.Mono{
		Module: "example.com/shop", Name: "shop", Port: 8080, Event: "inmem", Bare: true,
	}); err != nil {
		t.Fatal(err)
	}
	ctrl := addComponent(t, scaffold.Component{Kind: scaffold.KindController, Name: "order-items", Root: root})
	if want := filepath.Join(root, "internal", "order-items", "order_items_controller.go"); ctrl != want {
		t.Fatalf("controller at %s, want %s", ctrl, want)
	}
	src := readFile(t, ctrl)
	for _, s := range []string{"package orderitems", "type OrderItemsController struct", `bosun.Controller[OrderItemsController]("/order-items")`,
		`bosun.Get(r, "/:id", c.Get)`, "req *bosun.Req[GetOrderItemsIn]"} {
		if !strings.Contains(src, s) {
			t.Fatalf("controller missing %q:\n%s", s, src)
		}
	}
	svc := addComponent(t, scaffold.Component{Kind: scaffold.KindService, Name: "order-items", Root: root})
	if src := readFile(t, svc); !strings.Contains(src, "package orderitems") || !strings.Contains(src, "bosun.Service[OrderItemsService]()") {
		t.Fatalf("service:\n%s", src)
	}
	mw := addComponent(t, scaffold.Component{Kind: scaffold.KindMiddleware, Name: "request_log", Root: root})
	if want := filepath.Join(root, "internal", "middleware", "request_log.go"); mw != want {
		t.Fatalf("middleware at %s, want %s", mw, want)
	}
	if src := readFile(t, mw); !strings.Contains(src, "bosun.Middleware[RequestLog]()") ||
		!strings.Contains(src, "Handle(next http.Handler) http.Handler") {
		t.Fatalf("middleware:\n%s", src)
	}
	custom := addComponent(t, scaffold.Component{Kind: scaffold.KindController, Name: "admin", Root: root,
		Dir: "internal/backoffice", Pkg: "office", Prefix: "/admin/v1"})
	if src := readFile(t, custom); !strings.Contains(src, "package office") || !strings.Contains(src, `("/admin/v1")`) {
		t.Fatalf("--dir/--pkg/--prefix ignored:\n%s", src)
	}

	regs, err := scaffold.RegenerateRegistries(root)
	if err != nil || len(regs) != 1 {
		t.Fatalf("regenerate = %v, %v", regs, err)
	}
	reg := readFile(t, regs[0])
	for _, imp := range []string{
		`_ "example.com/shop/internal/backoffice"`,
		`_ "example.com/shop/internal/middleware" // Middleware[RequestLog]`,
		`_ "example.com/shop/internal/order-items" // Controller[OrderItemsController], Service[OrderItemsService]`,
	} {
		if !containsFields(reg, imp) {
			t.Fatalf("registry missing %s:\n%s", imp, reg)
		}
	}

	// Never overwrite without Force, never redeclare a type, never mix packages.
	before := readFile(t, svc)
	if _, err := scaffold.AddComponent(scaffold.Component{Kind: scaffold.KindService, Name: "order-items", Root: root}); !errors.Is(err, scaffold.ErrFileExists) {
		t.Fatalf("err = %v, want ErrFileExists", err)
	}
	if readFile(t, svc) != before {
		t.Fatal("existing file was overwritten")
	}
	if _, err := scaffold.AddComponent(scaffold.Component{Kind: scaffold.KindService, Name: "order-items", Root: root, Force: true}); err != nil {
		t.Fatalf("with Force: %v", err)
	}
	if _, err := scaffold.AddComponent(scaffold.Component{Kind: scaffold.KindService, Name: "orderItems", Root: root,
		Dir: "internal/order-items"}); err == nil || !strings.Contains(err.Error(), "already declares OrderItemsService") {
		t.Fatalf("duplicate type: err = %v", err)
	}
	if _, err := scaffold.AddComponent(scaffold.Component{Kind: scaffold.KindService, Name: "x", Root: root,
		Dir: "internal/middleware", Pkg: "other"}); err == nil {
		t.Fatal("package mismatch: no error")
	}
	for _, c := range []scaffold.Component{
		{Kind: scaffold.KindService, Name: "two words", Root: root},
		{Kind: scaffold.KindService, Name: "x", Root: root, Dir: "../outside"},
		{Kind: scaffold.KindController, Name: "x", Root: root, Prefix: "no-slash"},
		{Kind: "widget", Name: "x", Root: root},
	} {
		if _, err := scaffold.AddComponent(c); err == nil {
			t.Errorf("%+v: no error", c)
		}
	}
}

// TestAddedComponentsBuild adds one of each component to a bare monolith
// and to a service in a bare workspace, regenerates the registry and builds
// and vets the result against this checkout of bosun.
func TestAddedComponentsBuild(t *testing.T) {
	root := bosunRoot(t)
	t.Run("mono", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "shop")
		if _, err := scaffold.WriteMono(dir, scaffold.Mono{
			Module: "example.com/shop", Name: "shop", Port: 8080, Event: "inmem", LocalBosun: root, Bare: true,
		}); err != nil {
			t.Fatal(err)
		}
		addAll(t, dir)
		if err := scaffold.Tidy(dir); err != nil {
			t.Fatalf("tidy: %v", err)
		}
		goRun(t, dir, "build", "./...")
		goRun(t, dir, "vet", "./...")
	})
	t.Run("workspace", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), "acme")
		if _, err := scaffold.WriteProject(dir, scaffold.Project{
			Module: "example.com/acme", Name: "acme", Service: "api", Port: 8080, Event: "inmem", LocalBosun: root, Bare: true,
		}); err != nil {
			t.Fatal(err)
		}
		svc, err := scaffold.FindModule(dir, "") // from the workspace root: the only service
		if err != nil {
			t.Fatalf("FindModule: %v", err)
		}
		addAll(t, svc)
		if err := scaffold.Tidy(svc); err != nil {
			t.Fatalf("tidy: %v", err)
		}
		goWork(t, svc, "build", "./...")
		goWork(t, svc, "vet", "./...")
		goRun(t, svc, "build", "./...")
	})
}

func addAll(t *testing.T, root string) {
	t.Helper()
	addComponent(t, scaffold.Component{Kind: scaffold.KindService, Name: "users", Root: root})
	ctrl := addComponent(t, scaffold.Component{Kind: scaffold.KindController, Name: "users", Root: root})
	addComponent(t, scaffold.Component{Kind: scaffold.KindMiddleware, Name: "audit", Root: root})
	// Use the documented injection: the controller gets the service.
	src := strings.Replace(readFile(t, ctrl), "type UsersController struct {", "type UsersController struct {\n\tUsers *UsersService", 1)
	if err := os.WriteFile(ctrl, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	regs, err := scaffold.RegenerateRegistries(root)
	if err != nil || len(regs) != 1 {
		t.Fatalf("regenerate = %v, %v", regs, err)
	}
	reg := readFile(t, regs[0])
	if !containsFields(reg, "/internal/users\" // Controller[UsersController], Service[UsersService]") ||
		!containsFields(reg, "/internal/middleware\" // Middleware[Audit]") {
		t.Fatalf("registry:\n%s", reg)
	}
}

// containsFields reports whether s contains sub, ignoring differences in
// runs of spaces (gofmt aligns the registry's import comments).
func containsFields(s, sub string) bool {
	return strings.Contains(strings.Join(strings.Fields(s), " "), strings.Join(strings.Fields(sub), " "))
}

func TestFindModule(t *testing.T) {
	dir := t.TempDir()
	if _, err := scaffold.WriteProject(dir, scaffold.Project{
		Module: "example.com/acme", Name: "acme", Service: "api", Port: 8080, Event: "none", Bare: true,
	}); err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.Abs(dir)
	api := filepath.Join(root, "services", "api")
	for _, from := range []string{root, filepath.Join(root, "contracts"), api} {
		if got, err := scaffold.FindModule(from, ""); err != nil || got != api {
			t.Errorf("from %s: %s, %v; want %s", from, got, err, api)
		}
	}

	// Add a second service to go.work: the root is now ambiguous.
	ws, err := scaffold.FindWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	orders := filepath.Join(root, "services", "orders")
	if _, err := scaffold.WriteService(orders, scaffold.Service{
		Module: "example.com/acme/services/orders", Name: "orders", Port: 8081, Event: "none", Workspace: ws, Bare: true,
	}); err != nil {
		t.Fatal(err)
	}
	gowork := strings.Replace(readFile(t, filepath.Join(root, "go.work")), "./services/api", "./services/api\n\t./services/orders", 1)
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte(gowork), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := scaffold.FindModule(root, ""); err == nil || !strings.Contains(err.Error(), "services/api, services/orders") {
		t.Fatalf("ambiguous root: err = %v", err)
	}
	if got, err := scaffold.FindModule(root, "orders"); err != nil || got != orders {
		t.Fatalf("--in orders: %s, %v", got, err)
	}
	if got, err := scaffold.FindModule(filepath.Join(orders, "internal"), ""); err != nil || got != orders {
		t.Fatalf("inside orders: %s, %v", got, err)
	}
	if _, err := scaffold.FindModule(root, "billing"); err == nil {
		t.Fatal("--in billing: no error")
	}
	if got, err := scaffold.FindModule(t.TempDir(), api); err != nil || got != api {
		t.Fatalf("--in <dir>: %s, %v", got, err)
	}
}
