package registry

import (
	"reflect"
	"strings"
	"testing"
)

func TestRuntimeTypeAPI(t *testing.T) {
	r := New()
	type thing struct{ n int }
	tt := reflect.TypeOf((*thing)(nil))

	if r.Has(tt) {
		t.Fatal("Has should be false before registration")
	}
	r.RegisterType(tt, func(*Registry) (any, error) { return &thing{n: 7}, nil })
	if !r.Has(tt) {
		t.Fatal("Has should be true after registration")
	}
	v, err := r.ResolveType(tt)
	if err != nil || v.(*thing).n != 7 {
		t.Fatalf("ResolveType failed: %v %v", err, v)
	}

	defer func() {
		if recover() == nil {
			t.Fatal("duplicate RegisterType must panic")
		}
	}()
	r.RegisterType(tt, func(*Registry) (any, error) { return nil, nil })
}

func TestMustResolvePanics(t *testing.T) {
	r := New()
	defer func() {
		if recover() == nil {
			t.Fatal("MustResolve must panic on missing provider")
		}
	}()
	MustResolve[*testing.T](r)
}

func TestRequireChainInError(t *testing.T) {
	r := New()
	type outerSvc struct{}
	Register[*outerSvc](r, func(r *Registry) (*outerSvc, error) {
		if _, err := Resolve[*testing.T](r); err != nil {
			return nil, err
		}
		return &outerSvc{}, nil
	})
	_, err := Resolve[*outerSvc](r)
	if err == nil || !strings.Contains(err.Error(), "required by") {
		t.Fatalf("error should include the require chain, got %v", err)
	}
}
