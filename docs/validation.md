# Validation

Typed handlers validate their input before your code runs. You declare rules on the `In` struct with `validate:"..."` tags, and add a `Validate()` method for anything the tags can't express. A request that fails is answered with a `400` that names every invalid field, and the handler is never called. Validation adds no dependencies. Its rules also appear in the [OpenAPI spec](./openapi.md).

## Add rules to a request type

```go
type CreateUserIn struct {
    OrgID int      `path:"org_id"    validate:"min=1"`
    Email string   `json:"email"     validate:"required,max=254"`
    Name  string   `json:"name"      validate:"required,min=2,max=50"`
    Role  string   `json:"role"      validate:"omitempty,oneof=admin editor viewer"`
    Age   int      `json:"age"       validate:"omitempty,min=13"`
    Tags  []string `json:"tags"      validate:"max=10"`
}

func (c *Users) Create(ctx context.Context, req *bosun.Req[CreateUserIn]) (UserOut, error) {
    // req.Body has passed every rule by the time this runs.
}
```

Rules are separated by commas and checked in order. The first rule that fails is reported for each field. Validation runs after all binding is done (JSON or form body, then `path`, `query`, `header` and `form` tags), so it sees the final value of each field whatever its source.

## What a failure looks like

A request to the route above with `{"name":"J","role":"owner"}` and a path of `/orgs/0/users` gets:

```http
HTTP/1.1 400 Bad Request
Content-Type: application/json
```

```json
{
  "error": "validation failed",
  "fields": [
    { "field": "org_id", "in": "path", "rule": "min",      "param": "1", "message": "must be at least 1" },
    { "field": "email",  "in": "body", "rule": "required",               "message": "is required" },
    { "field": "name",   "in": "body", "rule": "min",      "param": "2", "message": "must be at least 2 characters" },
    { "field": "role",   "in": "body", "rule": "oneof",    "param": "admin editor viewer", "message": "must be one of: admin, editor, viewer" }
  ]
}
```

- `field` is the name the client used: the `json`, `path`, `query`, `header` or `form` tag name, not the Go field name.
- `in` says where the value came from: `body`, `path`, `query`, `header` or `form`.
- Fields are listed in struct declaration order, so the same input always produces the same output.
- Messages are written for API clients and never contain Go type names or internal details.

## Optional fields and zero values

Go can't tell a missing value from a zero value, so it matters how you mark optional fields:

| Field | Tag | Value absent or zero | Value present |
|---|---|---|---|
| `Name string` | `required,min=2` | fails `required` | checked against `min` |
| `Age int` | `min=13` | **checked**: `0` fails `min=13` | checked |
| `Age int` | `omitempty,min=13` | skipped | checked |
| `Nick *string` | `min=3` | `nil` is skipped | checked, even when `""` |
| `Nick *string` | `required,min=3` | `nil` fails `required` | checked |

Use `omitempty` for optional fields whose zero value isn't meaningful. Use a pointer when a zero value is legitimate input but you still need to know whether the client sent it. For example, `*int` lets `0` be valid while still telling it apart from "not sent".

Rules without `omitempty` always run, which is what you want for path parameters: `/orgs/0/users` fails `min=1` instead of slipping through.

## Validate nested data

Rules on nested structs, pointers to structs, and slices of structs are checked too. Field names in errors show the path to the field:

```go
type OrderIn struct {
    Shipping Address   `json:"shipping"`
    Items    []LineIn  `json:"items" validate:"required,max=50"`
}

type LineIn struct {
    SKU string `json:"sku" validate:"required"`
    Qty int    `json:"qty" validate:"min=1,max=99"`
}
```

An invalid second line is reported as `"field": "items[1].qty"`. An address error is reported as `"field": "shipping.city"`. A `nil` pointer to a struct is not descended into.

## Write custom checks

For cross-field rules, or anything else tags can't express, implement `bosun.Validator` on the request type:

```go
type BookingIn struct {
    Start time.Time `json:"start" validate:"required"`
    End   time.Time `json:"end"   validate:"required"`
}

func (in BookingIn) Validate() error {
    if !in.End.After(in.Start) {
        return bosun.Invalid("end", "must be after start")
    }
    return nil
}
```

