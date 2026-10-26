package clientgen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/clientgen"
)

// contractsA and contractsB are two packages named contracts that both
// declare User, as a service and a shared library might.
const contractsA = `package contracts

type User struct{ ID string }
type Item struct{ Name string }
type Page[T any] struct{ Items []T }
type ListIn struct{ Limit int ` + "`query:\"limit\"`" + ` }
`

const contractsB = `package contracts

type User struct{ Email string }
`

const modA = "example.com/full/a/contracts"
const modB = "example.com/full/b/contracts"

// TestGeneratedClientsCompile builds the generated client for each case in a
// throwaway module, so the test fails on any output the compiler rejects.
func TestGeneratedClientsCompile(t *testing.T) {
	cases := map[string][]clientgen.Route{
		"empty": nil,
		"static": {
			{Method: "GET", Path: "/health", Operation: "app.(*Health).Get-fm", In: "struct {}", Out: "string"},
			{Method: "POST", Path: "/users", Operation: "app.(*Users).Create-fm", In: "contracts.User", InImport: modA, Out: "contracts.User", OutImport: modA},
		},
		"duplicates": {
			{Method: "GET", Path: "/users", Operation: "app.(*Users).List-fm", In: "struct {}", Out: "[]contracts.User", OutType: "[]" + modA + ".User"},
			{Method: "GET", Path: "/orders", Operation: "app.(*Orders).List-fm", In: "struct {}", Out: "struct {}"},
			{Method: "GET", Path: "/a", Operation: "app.(*Users).Routes.func1", In: "struct {}", Out: "struct {}"},
			{Method: "GET", Path: "/b", Operation: "app.(*Users).Routes.func2", In: "struct {}", Out: "struct {}"},
			{Method: "GET", Path: "/a/{id}", Operation: "app.(*Users).Routes.func3", In: "struct {}", Out: "struct {}"},
			{Method: "GET", Path: "/conf", Operation: "app.(*Conf).HTTP-fm", In: "struct {}", Out: "struct {}"},
			{Method: "GET", Path: "/conf.json", Operation: "app.main.func1", In: "struct {}", Out: "struct {}"},
		},
		"reserved": {
			{Method: "PUT", Path: "/{url}/{ctx}/{in}/{out}/{c}/{err}/{type}/{string}/{contracts}/{rest...}", Operation: "app.(*X).Put-fm",
				In: "contracts.User", InImport: modA, Out: "contracts.User", OutImport: modA},
		},
		"samename": {
			{Method: "POST", Path: "/convert/{id}", Operation: "app.(*X).Convert-fm", In: "contracts.User", InImport: modA, Out: "contracts.User", OutImport: modB},
			{Method: "GET", Path: "/both", Operation: "app.(*X).Both-fm", In: "struct {}",
				Out: "map[string]contracts.User", OutType: "map[string]" + modB + ".User"},
		},
		"generic": {
			{Method: "GET", Path: "/items", Operation: "app.(*X).Items-fm", In: "contracts.ListIn", InImport: modA,
				Out: "contracts.Page[" + modA + ".Item]", OutImport: modA},
			{Method: "GET", Path: "/users", Operation: "app.(*X).Users-fm", In: "struct {}",
				Out: "contracts.Page[" + modB + ".User]", OutType: modA + ".Page[" + modB + ".User]"},
		},
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/full\n\ngo 1.22\n")
	writeFile(t, filepath.Join(dir, "a", "contracts", "contracts.go"), contractsA)
	writeFile(t, filepath.Join(dir, "b", "contracts", "contracts.go"), contractsB)
	for name, routes := range cases {
		src, err := clientgen.Generate("client", name, routes)
		if err != nil {
			t.Fatalf("%s: generate: %v\n%s", name, err, src)
		}
		writeFile(t, filepath.Join(dir, "client", name, "client.go"), src)
	}
	goBuild(t, dir)
}

func writeFile(t *testing.T, path, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// goBuild runs go build ./... in the module at dir, which must need nothing
// outside the standard library.
func goBuild(t *testing.T, dir string) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs the go tool")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go tool not found")
	}
	cmd := exec.Command(goBin, "build", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated client does not build: %v\n%s", err, out)
	}
}
