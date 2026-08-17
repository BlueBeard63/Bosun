# bosun.Convert: struct to struct mapping

`bosun.Convert[Dst, Src]` copies fields from one struct into another by name. It is the everyday mapper for turning a database model into an API response. Because it walks the destination's fields, anything not declared on the destination is structurally impossible to leak, which makes the destination type your contract.

## The canonical case

A response DTO omits the sensitive fields, and the mapper simply cannot copy what has no destination.

```go
type User struct {
    ID       string `json:"id"`
    Name     string `json:"name"`
    Email    string `json:"email"`
    Password string `json:"-"` // never wanted on the wire
}

type UserResponse struct {
    ID    string `json:"id"`
    Name  string `json:"name"`
    Email string `json:"email"`
}

func (c *Users) Get(ctx context.Context, req *bosun.Req[GetIn]) (UserResponse, error) {
    u, err := c.Repo.Find(ctx, req.Body.ID)
    if err != nil {
        return UserResponse{}, mapErr(err)
    }
    return bosun.Convert[UserResponse](*u)
}
```

`Password` exists on `User` but not on `UserResponse`, so it is never read. The DTO is the contract and the mapper cannot widen it.

## Matching rules

`Convert` walks the destination's exported fields, and for each one it looks up a same-named field on the source. The default match is the exact field name, with no case folding. A `convert:"OtherName"` tag overrides the lookup name, and a `convert:"-"` tag skips the field entirely. Fields with no match are left at their zero value without error, and unexported fields are skipped on both sides.

```go
type Dst struct {
    Name  string                       // matches Src.Name
    Bio   string `convert:"Description"` // matches Src.Description
    Token string `convert:"-"`           // never copied
}
```

## Type rules

Same types are assigned directly. Convertible scalars (numeric widening, a named type and its underlying type) are converted. A pointer is dereferenced into a value field when non-nil, or wrapped into a pointer field. Nested structs are converted recursively, and slices and maps are converted element by element. Anything the rules do not cover is silently skipped.

## What it does not do

`Convert` is a shallow, reflection-based field copier, not a deep cloner. It does not decode JSON, parse strings, run hooks, or validate. When source and destination types disagree in a way the rules do not cover, the destination field is left at zero rather than raising an error. For strict behavior or a hot path, write the mapper by hand.

## Common shapes

Map a model to a response, passing the value or a pointer.

```go
return bosun.Convert[UserResponse](*user)
```

Map a request DTO into a model, then fill in the fields the DTO does not carry.

```go
u, _ := bosun.Convert[User](in)
u.CreatedAt = time.Now()
```

Map a top-level slice, where the source may be `[]User`, `[]*User`, or `nil`.

```go
return bosun.Convert[[]UserResponse](users)
```

Rename fields on the way out, decoupling wire names from storage names without writing a mapper.

```go
type OrderOut struct {
    ID   string `convert:"OrderID" json:"id"`
    User string `convert:"BuyerID" json:"user"`
}
```

## Errors

`Convert` returns an error only when the inputs are structurally invalid: the source is not a struct or pointer to struct, or the destination is not a struct. Every other mismatch is skipped silently.
