# OpenAPI reference

This page describes exactly what `github.com/bluebeard63/bosun/openapi` generates. For tasks, see the [OpenAPI how-to guides](./openapi.md). For a walkthrough, see the [OpenAPI tutorial](./openapi-tutorial.md).

## Package API

| Symbol | Description |
|---|---|
| `SpecController` | Controller that serves the spec. Registered with prefix `""`, so it serves `GET /openapi.json`. Remount it with `bosun.OverridePrefix[openapi.SpecController](prefix)`. |
| `ScanOptions{SourceDir string}` | Where the source scanner looks for files on disk. Default: `&ScanOptions{SourceDir: "."}`. Override it with `registry.RegisterInstance[*openapi.ScanOptions]`. An empty `SourceDir` turns off on-disk scanning. |
| `Sources(fsys fs.FS) struct{}` | Registers an embedded source tree for scanning. Use it as `var _ = openapi.Sources(src)`. The registration applies to every app in the process. |

Importing the package registers the controller, the scanner service and the `ScanOptions` default. `bosun.Disable("github.com/bluebeard63/bosun/openapi")` removes all three from an app.

## Endpoint

`GET /openapi.json` responds with `200`, `Content-Type: application/json`, and an indented OpenAPI **3.0.3** document:

```json
{
  "openapi": "3.0.3",
  "info": { "title": "bosun API", "version": "1.0.0" },
  "paths": { "...": "..." },
  "components": { "schemas": { "...": "..." } }
}
```

The document is rebuilt on every request from the serving app's `app.TypedRoutes()`. The source scan runs once per app, on the first request.

## Operations

Each typed route (`bosun.Get`, `Post`, `Put`, `Delete`, `Patch`) becomes one operation:

| Spec field | Source |
|---|---|
| Path key | `RouteInfo.Path`: the controller prefix, group prefixes and the route path, with `:name` written as `{name}`. A route at `"/"` under `/notes` is `/notes`. A `{name...}` wildcard is written as `{name}`, and a `{$}` end anchor is dropped, so `/files/{path...}` becomes `/files/{path}` and `/notes/{$}` becomes `/notes/`. |
| Method key | The HTTP method, in lower case. |
| `operationId` | `<Controller><Method>`: the receiver type name without a `Controller` suffix, then the method name, e.g. `NotesGet` for `(*NotesController).Get`. Handlers that aren't methods use the lower-case HTTP method and the path's words, e.g. `get_notes_id`. Operations are numbered in path and method order, and an ID that is already taken gets the first free numeric suffix (`NotesGet2`), so every ID is unique. |
| `parameters`, path | One per `{name}` or `{name...}` segment: `in: path`, `required: true`. The schema comes from the `In` field tagged `path:"name"` (type plus [validation](./validation.md) constraints), or `{"type":"string"}` when there's no such field. |
| `parameters`, query | One per `In` field tagged `query:"name"`: `in: query`. `required` is `true` when the field has the `required` rule. The schema comes from the field type plus its constraints. |
| `parameters`, header | One per `In` field tagged `header:"Name"`, in the same way as query parameters. |
| `requestBody` | Only for `POST`, `PUT` and `PATCH`, when `In` has at least one exported field that is not tagged `path`, `query` or `header`. It is `required: true`, with content type `application/json` and the schema of `In`. |
| `responses` | See the next section. |

Raw routes (`r.Get`, `r.Post` and so on) are not included.

## Responses

| Code | When it is listed | Body schema |
|---|---|---|
| `200` | Always. Description `OK`. | Schema of `Out` |
| `400` | When `In` is a struct with at least one field (description "invalid request body or parameters"), or when one of the error layers adds it | `ValidationError` when `In` is a struct with fields, otherwise `ErrorResponse` |
| `500` | Always (description "internal server error") | `ErrorResponse` |
| Any other code | Declared, scanned or observed (see below). Description from `http.StatusText`. | `ErrorResponse` |

`ErrorResponse` is added to `components/schemas` the first time it is used:

```json
{ "type": "object", "properties": { "error": { "type": "string" } } }
```

It matches the body Bosun writes for handler errors: `{"error": "<public message>"}`.

