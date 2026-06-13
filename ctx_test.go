package bosun

import (
	"context"
	"testing"
)

type fakeUser struct {
	ID   int
	Name string
}

type fakeOrg struct {
	ID int
}

func TestWithValueAndValue(t *testing.T) {
	ctx := context.Background()
	if got := Value[fakeUser](ctx); got != nil {
		t.Fatalf("empty ctx should return nil, got %+v", got)
	}

	u := &fakeUser{ID: 1, Name: "jack"}
	ctx = WithValue(ctx, u)

	got := Value[fakeUser](ctx)
	if got == nil || got.ID != 1 || got.Name != "jack" {
		t.Fatalf("Value[fakeUser] = %+v, want %+v", got, u)
	}
}

func TestValuesOfDifferentTypesDoNotCollide(t *testing.T) {
	ctx := context.Background()
	u := &fakeUser{ID: 7}
	o := &fakeOrg{ID: 9}
	ctx = WithValue(ctx, u)
	ctx = WithValue(ctx, o)

	if Value[fakeUser](ctx).ID != 7 {
		t.Fatal("user slot clobbered")
	}
	if Value[fakeOrg](ctx).ID != 9 {
		t.Fatal("org slot clobbered")
	}
}

func TestWithValueLaterOverrides(t *testing.T) {
	ctx := WithValue(context.Background(), &fakeUser{ID: 1})
	ctx = WithValue(ctx, &fakeUser{ID: 2})
	if Value[fakeUser](ctx).ID != 2 {
		t.Fatal("later WithValue should override earlier one for the same T")
	}
}
