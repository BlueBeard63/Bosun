# OpenAPI reference

This page describes exactly what `github.com/bluebeard63/bosun/openapi` generates. For tasks, see the [OpenAPI how-to guides](./openapi.md). For a walkthrough, see the [OpenAPI tutorial](./openapi-tutorial.md).

## Package API

| Symbol | Description |
|---|---|
| `SpecController` | Controller that serves the spec and the docs page. Registered with prefix `""`, so it serves `GET /openapi.json` and `GET /docs`. Remount it with `bosun.OverridePrefix[openapi.SpecController](prefix)`. |
| `ScanOptions{SourceDir string}` | Where the source scanner looks for files on disk. Default: `&ScanOptions{SourceDir: "."}`. Override it with `registry.RegisterInstance[*openapi.ScanOptions]`. An empty `SourceDir` turns off on-disk scanning. |
| `Sources(fsys fs.FS) struct{}` | Registers an embedded source tree for scanning. Use it as `var _ = openapi.Sources(src)`. The registration applies to every app in the process. |
| `Options` | The document's `info`, `servers` and `securitySchemes`, and which schemes routes with permissions require. Default: `&Options{}`. Override it with `registry.RegisterInstance[*openapi.Options]`. See [Options](#options). |
| `Server{URL, Description string}` | One entry of `Options.Servers`. |
| `SecurityScheme` | One entry of `Options.SecuritySchemes`: an OpenAPI 3.0 security scheme. See [Security](#security). |
| `bosun.Doc(summary, description string)` | Route option that sets the operation's `summary` and `description`. It stores a `bosun.RouteDoc` under `bosun.MetaDoc` in the route's `Meta`. |
| `bosun.RegisterDocComments(map[string]string)` | Records doc comments for the spec. Called by the file `bosun gen registry` writes. See [Descriptions](#descriptions). |
| `DocsOptions{Path, Title, AssetsURL string}` | The interactive docs page. Default: `&DocsOptions{Path: "/docs"}`. Override it with `registry.RegisterInstance[*openapi.DocsOptions]`. See [Docs page](#docs-page). |
| `DefaultAssetsURL` | `https://cdn.jsdelivr.net/npm/swagger-ui-dist@5.17.14`, the Swagger UI build the docs page loads by default. |
| `OAuthFlows`, `OAuthFlow` | The flows of an `oauth2` `SecurityScheme`: `Implicit`, `Password`, `ClientCredentials` and `AuthorizationCode`, each with `AuthorizationURL`, `TokenURL`, `RefreshURL` and `Scopes map[string]string`. |

Importing the package registers the controller, the scanner service and the `ScanOptions`, `Options` and `DocsOptions` defaults. `bosun.Disable("github.com/bluebeard63/bosun/openapi")` removes them all from an app.

## Options

| Field | Spec field | Default |
|---|---|---|
| `Title string` | `info.title` | `bosun API` |
| `Version string` | `info.version` | `1.0.0` |
| `Description string` | `info.description` (CommonMark) | left out |
| `Servers []Server` | `servers`, in order. `Server.Description` is left out when empty. | left out |
| `SecuritySchemes map[string]SecurityScheme` | `components.securitySchemes`, keyed by name | left out |
| `PermissionSchemes []string` | The schemes a route with permissions requires (see [Security](#security)). Names not in `SecuritySchemes` are ignored. | every scheme in `SecuritySchemes` |

## Security

`SecurityScheme` fields map to the OpenAPI 3.0 security scheme object; empty fields are left out:

| Field | Spec field | Used with `Type` |
|---|---|---|
| `Type` | `type` | all: `http`, `apiKey`, `oauth2`, `openIdConnect` |
| `Description` | `description` | all |
| `Scheme`, `BearerFormat` | `scheme`, `bearerFormat` | `http` (`bearer`, `basic`, ...) |
| `In`, `Name` | `in`, `name` | `apiKey` (`header`, `query`, `cookie`) |
| `Flows` | `flows` | `oauth2` |
| `OpenIDConnectURL` | `openIdConnectUrl` | `openIdConnect` |

Per-route security comes from the route's permissions, the `[]string` stored under `bosun.MetaPermissions` (see [route metadata](./routing-internals.md#inspecting-registered-routes)), merged from the controller, its groups and the route, with duplicates removed:

| Route | `security` | `x-permissions` |
|---|---|---|
| No permissions | left out | left out |
| Permissions, no schemes configured | left out | the permissions |
| Permissions, schemes configured | one requirement per scheme in `PermissionSchemes`, as alternatives | the permissions |

In a requirement for an `oauth2` or `openIdConnect` scheme the scopes are the route's permissions. OpenAPI 3.0 allows no scopes for `http` and `apiKey` schemes, so their requirement is an empty list. For example, a route with permission `notes:write` and `PermissionSchemes: []string{"bearer", "oauth"}`:

```json
"security": [ { "bearer": [] }, { "oauth": ["notes:write"] } ],
"x-permissions": ["notes:write"]
```

Every permission used by a route is also added to the `scopes` of each `oauth2` flow, with an empty description unless the flow's `Scopes` already describes it. Operations without permissions have no `security`; there is no document-wide `security`.

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

`info` comes from [Options](#options). `servers` and `components.securitySchemes` are added when `Options` configures them.

The document is built from the serving app's `app.TypedRoutes()` on the first request, which comes after `Start` when the route list is complete, and the encoded bytes are served from then on. It is rebuilt only when the observed layer (see [Error layers](#error-layers)) has recorded a new status since. The source scan runs once per app, on the first request.

## Docs page

When `DocsOptions.Path` is not empty, `SpecController` mounts two raw routes under its prefix. Raw routes are listed by `app.Routes()` with `Raw` set and are never in the spec.

| Route | Response |
|---|---|
| `GET <Path>` | `200`, `text/html; charset=utf-8`: a page that loads `<AssetsURL>/swagger-ui.css`, `<AssetsURL>/swagger-ui-bundle.js` and the init script, with no inline script. |
| `GET <Path without trailing slash>/init.js` | `200`, `text/javascript`: starts Swagger UI with the spec URL. |

| Field | Meaning | Default |
|---|---|---|
| `Path` | The page's path. A missing leading `/` is added. A trailing `/` is kept and matched exactly (`/docs/` serves only `/docs/`). Empty turns the page off; `/openapi.json` is still served. | `/docs` |
| `Title` | The page's `<title>`, HTML-escaped. | `Options.Title`, else `bosun API` |
| `AssetsURL` | Base URL of a [swagger-ui-dist](https://www.npmjs.com/package/swagger-ui-dist) build. A trailing `/` is removed. | `DefaultAssetsURL` |

The page refers to the spec and the init script by URLs relative to itself (`./openapi.json` from `/docs`, `../openapi.json` from `/docs/`), so the links hold under any `OverridePrefix` and behind a proxy that removes a path prefix before forwarding.

## Operations

Each typed route (`bosun.Get`, `Post`, `Put`, `Delete`, `Patch`) becomes one operation:

| Spec field | Source |
|---|---|
| Path key | `RouteInfo.Path`: the controller prefix, group prefixes and the route path, with `:name` written as `{name}`. A route at `"/"` under `/notes` is `/notes`. A `{name...}` wildcard is written as `{name}`, and a `{$}` end anchor is dropped, so `/files/{path...}` becomes `/files/{path}` and `/notes/{$}` becomes `/notes/`. |
| Method key | The HTTP method, in lower case. |
| `summary`, `description` | From `bosun.Doc` and the handler's doc comment. Left out when empty. See [Descriptions](#descriptions). |
| `operationId` | `<Controller><Method>`: the receiver type name without a `Controller` suffix, then the method name, e.g. `NotesGet` for `(*NotesController).Get`. Handlers that aren't methods use the lower-case HTTP method and the path's words, e.g. `get_notes_id`. Operations are numbered in path and method order, and an ID that is already taken gets the first free numeric suffix (`NotesGet2`), so every ID is unique. |
| `parameters`, path | One per `{name}` or `{name...}` segment: `in: path`, `required: true`. The schema comes from the `In` field tagged `path:"name"` (type plus [validation](./validation.md) constraints), or `{"type":"string"}` when there's no such field. |
| `parameters`, query | One per `In` field tagged `query:"name"`: `in: query`. `required` is `true` when the field has the `required` rule. The schema comes from the field type plus its constraints. |
| `parameters`, header | One per `In` field tagged `header:"Name"`, in the same way as query parameters. |
| `requestBody` | Only for `POST`, `PUT`, `PATCH` and `DELETE`, when `In` has at least one body property (an exported field that is not tagged `path`, `query`, `header` or `json:"-"`). It is `required: true`, with content type `application/json` and the schema of `In`. |
| `security`, `x-permissions` | From the route's `bosun.MetaPermissions`. See [Security](#security). |
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
| `int`, `int8` … `int64`, `uint`, `uint8` … `uint64` | `{"type":"integer"}` |
| `float32`, `float64` | `{"type":"number"}` |
| `time.Time` | `{"type":"string","format":"date-time"}` |
| `[]byte` | `{"type":"string","format":"byte"}` (base64, as `encoding/json` sends it) |
| `[]T`, `[N]T` | `{"type":"array","items":<T>}` |
| `map[K]V` | `{"type":"object","additionalProperties":<V>}` |
| `*T` | Same as `T`. As a struct property it also gets `"nullable": true` (a `$ref` is wrapped as `{"allOf":[{"$ref":...}],"nullable":true}`). |
| Named struct | `{"$ref":"#/components/schemas/<Name>"}`, with the definition stored once (see [Schema names](#schema-names)). Recursive types are supported. |
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
- An `example:"..."` tag becomes the property's (or parameter's) `example`. For a `string` schema the tag text is used as is; otherwise it is parsed as JSON, so `example:"42"` is a number, `example:"true"` a boolean and `example:"[\"a\"]"` an array. Text that isn't valid JSON is used as a string. On a `$ref` property the example is set next to an `allOf` wrapper.

### Schema names

A named struct's `components/schemas` key is its package's last import path element followed by its type name, with each word capitalised and other characters removed: `users.Out` in `example.com/app/users` is `UsersOut`. Types in package `main` keep their bare name, so `NoteOut` stays `NoteOut`. A generic type's arguments are added the same way: `api.Page[users.Out]` is `ApiPageUsersOut`.

When two types would get the same key, both are qualified with more import path elements until they differ: `example.com/app/v1/users.Out` and `example.com/app/v2/users.Out` become `V1UsersOut` and `V2UsersOut`. A key that is still shared, or that equals `ErrorResponse` or `ValidationError`, gets a numeric suffix (`2`, `3`, ...) in import path order. Keys depend only on the set of types the app's routes use, not on route order.

### Validation constraints

`validate` tags add JSON Schema keywords to inline property and parameter schemas:

| Rule | string | number | slice / array | map |
|---|---|---|---|---|
| `min=N` | `minLength` | `minimum` | `minItems` | `minProperties` |
| `max=N` | `maxLength` | `maximum` | `maxItems` | `maxProperties` |
| `oneof=a b` | `enum` of strings | `enum` of numbers | n/a | n/a |

Constraints aren't added to properties whose schema is a `$ref` (a named struct), because OpenAPI 3.0 ignores keywords next to `$ref`. Custom `Validate()` methods can't be inspected and don't appear in the spec.

## Descriptions

Doc comments reach the spec through `bosun.RegisterDocComments`, which the file written by `bosun gen registry` calls. Its keys are the package import path (`main` for package `main`), a `.`, and:

| Key | Collected from | Becomes |
|---|---|---|
| `(*T).Method`, `T.Method`, `Func` | Typed handlers: functions and methods whose second parameter is `*bosun.Req[...]` | The operation's `summary` and `description` |
| `T` | Struct types (not aliases) | The schema's `description` |
| `T.Field` | Exported struct fields: the comment above the field, else the one at the end of its line | The property's `description`, or the parameter's for `path`, `query` and `header` fields |

Comments are collected from every package in the module except `_test.go` files, other binaries' `main` packages and the directories `bosun gen registry` skips. Directives such as `//go:generate` are dropped. The generator only writes the call when the module requires bosun v0.8.0 or later or replaces bosun, and never with `--no-docs`.

An operation's `summary` is the comment's first sentence, joined onto one line: it ends at the first `.` followed by a space or line break and not by a lower-case letter, or at the end of the first paragraph. The rest is the `description`. A non-empty `bosun.Doc` summary or description replaces the one from the comment. Empty values are left out of the spec.

A generic type is keyed without its type parameters, so `Page[T]`'s comment describes every instance's schema. A field's key uses the struct that declares it, so a promoted field of an embedded struct is described by that struct's comment for the field.

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

- **No tags or document-wide security.** Operations have no `tags`, and `security` is only set per operation, from permissions.
- **Success is always `200` with JSON.** A `string` or `[]byte` `Out` is still described as `application/json`, and an empty `struct{}` appears as an empty object. Bosun actually sends `text/plain`, `application/octet-stream` and no body respectively.
- **Form bodies are described as JSON.** Fields tagged `form` appear as JSON body properties, and form and multipart request bodies get an `application/json` content type.
- **Custom validation is invisible.** Checks made in `Validate()` methods aren't reflected in schemas.
- **Observed statuses reset on restart** and only include codes `>= 400`.