`Validate` runs only after every tag rule has passed, so it can rely on required fields being present. What you return decides the response:

| Return value | Response |
|---|---|
| `nil` | The handler runs. |
| `bosun.Invalid(field, message)` or any `*bosun.ValidationError` | `400` with those field errors. The rule is `custom` for `Invalid`; build a `ValidationError` yourself to set your own `Rule` or `In`. |
| `bosun.E(status, message, cause)` | That status and message, for example `422` for a business rule. |
| Any other error | `400` with `{"error": err.Error()}`. The message is shown to the client, so keep it free of internals. |

`Validate` may have a value or a pointer receiver. It is only called on the top-level `In` type, not on nested structs.

## Binding errors use the same shape

Input that can't be converted into the `In` type is rejected in the same format before any rules run:

```json
{ "error": "invalid request",   "fields": [ { "field": "org_id", "in": "path", "rule": "type", "message": "must be an integer" } ] }
{ "error": "invalid JSON body", "fields": [ { "field": "age",    "in": "body", "rule": "type", "message": "must be an integer" } ] }
{ "error": "invalid JSON body" }
```

The last form is used for malformed JSON, where no single field is to blame. Decoder messages such as `json: cannot unmarshal ...` are never sent to clients.

A binding tag on a field type that binding doesn't support (for example `[]string` with a `query` tag) is a mistake in the code, not bad input. It is answered with a `500` and the real cause is kept for the audit log.

## Mistakes in tags fail at startup

`app.Start()` checks every `validate` tag on every typed route and returns an error for an unknown rule, a missing or non-numeric parameter, or a rule that can't apply to the field's type (such as `min` on a `bool`). A typo shows up when the app boots, not on the first request:

```
POST /users: invalid validate tag: CreateUserIn.Age: unknown validate rule "minimum"
```

## Rule reference

| Rule | Applies to | Passes when |
|---|---|---|
| `required` | any type | The value is not the zero value. A pointer must be non-nil (the value it points to may be zero). |
| `omitempty` | any type | Always passes. When the value is zero, the field's other rules are skipped. |
| `min=N` | strings | Length in characters (runes) is at least `N`. |
| | integers, floats | The value is at least `N`. |
| | slices, arrays, maps | The number of items is at least `N`. |
| `max=N` | same as `min` | Length, value or item count is at most `N`. |
| `oneof=a b c` | strings, integers, floats | The value equals one of the space-separated options. Numeric options must be numbers. |

`N` may be a decimal number, for example `max=0.5`.

## In the OpenAPI spec

When the [`openapi`](./openapi.md) package is imported, validation rules are documented in the spec:

| Rule | Body property | Query, header or path parameter |
|---|---|---|
| `required` | listed in the schema's `required` array | `"required": true` |
| `min` / `max` on a string | `minLength` / `maxLength` | same, on the parameter schema |
| `min` / `max` on a number | `minimum` / `maximum` | same |
| `min` / `max` on a slice | `minItems` / `maxItems` | n/a |
| `min` / `max` on a map | `minProperties` / `maxProperties` | n/a |
| `oneof` | `enum` (typed values) | `enum` |

Every typed route with input lists a `400` response that uses the `ValidationError` schema shown above. `Validate()` methods can't be inspected, so their checks don't appear in the spec. Declare any extra status they return with `bosun.Errors(...)`.

## API reference

```go
type FieldError struct {
    Field   string `json:"field"`
    In      string `json:"in,omitempty"`
    Rule    string `json:"rule"`
    Param   string `json:"param,omitempty"`
    Message string `json:"message"`
}

type ValidationError struct {
    Message string       // rendered as "error"; default "validation failed"
    Fields  []FieldError // rendered as "fields"
}

type Validator interface{ Validate() error }

func Invalid(field, message string) *ValidationError

type Rule struct{ Name, Param string }
func ParseRules(tag string) ([]Rule, error) // for tooling; used by the openapi package
```
