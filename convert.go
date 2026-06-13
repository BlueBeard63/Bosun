package bosun

import (
	"fmt"
	"reflect"
)

// --- struct conversion ---
//
// Convert copies fields from one struct to another by name. It's intended
// for the everyday "DB model ↔ API DTO" mapping, not as a general-purpose
// object mapper. Keep both ends close in shape; reach for a hand-written
// mapping when they aren't.

// Convert returns a new Dst populated from src.
//
// Both struct→struct and slice→slice (of structs or scalars) work at the
// top level. For struct conversions, fields are matched by exact name;
// override with `convert:"OtherName"` or skip with `convert:"-"`.
//
// Type rules, applied per matched pair:
//
//   - identical types → direct assignment.
//   - convertible types (int → int64, named string → string, etc.) →
//     reflect.Value.Convert.
//   - both pointers → recurse on the pointed-to values (nil src leaves dst nil).
//   - dst pointer, src value → allocate, then convert.
//   - src pointer, dst value → deref (nil src leaves dst zero), then convert.
//   - both structs of different types → recursive Convert on the struct.
//   - slices of compatible element types → element-wise convert.
//   - maps with the same key type and compatible value types → element-wise convert.
//   - anything else → silently skipped.
//
// Fields that don't match anything on the other side are left at zero.
// The only error returned is when src and Dst aren't both structs or both
// slices at the top level.
//
//	type DBUser  struct { ID uint; Email string; PasswordHash string }
//	type UserOut struct { ID uint; Email string }
//	out,  _ := bosun.Convert[UserOut](dbUser)      // PasswordHash is dropped
//	outs, _ := bosun.Convert[[]UserOut](dbUsers)   // []DBUser → []UserOut
func Convert[Dst any, Src any](src Src) (Dst, error) {
	var dst Dst

	srcV := reflect.ValueOf(src)
	if srcV.Kind() == reflect.Pointer {
		if srcV.IsNil() {
			return dst, nil
		}
		srcV = srcV.Elem()
	}

	dstV := reflect.ValueOf(&dst).Elem()

	switch {
	case srcV.Kind() == reflect.Struct && dstV.Kind() == reflect.Struct:
		convertStruct(dstV, srcV)
		return dst, nil

	case srcV.Kind() == reflect.Slice && dstV.Kind() == reflect.Slice:
		out := reflect.MakeSlice(dstV.Type(), srcV.Len(), srcV.Len())
		for i := 0; i < srcV.Len(); i++ {
			assignConvert(out.Index(i), srcV.Index(i))
		}
		dstV.Set(out)
		return dst, nil

	default:
		return dst, fmt.Errorf("bosun.Convert: src and Dst must both be structs or both slices, got %s -> %s",
			srcV.Kind(), dstV.Kind())
	}
}

func convertStruct(dst, src reflect.Value) {
	dstT := dst.Type()
	srcT := src.Type()
	for i := 0; i < dstT.NumField(); i++ {
		df := dstT.Field(i)
		if !df.IsExported() {
			continue
		}
		name := df.Name
		if tag := df.Tag.Get("convert"); tag != "" {
			if tag == "-" {
				continue
			}
			name = tag
		}
		sf, ok := srcT.FieldByName(name)
		if !ok {
			continue
		}
		sv := src.FieldByIndex(sf.Index)
		dv := dst.Field(i)
		assignConvert(dv, sv)
	}
}

func assignConvert(dst, src reflect.Value) {
	if !dst.CanSet() {
		return
	}

	// Unwrap source pointer.
	for src.Kind() == reflect.Pointer {
		if src.IsNil() {
			return
		}
		src = src.Elem()
	}

	dstT := dst.Type()
	srcT := src.Type()

	// Identical types.
	if srcT == dstT {
		dst.Set(src)
		return
	}

	// Destination is a pointer — allocate then recurse on its element.
	if dstT.Kind() == reflect.Pointer {
		elem := reflect.New(dstT.Elem())
		assignConvert(elem.Elem(), src)
		if !elem.Elem().IsZero() {
			dst.Set(elem)
		}
		return
	}

	// Convertible scalars / named types / etc.
	if srcT.ConvertibleTo(dstT) && isSimpleConvert(dstT) {
		dst.Set(src.Convert(dstT))
		return
	}

	// Nested struct of a different type → recurse field-by-field.
	if srcT.Kind() == reflect.Struct && dstT.Kind() == reflect.Struct {
		convertStruct(dst, src)
		return
	}

	// Slices of compatible elements.
	if srcT.Kind() == reflect.Slice && dstT.Kind() == reflect.Slice {
		out := reflect.MakeSlice(dstT, src.Len(), src.Len())
		for i := 0; i < src.Len(); i++ {
			assignConvert(out.Index(i), src.Index(i))
		}
		dst.Set(out)
		return
	}

	// Maps with same key type and compatible value types.
	if srcT.Kind() == reflect.Map && dstT.Kind() == reflect.Map &&
		srcT.Key() == dstT.Key() {
		out := reflect.MakeMapWithSize(dstT, src.Len())
		iter := src.MapRange()
		for iter.Next() {
			v := reflect.New(dstT.Elem()).Elem()
			assignConvert(v, iter.Value())
			out.SetMapIndex(iter.Key(), v)
		}
		dst.Set(out)
		return
	}

	// Anything else: silently skip.
}

// isSimpleConvert filters reflect.ConvertibleTo down to the safe cases —
// numeric widening, named string <-> string, bool, etc. We intentionally
// exclude struct→struct (handled recursively) and slice/map (handled
// explicitly) so we don't accidentally bit-copy structurally distinct
// types just because reflect says they're convertible.
func isSimpleConvert(dst reflect.Type) bool {
	switch dst.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	}
	return false
}
