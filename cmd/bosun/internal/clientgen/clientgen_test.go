package clientgen_test

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/bluebeard63/bosun/cmd/bosun/internal/clientgen"
)

func TestGenerate(t *testing.T) {
	routes := []clientgen.Route{
		{Method: "GET", Path: "/users/{id}", Operation: "app.(*Users).Get", In: "struct {}", Out: "contracts.User", OutImport: "example.com/app/contracts"},
		{Method: "POST", Path: "/users", Operation: "app.(*Users).Create", In: "contracts.CreateUserIn", InImport: "example.com/app/contracts", Out: "contracts.User", OutImport: "example.com/app/contracts"},
		{Method: "DELETE", Path: "/users/{id}", Operation: "app.(*Users).Delete", In: "struct {}", Out: "struct {}"},
	}

	src, err := clientgen.Generate("client", "billing", routes)
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, src)
	}

	// Generate runs gofmt, so success means the source is valid Go; parse again
	// as a belt-and-suspenders check.
	if _, err := parser.ParseFile(token.NewFileSet(), "", src, parser.AllErrors); err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, src)
	}

	want := []string{
		"type BillingClient struct",
		"func NewBillingClient(baseURL string) *BillingClient",
		"func (c *BillingClient) Get(ctx context.Context, id string) (contracts.User, error)",
		"func (c *BillingClient) Create(ctx context.Context, in contracts.CreateUserIn) (contracts.User, error)",
		"func (c *BillingClient) Delete(ctx context.Context, id string) (struct{}, error)",
		`"example.com/app/contracts"`,
		"url.PathEscape(id)",
	}
	for _, w := range want {
		if !strings.Contains(src, w) {
			t.Errorf("generated client missing:\n  %s\n--- source ---\n%s", w, src)
		}
	}
}

func TestGenerateEmpty(t *testing.T) {
	// No routes should still produce a compilable client.
	src, err := clientgen.Generate("client", "empty", nil)
	if err != nil {
		t.Fatalf("generate: %v\n%s", err, src)
	}
	if !strings.Contains(src, "type EmptyClient struct") {
		t.Fatalf("missing client type:\n%s", src)
	}
}
