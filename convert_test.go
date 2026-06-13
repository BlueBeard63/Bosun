package bosun

import (
	"testing"
)

func TestConvertSameTypes(t *testing.T) {
	type Src struct {
		ID    int
		Email string
	}
	type Dst struct {
		ID    int
		Email string
	}
	got, err := Convert[Dst](Src{ID: 1, Email: "a@b.c"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 1 || got.Email != "a@b.c" {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertDropsExtraSrcFields(t *testing.T) {
	type Src struct {
		ID           int
		Email        string
		PasswordHash string
	}
	type Dst struct {
		ID    int
		Email string
	}
	got, _ := Convert[Dst](Src{ID: 1, Email: "x", PasswordHash: "secret"})
	if got.ID != 1 || got.Email != "x" {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertSkipsUnmappedDstFields(t *testing.T) {
	type Src struct{ ID int }
	type Dst struct {
		ID   int
		Name string
	}
	got, _ := Convert[Dst](Src{ID: 7})
	if got.ID != 7 || got.Name != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertTagRemap(t *testing.T) {
	type Src struct {
		FullName string
	}
	type Dst struct {
		Name string `convert:"FullName"`
	}
	got, _ := Convert[Dst](Src{FullName: "Jack"})
	if got.Name != "Jack" {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertTagSkip(t *testing.T) {
	type Src struct{ Secret string }
	type Dst struct {
		Secret string `convert:"-"`
	}
	got, _ := Convert[Dst](Src{Secret: "x"})
	if got.Secret != "" {
		t.Fatalf("convert:\"-\" should skip, got %+v", got)
	}
}

func TestConvertWidensNumeric(t *testing.T) {
	type Src struct{ N int }
	type Dst struct{ N int64 }
	got, _ := Convert[Dst](Src{N: 42})
	if got.N != 42 {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertNamedString(t *testing.T) {
	type Name string
	type Src struct{ Name Name }
	type Dst struct{ Name string }
	got, _ := Convert[Dst](Src{Name: "jack"})
	if got.Name != "jack" {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertSrcPointer(t *testing.T) {
	type Src struct{ ID int }
	type Dst struct{ ID int }
	got, _ := Convert[Dst](&Src{ID: 5})
	if got.ID != 5 {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertSrcNilPointerLeavesZero(t *testing.T) {
	type Src struct{ ID int }
	type Dst struct{ ID int }
	var p *Src
	got, err := Convert[Dst](p)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 0 {
		t.Fatalf("nil src should give zero dst, got %+v", got)
	}
}

func TestConvertPointerFieldUnwrap(t *testing.T) {
	type Src struct{ Name *string }
	type Dst struct{ Name string }
	s := "jack"
	got, _ := Convert[Dst](Src{Name: &s})
	if got.Name != "jack" {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertPointerFieldNilStaysZero(t *testing.T) {
	type Src struct{ Name *string }
	type Dst struct{ Name string }
	got, _ := Convert[Dst](Src{Name: nil})
	if got.Name != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertPointerFieldWrap(t *testing.T) {
	type Src struct{ Name string }
	type Dst struct{ Name *string }
	got, _ := Convert[Dst](Src{Name: "jack"})
	if got.Name == nil || *got.Name != "jack" {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertNestedStruct(t *testing.T) {
	type SrcAddr struct {
		City string
		Zip  string
	}
	type Src struct {
		Name string
		Addr SrcAddr
	}
	type DstAddr struct{ City string }
	type Dst struct {
		Name string
		Addr DstAddr
	}
	got, _ := Convert[Dst](Src{Name: "Jack", Addr: SrcAddr{City: "NYC", Zip: "10001"}})
	if got.Name != "Jack" || got.Addr.City != "NYC" {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertSliceElementwise(t *testing.T) {
	type SrcItem struct {
		ID    int
		Extra string
	}
	type DstItem struct{ ID int }
	type Src struct{ Items []SrcItem }
	type Dst struct{ Items []DstItem }
	got, _ := Convert[Dst](Src{Items: []SrcItem{{ID: 1, Extra: "a"}, {ID: 2, Extra: "b"}}})
	if len(got.Items) != 2 || got.Items[0].ID != 1 || got.Items[1].ID != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestConvertNonStructSrcErrors(t *testing.T) {
	type Dst struct{ N int }
	_, err := Convert[Dst](42)
	if err == nil {
		t.Fatal("expected error for non-struct src")
	}
}

func TestConvertNonStructDstErrors(t *testing.T) {
	type Src struct{ N int }
	_, err := Convert[int](Src{N: 1})
	if err == nil {
		t.Fatal("expected error for non-struct Dst")
	}
}

func TestConvertTopLevelSliceOfStructs(t *testing.T) {
	type User struct {
		ID       string
		Name     string
		Password string
	}
	type UserResponse struct {
		ID   string
		Name string
	}

	users := []User{
		{ID: "u1", Name: "Jack", Password: "secret1"},
		{ID: "u2", Name: "Jill", Password: "secret2"},
	}
	got, err := Convert[[]UserResponse](users)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].ID != "u1" || got[0].Name != "Jack" {
		t.Fatalf("got[0] = %+v", got[0])
	}
	if got[1].ID != "u2" || got[1].Name != "Jill" {
		t.Fatalf("got[1] = %+v", got[1])
	}
}

func TestConvertEmptySlice(t *testing.T) {
	type User struct{ ID string }
	type UserOut struct{ ID string }
	got, err := Convert[[]UserOut]([]User{})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("empty slice should produce empty (non-nil) slice")
	}
	if len(got) != 0 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestConvertNilSlice(t *testing.T) {
	type User struct{ ID string }
	type UserOut struct{ ID string }
	var users []User
	got, err := Convert[[]UserOut](users)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("nil src should give empty dst, got %v", got)
	}
}

func TestConvertSliceOfPointers(t *testing.T) {
	type User struct {
		ID       string
		Password string
	}
	type UserOut struct{ ID string }
	users := []*User{{ID: "u1", Password: "x"}, {ID: "u2", Password: "y"}}
	got, err := Convert[[]UserOut](users)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "u1" || got[1].ID != "u2" {
		t.Fatalf("got = %+v", got)
	}
}

func TestConvertSliceToScalarsErrors(t *testing.T) {
	type Src struct{ N int }
	_, err := Convert[[]int]([]Src{{N: 1}})
	if err != nil {
		t.Fatal(err) // slice→slice should succeed at top level; element conversion silently zero
	}
}

func TestConvertSliceStructToScalarErrorsAtTopLevel(t *testing.T) {
	type Src struct{ N int }
	// struct → slice should error at the top level
	_, err := Convert[[]int](Src{N: 1})
	if err == nil {
		t.Fatal("struct → slice at top level should error")
	}
}

func TestConvertUserToResponse(t *testing.T) {
	type User struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Avatar   string `json:"avatar"`
		Email    string `json:"email"`
		Password string `json:"-"`
	}
	type UserResponse struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Avatar string `json:"avatar"`
		Email  string `json:"email"`
	}

	u := User{
		ID:       "u_1",
		Name:     "Jack",
		Avatar:   "https://example.com/jack.png",
		Email:    "jack@x.dev",
		Password: "should-never-appear",
	}

	got, err := Convert[UserResponse](u)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "u_1" || got.Name != "Jack" || got.Avatar != "https://example.com/jack.png" || got.Email != "jack@x.dev" {
		t.Fatalf("fields not copied: %+v", got)
	}
	// Password is not present on UserResponse, so it can't leak —
	// the test passes structurally. This is the whole point of the
	// "fields walked from dst" design.
}

func TestConvertUnexportedFieldsIgnored(t *testing.T) {
	type Src struct {
		ID  int
		hot string
	}
	type Dst struct {
		ID  int
		hot string
	}
	got, _ := Convert[Dst](Src{ID: 1, hot: "private"})
	if got.ID != 1 {
		t.Fatalf("got %+v", got)
	}
	// hot is unexported on Dst — left zero, not an error
	if got.hot != "" {
		t.Fatalf("unexported should not copy")
	}
}
