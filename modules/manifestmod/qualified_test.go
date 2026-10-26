package manifestmod

import (
	"reflect"
	"testing"
	"time"
)

type item struct{}

type page[T any] struct{ Items []T }

func TestQualifiedType(t *testing.T) {
	const pkg = "github.com/bluebeard63/bosun/modules/manifestmod"
	cases := []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeFor[string](), "string"},
		{reflect.TypeFor[struct{}](), "struct {}"},
		{reflect.TypeFor[any](), "interface {}"},
		{reflect.TypeFor[item](), pkg + ".item"},
		{reflect.TypeFor[[]item](), "[]" + pkg + ".item"},
		{reflect.TypeFor[map[string]*time.Time](), "map[string]*time.Time"},
		{reflect.TypeFor[[2]item](), "[2]" + pkg + ".item"},
		{reflect.TypeFor[page[item]](), pkg + ".page[" + pkg + ".item]"},
		{reflect.TypeFor[struct {
			At time.Time `json:"at"`
			item
		}](), `struct { At time.Time "json:\"at\""; ` + pkg + `.item }`},
	}
	for _, c := range cases {
		if got := qualifiedType(c.typ); got != c.want {
			t.Errorf("qualifiedType(%s) = %q, want %q", c.typ, got, c.want)
		}
	}
}
