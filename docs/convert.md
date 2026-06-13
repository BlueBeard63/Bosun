# `bosun.Convert` — struct → struct mapping

`bosun.Convert[Dst, Src]` copies fields from one struct into another by
name. It's the everyday "DB model → API response" mapper. Walking from
the destination's fields means anything not declared on the destination
is structurally impossible to leak.

---

## The canonical case

```go
type User struct {
    ID       string `json:"id"`
    Name     string `json:"name"`
    Avatar   string `json:"avatar"`
    Email    string `json:"email"`
    Password string `json:"-"`           // never want this on the wire
}

type UserResponse struct {
    ID     string `json:"id"`
    Name   string `json:"name"`
    Avatar string `json:"avatar"`
    Email  string `json:"email"`
}

func (c *Users) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserResponse, error) {
    u, err := c.Repo.Find(ctx, req.Body.ID)
    if err != nil { return UserResponse{}, mapErr(err) }
    return bosun.Convert[UserResponse](*u)
}
```

`Password` exists on `User` but not on `UserResponse`, so it is never
looked at — there is no field to copy *to*. The DTO is the contract; the
mapper can't accidentally widen it.

---

## Matching rules

`Convert` walks the **destination**'s exported fields. For each, it looks
up a same-named field on the source:

```go
type Dst struct {
    Name string         // matches Src.Name (case-sensitive)
    Age  int            // matches Src.Age
    Bio  string `convert:"Description"`   // matches Src.Description
    Token string `convert:"-"`            // never copied, even if Src.Token exists
}
```

- Default match is exact field name. No case folding, no fuzzy match.
- `convert:"OtherName"` overrides the lookup name for that field.
- `convert:"-"` skips the field entirely.
- Fields with no match are left at zero — no error.
- Unexported fields are skipped (on both sides).

---

## Type rules

| `Src` field            | `Dst` field            | Behavior                                     |
| ---------------------- | ---------------------- | -------------------------------------------- |
| `T`                    | `T`                    | direct assign                                |
| `int`                  | `int64`                | `reflect.Value.Convert` (numeric widening)   |
| `MyString` (`~string`) | `string`               | `reflect.Value.Convert` (named ↔ underlying) |
| `*T`                   | `T` (non-pointer)      | deref if non-nil; nil leaves dst zero        |
| `T`                    | `*T`                   | allocate + assign                            |
| `*T`                   | `*T`                   | nil src → nil dst; non-nil → new pointer     |
| `struct{...}` (Src)    | `struct{...}` (Dst)    | recursive Convert on the nested struct       |
| `[]A`                  | `[]B`                  | element-wise convert via the same rules      |
| `map[K]A`              | `map[K]B`              | element-wise convert (same key type)         |
| anything else          | anything else          | silently skipped                             |

Convertible-but-not-simple pairs (e.g. `[]byte` ↔ `string`) work because
`reflect.ConvertibleTo` says so and the kind is in the simple-convert
set. For anything weirder, do the mapping by hand.

---

## What it doesn't do

- It is **not** a deep cloner. Same-type field assignment is a shallow
  copy.
- It does **not** decode JSON, parse strings, or run hooks. It's a
  reflection-based field copier, that's it.
- It does **not** validate. If types disagree in a way the matrix above
  doesn't cover, the dst field is left at zero — no error.
- It is **not** the fastest mapper in the world. Reflection per call. For
  hot paths, write a hand-rolled mapper.

---

## Common shapes

### DB model → API response (the canonical one)

```go
return bosun.Convert[UserResponse](*user)
```

Pass the value (or pointer — `*Src` works too). The dst type drives the
result; secrets/internal fields can't escape if they aren't declared on
the response type.

### Request DTO → DB model (then mutate)

```go
type CreateUserIn struct {
    Email string `json:"email"`
    Name  string `json:"name"`
}

func (r *UserRepo) Create(ctx context.Context, in CreateUserIn) (*User, error) {
    u, _ := bosun.Convert[User](in)
    u.PasswordHash = ""               // fill in fields not on the DTO
    u.CreatedAt = time.Now()
    return u, r.db.WithContext(ctx).Create(u).Error
}
```

`Convert` populates the shared fields; the repo fills in the rest.

### Top-level slice mapping

```go
func (c *Users) List(ctx context.Context, _ *bosun.Req[struct{}]) ([]UserResponse, error) {
    users, err := c.UserService.GetUsers(...)   // []models.User
    if err != nil { return nil, err }
    return bosun.Convert[[]UserResponse](users)
}
```

The src can also be `[]*User` (pointer elements auto-deref) or `nil` (you
get an empty slice). Each element is converted by the standard rules.

### Mapping slices nested in a struct

```go
type UsersResponse struct {
    Items []UserResponse `json:"items"`
}
type UsersList struct {
    Items []User
}

out, _ := bosun.Convert[UsersResponse](list)
// out.Items is []UserResponse with Password stripped from every entry
```

The slice element types differ but their fields overlap by name, so the
recursive rule maps element-wise.

### Renaming on the way out

```go
type DBOrder struct {
    OrderID  string
    BuyerID  string
}
type OrderOut struct {
    ID   string `convert:"OrderID" json:"id"`
    User string `convert:"BuyerID" json:"user"`
}
```

`convert:` tags decouple wire names from storage names without writing a
mapper function.

---

## Errors

`Convert` returns an error only when the inputs are structurally invalid:

- `src` is not a struct or `*struct`.
- `Dst` is not a struct.

Anything else — fields without matches, fields with incompatible types —
is silently skipped. If you want strict behavior, write a mapping function
by hand and assert on each field.
