package openapi

import (
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func scanSources(t *testing.T, files map[string]string) *errScanner {
	t.Helper()
	s := &errScanner{opts: &ScanOptions{}}
	s.once.Do(func() {}) // tests feed files directly
	s.byPath, s.byName = map[string][]int{}, map[string][]int{}
	s.nameOwner, s.ambiguous = map[string]string{}, map[string]bool{}
	s.scanned = map[string]bool{}
	fset := token.NewFileSet()
	for pkg, src := range files {
		s.scanFile(fset, pkg+"/x.go", []byte(src), pkg)
	}
	return s
}

func TestScanExactHandlerMatch(t *testing.T) {
	s := scanSources(t, map[string]string{
		"example.com/a/users": `package users
func (c *Users) Get() error    { return bosun.E(404, "missing", nil) }
func (c *Users) GetAll() error { return bosun.E(http.StatusUnsupportedMediaType, "x", nil) }
`,
		"example.com/b/users": `package users
func (c *Users) Get() error { return bosun.E(http.StatusGatewayTimeout, "slow", nil) }
`,
	})
	cases := map[string][]int{
		"example.com/a/users.(*Users).Get-fm":    {404},
		"example.com/a/users.(*Users).GetAll-fm": {415},
		"example.com/b/users.(*Users).Get-fm":    {504},
		"example.com/b/users.(*Users).GetAll-fm": nil, // a's GetAll must not leak
		"example.com/c/users.(*Users).Get-fm":    nil, // name key is ambiguous
	}
	for i := 0; i < 20; i++ { // map order must not matter
		for handler, want := range cases {
			if got := s.statusesFor(handler); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s = %v, want %v", handler, got, want)
			}
		}
	}
}

func TestScanByNameFallback(t *testing.T) {
	// A package whose import path is unknown is matched by package name.
	s := scanSources(t, map[string]string{"": `package users
func (c *Users) Get() error { return bosun.E(409, "dup", nil) }
`})
	if got := s.statusesFor("example.com/app/users.(*Users).Get-fm"); !reflect.DeepEqual(got, []int{409}) {
		t.Fatalf("fallback = %v", got)
	}
	if got := s.statusesFor("example.com/app/orders.(*Users).Get-fm"); got != nil {
		t.Fatalf("other package matched: %v", got)
	}
}

func TestScanGenericAndValueReceivers(t *testing.T) {
	s := scanSources(t, map[string]string{"example.com/repo": `package repo
func (r *Repo[T]) Get() error     { return bosun.E(410, "gone", nil) }
func (r Store[K, V]) List() error { return bosun.E(423, "locked", nil) }
`})
	if got := s.statusesFor("example.com/repo.(*Repo[...]).Get-fm"); !reflect.DeepEqual(got, []int{410}) {
		t.Fatalf("generic pointer receiver = %v", got)
	}
	if got := s.statusesFor("example.com/repo.Store[...].List-fm"); !reflect.DeepEqual(got, []int{423}) {
		t.Fatalf("generic value receiver = %v", got)
	}
	if _, ok := s.byPath["example.com/repo.(*)."+"Get"]; ok {
		t.Fatal("generic receiver recorded as (*)")
	}
}

func TestParseHandler(t *testing.T) {
	cases := []struct{ in, pkg, recv, method string }{
		{"example.com/app/users.(*Users).Get-fm", "example.com/app/users", "Users", "Get"},
		{"main.(*C).Login-fm", "main", "C", "Login"},
		{"gopkg.in/x%2ev2.V.Get-fm", "gopkg.in/x.v2", "V", "Get"},
		{"example.com/r.(*Repo[...]).Get-fm", "example.com/r", "Repo", "Get"},
	}
	for _, c := range cases {
		pkg, recv, method, ok := parseHandler(c.in)
		if !ok || pkg != c.pkg || recv != c.recv || method != c.method {
			t.Errorf("%s -> %q %q %q %v", c.in, pkg, recv, method, ok)
		}
	}
	for _, in := range []string{"main.handler", "main.(*C).Routes.func1", "nope"} {
		if _, _, _, ok := parseHandler(in); ok {
			t.Errorf("%s should not parse as a method", in)
		}
	}
}

func TestStatusNamesFromNetHTTP(t *testing.T) {
	want := map[string]int{
		"StatusContinue": http.StatusContinue, "StatusEarlyHints": http.StatusEarlyHints,
		"StatusNonAuthoritativeInfo": http.StatusNonAuthoritativeInfo, "StatusMultiStatus": http.StatusMultiStatus,
		"StatusIMUsed": http.StatusIMUsed, "StatusPermanentRedirect": http.StatusPermanentRedirect,
		"StatusBadRequest": http.StatusBadRequest, "StatusProxyAuthRequired": http.StatusProxyAuthRequired,
		"StatusPreconditionFailed": http.StatusPreconditionFailed, "StatusRequestEntityTooLarge": http.StatusRequestEntityTooLarge,
		"StatusRequestURITooLong": http.StatusRequestURITooLong, "StatusUnsupportedMediaType": http.StatusUnsupportedMediaType,
		"StatusRequestedRangeNotSatisfiable": http.StatusRequestedRangeNotSatisfiable, "StatusTeapot": http.StatusTeapot,
		"StatusMisdirectedRequest": http.StatusMisdirectedRequest, "StatusUnprocessableEntity": http.StatusUnprocessableEntity,
		"StatusLocked": http.StatusLocked, "StatusFailedDependency": http.StatusFailedDependency,
		"StatusTooEarly": http.StatusTooEarly, "StatusPreconditionRequired": http.StatusPreconditionRequired,
		"StatusRequestHeaderFieldsTooLarge": http.StatusRequestHeaderFieldsTooLarge,
		"StatusUnavailableForLegalReasons":  http.StatusUnavailableForLegalReasons,
		"StatusGatewayTimeout":              http.StatusGatewayTimeout, "StatusHTTPVersionNotSupported": http.StatusHTTPVersionNotSupported,
		"StatusVariantAlsoNegotiates": http.StatusVariantAlsoNegotiates, "StatusLoopDetected": http.StatusLoopDetected,
		"StatusNetworkAuthenticationRequired": http.StatusNetworkAuthenticationRequired,
	}
	for name, code := range want {
		if statusNames[name] != code {
			t.Errorf("statusNames[%s] = %d, want %d", name, statusNames[name], code)
		}
	}
}

func TestImportPathFromGoMod(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "internal", "users")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := "package users\nfunc (c *Users) Get() error { return bosun.E(404, \"x\", nil) }\n"
	if err := os.WriteFile(filepath.Join(sub, "users.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &errScanner{opts: &ScanOptions{SourceDir: root}}
	if got := s.statusesFor("example.com/app/internal/users.(*Users).Get-fm"); !reflect.DeepEqual(got, []int{404}) {
		t.Fatalf("disk scan = %v (byPath %v)", got, s.byPath)
	}
}