`ValidationError` describes the body of binding and validation failures (see [validation](./validation.md#what-a-failure-looks-like)):

```json
{
  "type": "object",
  "required": ["error"],
  "properties": {
    "error": { "type": "string" },
    "fields": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["field", "rule", "message"],
        "properties": {
          "field": { "type": "string" },
          "in": { "type": "string", "enum": ["body", "path", "query", "header", "form"] },
          "rule": { "type": "string" },
          "param": { "type": "string" },
          "message": { "type": "string" }
        }
      }
    }
  }
}
```

### Error layers

| Layer | Source | Scope and lifetime |
|---|---|---|
| Declared | `bosun.Errors(codes...)` passed to the route | Fixed when the route is mounted |
| Scanned | `bosun.E(status, ...)` calls inside the handler method's body | Scanned once per app, on the first spec request |
| Observed | Statuses `>= 400` the route has returned, from `app.ObservedStatuses(method, path)` | Per app, in memory, reset on restart |

Codes from the three layers are combined, and each code appears once.

## Schema mapping

| Go type | Schema |
|---|---|
| `string` | `{"type":"string"}` |
| `bool` | `{"type":"boolean"}` |
| `int`, `int8` ... `int64`, `uint`, `uint8` ... `uint64` | `{"type":"integer"}` |
| `float32`, `float64` | `{"type":"number"}` |
| `time.Time` | `{"type":"string","format":"date-time"}` |
| `[]byte` | `{"type":"string","format":"byte"}` (base64, as `encoding/json` sends it) |
| `[]T`, `[N]T` | `{"type":"array","items":<T>}` |
| `map[K]V` | `{"type":"object","additionalProperties":<V>}` |
| `*T` | Same as `T` |
| Named struct | `{"$ref":"#/components/schemas/<TypeName>"}`, with the definition stored once under its bare type name. Recursive types are supported. |
| Anonymous struct | Inline `{"type":"object","properties":{...}}` |
| Anything else (interfaces, `any`, channels) | `{}` (any value) |

Struct properties:

Properties follow `encoding/json`'s naming rules:

- Only exported fields are included. Fields tagged `json:"-"` are left out.
- Fields tagged `path`, `query` or `header` are left out of object schemas; they are documented as parameters instead.
- The property name is the first part of the `json` tag (`json:"created_at,omitempty"` becomes `created_at`), or the Go field name when the tag has no name (`json:",omitempty"`) or there is no tag.
- The fields of an embedded struct without a `json` name are flattened into the outer object. When names conflict, the shallower field wins, then the one with a `json` tag; otherwise the name is left out.
- A number or `bool` field with the `,string` option is `{"type":"string"}`.
- Fields with the `required` validation rule are listed in the object's `required` array.

### Validation constraints

`validate` tags add JSON Schema keywords to inline property and parameter schemas:

| Rule | string | number | slice / array | map |
|---|---|---|---|---|
| `min=N` | `minLength` | `minimum` | `minItems` | `minProperties` |
| `max=N` | `maxLength` | `maximum` | `maxItems` | `maxProperties` |
| `oneof=a b` | `enum` of strings | `enum` of numbers | n/a | n/a |

Constraints aren't added to properties whose schema is a `$ref` (a named struct), because OpenAPI 3.0 ignores keywords next to `$ref`. Custom `Validate()` methods can't be inspected and don't appear in the spec.

## Source scanning rules

The scanner reads `.go` files from two places:

1. **Embedded trees** registered with `openapi.Sources`. Every `.go` file in them is read.
2. **On-disk files** under `ScanOptions.SourceDir`. It skips `_test.go` files and the directories `vendor`, `node_modules`, and any directory whose name starts with `.`.

A status is recorded only when all of these are true:

- The call is written inside the body of a **method**, such as `func (c *NotesController) Get(...)`. Pointer and value receivers both count, including receivers of generic types (`func (c *Repo[T]) Get(...)`). Plain functions and closures aren't scanned.
- The call has the form `<anything>.E(status, ...)`, for example `bosun.E(...)`.
- The status argument is an **integer literal** (`409`) or any `Status...` constant defined by `net/http`, such as `http.StatusUnsupportedMediaType`.

The scanner links recorded statuses to a route by matching the package import path, receiver type and method name exactly against the route's handler. The import path of an on-disk file comes from the nearest `go.mod`. Files at the root of an embedded tree belong to the package that called `openapi.Sources`, and files in its subdirectories to the packages below it. If a handler's package wasn't scanned under its import path, the scanner falls back to matching by package name, unless two scanned packages share that name. Files that fail to parse are skipped without an error.

## Limitations

These describe the current generator. Use [declarations](./openapi.md#declare-statuses-on-the-route) or your own tooling where they matter.

- **Fixed document info.** `info.title` is always `bosun API` and `info.version` is always `1.0.0`. There are no `servers`, `tags` or `securitySchemes`.
- **Success is always `200` with JSON.** A `string` or `[]byte` `Out` is still described as `application/json`, and an empty `struct{}` appears as an empty object. Bosun actually sends `text/plain`, `application/octet-stream` and no body respectively.
- **Form bodies are described as JSON.** Fields tagged `form` appear as JSON body properties, and form and multipart request bodies get an `application/json` content type.
- **Custom validation is invisible.** Checks made in `Validate()` methods aren't reflected in schemas.
- **Schema names can collide.** Two different types with the same name in different packages share one `components/schemas` entry.
- **Observed statuses reset on restart** and only include codes `>= 400`.
