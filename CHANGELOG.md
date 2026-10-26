# Changelog

All notable changes to Bosun are documented here. Each release's section is
published as the top of its GitHub release by `.github/workflows/release.yml`.

## v0.8.0

v0.8.0 is a hardening release. It closes 51 issues, most found in a full audit of
v0.7.0: security holes, panics, lost or duplicated messages, and specs and
clients that didn't match the server. It also adds what typed handlers were
missing (status codes, headers, cookies, body limits), testing helpers,
retries for events, new middleware, and a much richer OpenAPI spec.

**Many defaults changed to fail safe.** Read the upgrade guide before you
deploy.

### Upgrade guide

These changes can break a v0.7.0 app or change what it does at runtime. Each
item says how to keep the old behaviour where that's possible.

**Requests and responses**
- **Typed routes cap request bodies at 1 MiB** and answer `413` above it
  (#23). Raise the limit with `bosun.WithMaxBody(n)` for the app or
  `bosun.MaxBody(n)` for one route; `n <= 0` means no limit. Raw handlers and
  `struct{}` routes are never limited.
- **The server sets `ReadHeaderTimeout` to 10s** (#23). Override it with
  `bosun.WithHTTPServer`.
- **A body with an unsupported `Content-Type` gets `415`** instead of being
  ignored (#54). JSON (`application/json` and any `*+json`), urlencoded and
  multipart forms are accepted, and a missing Content-Type is still read as
  JSON. Browser `fetch` with a string body and no header sends `text/plain`;
  set `Content-Type: application/json`.
- **Some binding mistakes now stop `app.Start`** instead of failing each
  request (#54, #25):
  - `Req[*T]` In types
  - a `query:`/`path:`/`header:`/`form:` tag on a type the binder can't fill
  - a binding tag on an unexported field
- **Out-of-range numbers are a `400`** with rule `"range"` (`?n=300` into an
  `int8` used to bind 44) (#54).
- **`repomod.ErrNotFound` and `storemod.ErrNotFound` answer `404`** instead
  of `500` (#29).
- **A plain error from `Validate()` answers `400 {"error":"invalid request"}`**
  rather than echoing the error text (#29). Return a `*bosun.Error` or
  `*bosun.ValidationError` to control the message.
- **An `Out` that can't be encoded answers `500`** and is recorded as an
  error (#55).

**Security defaults**
- **Webhooks fail closed** (#38). A provider with no verifier gets `401`. Use
  `webhookmod.Unverified()` for local development only. Stripe events older
  than 5 minutes are rejected (`StripeVerifier{Tolerance: ...}`). Bodies over
  `Options.MaxBody` (default 1 MiB) get `413`.
- **The manifest endpoint is opt-in** (#41). Importing `manifestmod` no longer
  serves `/.bosun/manifest`. Set `manifestmod.Options{Expose: true}` and keep
  the endpoint off the public internet. `bosun manifest` and `bosun gen client`
  need a service with `Expose` set.
- **Health probes hide error details** (#41). Failing probes report
  `"failed"`; set `healthmod.DetailedErrors = true` to get the error text.
- **CORS rejects `"*"` with `AllowCredentials`** with a panic at startup
  (#37). List the trusted origins instead.
- **`config.SecretBox` binds each value to its config key** (#51):
  - `Seal(key, plaintext)` and `Open(key, sealed)` now take the key, so
    existing call sites fail to compile.
  - `KVSource{Decrypt: box.Open}` still compiles.
  - Values sealed by v0.7.0 are rejected by default. To migrate, set
    `box.AllowLegacy = true`, run `box.Reseal(key, value)` once over each
    stored value, then turn `AllowLegacy` off again.

**Configuration**
- **Unknown config fields are an error** (#51): fatal at startup; at runtime
  the previous value is kept and the error logged. The error names the key,
  field and source.
- **A `Validate() error` method on a bound type is called** on every load
  (#51), with the same failure rules.
- **Sources deep-merge per field** (#49). An env var now overrides only the
  field it names, not the whole key. A key removed from every source goes
  back to its default.
- **Env values are decoded against the bound field type** (#50):
  - strings stay strings, so `PASSWORD=123456` works
  - `time.Duration` needs a unit (`5s`; a bare `5` is an error)
  - `__` reaches nested fields (`DB__POOL__MAX=10`)
  - an invalid value names the variable in the error

  `docs/config.md` used to describe a `BOSUN_CONFIG_<KEY>` convention that was
  never implemented; it now documents the real rules.

**Events**
- **Typed handlers retry with backoff and give up** (#43). The default is 10
  attempts with exponential backoff from 100ms, capped at 5s. After the last
  attempt the message goes to `WithDeadLetter(subject)` if set; otherwise it
  is acked and an error is logged. `WithMaxAttempts(0)` plus
  `WithBackoff(nil)` restores the old retry-forever behaviour.
- **Nack with requeue redelivers only to the subscriber or group that
  failed**, not to every subscriber (#42). The original is acked only after a
  successful requeue. `Ack` and `Nack` now return driver errors.
- **AMQP group queues are renamed from `<group>` to `<group>.<subject>`**
  (#45). The old durable queues stay on the broker unused: **drain or delete
  them**. AMQP `Publish` now waits for a publisher confirm (`ErrNacked`), so
  it is slower, and `Nack` without requeue rejects the message so a
  dead-letter exchange applies.
- **Message IDs:** NATS and AMQP messages always carry an ID (32 hex
  characters), stable across redelivery (#46).
- **Core NATS stays best-effort:** a failed handler is retried, but a crashed
  process loses its message.

**Data and storage**
- **The outbox table gains five columns** (`next_attempt_at`, `claim_token`,
  `attempts`, `last_error`, `dead_at`); `AutoMigrate` adds them (#47).
  - Failing rows back off and no longer block newer ones, so strict ID order
    no longer holds after a failure.
  - After `MaxAttempts` (default 20) a row is marked dead.
  - Safe claiming across replicas needs the GORM repo from `outboxmod.For()`.
    Run a single relay with `Register()`.
- **`tenantmod` `Update` checks the row belongs to the caller's tenant**
  (#35). Another tenant's row, or a missing row with a non-zero key, returns
  `ErrNotFound` instead of being overwritten or inserted.
- **WebDAV store** (#32):
  - `List` returns objects at any depth, as FS and S3 already did.
  - Requests through the default client time out after 5 minutes
    (`storewebdavmod.DefaultTimeout`); pass `Options.HTTP` for slower
    transfers.
  - `Put` no longer closes the reader you pass in.

**OpenAPI**
- **`GET /docs` serves Swagger UI** whenever `openapi` is imported (#63). An
  app with its own `/docs` route now fails `Start` with a duplicate-route
  error: move it, or set `DocsOptions{Path: ""}` to turn the page off.
- **Schema names include the package** (`users.Out` becomes `UsersOut`;
  package `main` keeps bare names), and **operationIds are
  `<Controller><Method>`** such as `NotesGet` (#28, #60). Regenerate any
  clients built from the spec.

**Middleware and lifecycle**
- **`Recover` sends its 500 with only CORS, `Vary` and the correlation
  header**, dropping everything the handler had set (#58).
- **Inbound correlation IDs** longer than 128 characters or outside
  `[A-Za-z0-9._-]` are replaced, and `traceparent` must be valid lowercase hex
  (#58).
- **`Logging`** uses `InfoContext` and adds `correlation_id` (#59).
- **`RateLimit`** sends `Retry-After` on `429`, and buckets by client IP when
  `mw.RealIP` runs (#59).
- **After `Shutdown`, resolving a constructed service returns an error**
  instead of rebuilding it (#56). Instances stay resolvable and `Shutdown`
  runs once.
- **`app.Start` runs once**, so `Start()` followed by `Run()` is supported
  (#53).
- **A duplicate route makes `Start` return an error** naming both controllers
  (#30).

**CLI**
- **`bosun new` runs `go mod tidy`**, so it needs network access or a warm
  module cache. It refuses a non-empty directory without `--force`, and
  inside a workspace it creates services under `services/` (#66, #67).
- **Requirements before using `bosun new` and the doc-comment table:**
  scaffolded services and generated registries need **bosun v0.8.0 or later**.
  - The doc-comment table is written only when the module requires v0.8.0+.
  - Use `--no-docs` to leave it out.

### Security

- `gormrepomod` passed caller IDs to GORM as raw SQL conditions: `Get` and
  `Delete` could run injected SQL or delete a whole table (#34).
- `tenantmod` `Update` could overwrite another tenant's row (#35).
- Audit `Redact` leaked `json:",omitempty"` and `json:"-"` fields and could
  crash on cyclic values (#36).
- CORS turned `"*"` plus credentials into "any origin, with credentials"
  (#37).
- `webhookmod` accepted unverified providers by default, had no Stripe replay
  window and kept only the last `v1` signature (#38).
- `storewebdavmod` didn't escape keys, so `?`, `#` and encoded `..` could
  overwrite other objects or escape `BaseURL` (#39).
- Manifest and health endpoints exposed internals by default (#41).
- `SecretBox` ciphertexts weren't bound to their key (#51).
- Dependencies bumped for reachable vulnerabilities (#69):
  - `rabbitmq/amqp091-go` 1.10.0 → 1.14.0 (eventamqpmod)
  - `redis/go-redis` 9.7.0 → 9.7.3 (eventredismod)
  - `yuin/goldmark` 1.7.8 → 1.7.17 (cmd/bosun)

### Added

**Typed handlers**
- `bosun.SetStatus(ctx, code)`, `bosun.ResponseHeader(ctx)`,
  `bosun.SetCookie(ctx, c)` and the `bosun.Status(code)` route option; `204`,
  `205` and `304` send no body (#57).
- `bosun.WithMaxBody(n)`, `bosun.MaxBody(n)`, `bosun.DefaultMaxBody`,
  `bosun.DefaultReadHeaderTimeout` (#23).
- Repeated query, header and form values bind to `[]scalar` fields;
  `*scalar` fields stay nil when absent (#25).
- Multipart `form:` fields bind (#31).
- Named `string`/`[]byte` In and Out types work, and `json.RawMessage` is
  sent as JSON (#52).

**Errors**
- `bosun.MapError(target, status, msg)` maps a sentinel error to a status,
  per app (#29).
- An error with `HTTPStatus() int` (and optionally `PublicMessage() string`)
  carries its own status (#29).
- Joined errors (`errors.Join`, multiple `%w`) keep their `*bosun.Error`
  status (#55).

**Route metadata**
- `bosun.Meta(key, val)`, `MWRef.WithMeta` and `bosun.MetaPermissions` (#27).
- `RouteInfo.Meta`, `.Middleware`, `.Raw` and `.Status`.
- `app.Routes()`, which includes raw routes (#27).

**OpenAPI**
- `openapi.Options`: title, version, description, servers and security
  schemes (#62).
  - A route's permissions become its security requirement, with OAuth scopes,
    and an `x-permissions` extension.
  - Pointer fields are `nullable`.
  - The `example` struct tag adds examples.
- `openapi.DocsOptions` for the Swagger UI page: `Path`, `Title`, and
  `AssetsURL` for self-hosted assets (#63).
- `bosun.Doc(summary, description)`; handler, type and field doc comments
  reach the spec through `bosun gen registry` (#71).
- `requestBody` for DELETE routes with body fields (#28).
- The spec is built once and cached (#28).

**Testing**
- New `bosuntest` package (#26):
  - `Build[T]` builds one service with fakes, without starting the app.
  - `App` builds, starts and cleans up an app.
  - Request helpers `Get`/`Post`/`Put`/`Patch`/`Delete`/`Do`, with options
    `JSON`, `Body`, `Header`, `Query` and `Session(id)`, and `Decode[Out]`.
  - Session transport is configurable with `SessionHeader` and
    `SessionCookie`.
- `registry.Replace[T]` overrides a registered service before it's built
  (#26).
- `App.ApplyDefaults()` (#26).

**Events**
- `eventmod.WithMaxAttempts`, `WithBackoff` (`ExponentialBackoff`,
  `ConstantBackoff`), `WithDeadLetter`, and the optional `DelayedNacker`
  interface (#43).
- `eventredismod.Options.ClaimIdle` (default 30s) and `MaxLen` (#44).
- `eventamqpmod.Options.Mandatory`, `ErrNacked`, `ErrUnroutable` (#45).

**Middleware**
- `mw.RealIP` and `mw.ClientIP` (#59).
- `mw.Compress`: gzip and deflate from the standard library (#59).
- `mw.ETag` with conditional GET (#59).
- `mw.Timeout` (#59).
- `mw.SecureHeaders` (#59).

**Configuration**
- `config.Validator`, `SecretBox.Reseal` and `SecretBox.AllowLegacy` (#51).

**Audit**
- An Auditor may implement `bosun.AuditFilter` (`ShouldAudit`) to skip
  events. Redaction then never runs for them (#22).

**CLI**
- `bosun new mono <name>` (alias `monolith`): a single-module app organised by
  feature (#86).
- `--bare` on `bosun new mono|service|project`: wiring only, no demo code
  (#86).
- `bosun add controller|service|middleware <name>` adds a component to an
  existing project and regenerates the registry (#86). Flags: `--dir`,
  `--pkg`, `--in`, `--force`, and `--prefix` for controllers.
- `bosun new` flags `--bosun-dir` and `--force` (#66, #67).
- `bosun gen registry --tags` and `--no-docs` (#68, #71).

### Fixed

**Core**
- Calling `app.Start()` then `Run()` panicked (#53).
- A duplicate route panicked inside `Start` (#30).
- Typed handlers panicked for named `string`/`[]byte` types (#52).
- An invalid `*bosun.Error` status panicked in `WriteHeader` (#55).
- `Shutdown` broke `RegisterInstance` nodes, a failed `Start` leaked services
  that were already built, and request contexts weren't cancelled when
  shutdown began (#56).

**Middleware**
- `Recover` kept the handler's headers, such as `Content-Encoding` and
  `Set-Cookie`, on its 500 (#58).
- `Tracing` broke streaming and leaked spans on panic (#58).
- `Logging` broke streaming (#33).
- The `RateLimit` map never shrank (#33).

**Events**
- Nack-requeue fanned out to every subscriber and could lose messages (#42).
- Redis replicas shared group names, pending entries were never reclaimed and
  streams grew forever (#44).
- AMQP groups shared one queue across subjects, and RPC responders were lost
  on reconnect (#45).
- NATS `Close` cut off in-flight handlers (#46).
- The in-memory bus spawned unbounded goroutines under a poison message (#43).

**Data and storage**
- The outbox blocked behind failing rows, replicas published duplicates and
  `Close` could hang (#47).
- `gormrepomod` transactions on the context leaked across databases (#48).
- An interrupted FS `Put` corrupted the previous object (#40).
- FS `List` walked the whole store (#32).
- WebDAV `List` buffered whole replies and only went one level deep (#32).
- WebDAV reported size -1 from servers that gzip responses (#69).

**Configuration**
- One bad config key blocked every other key, and the watcher goroutine
  leaked (#49).
- Number-looking env values broke string fields (#50).

**OpenAPI**
- Same-named types from different packages collided (#28).
- `json` tags were misread (#60).
- Go 1.22 path patterns produced invalid parameters (#60).
- operationIds were duplicated (#60).
- Error scanning matched handlers by substring, nondeterministically (#61).
- Nullable `$ref` fields had no `type` (#69).

**CLI**
- `bosun gen client` produced code that didn't compile, and put query, path
  and header fields in the JSON body (#64, #65).
- `bosun new` produced services that didn't build (#66).
- Project contracts weren't a module, and a new service overwrote files and
  didn't join the workspace (#67).
- The CLI reported version `dev`, `gen registry` ignored build tags, and
  docgen left stale pages (#68).

### Performance

| Benchmark | v0.7.0 | v0.8.0 |
|---|---|---|
| 500-row list with an auditor that drops the event (#22) | 312 KB, 4,518 allocs | 84 KB, 14 allocs |
| Parallel typed requests, 8 CPUs (#24) | ~395 ns/op | ~183 ns/op |
| Binding 2 path + 4 query + 1 header fields (#25) | 34 allocs, ~5.1 µs | 16 allocs, ~2.3 µs |

- The process-wide lock on the request path is gone (#24).
- Binding uses a plan compiled once per type and parses the query string once
  per request (#25).

### Tooling and docs

- **CI** (#69):
  - govulncheck and golangci-lint on every module
  - a Go 1.22 matrix for modules that declare it
  - a real WebDAV server (rclone) in the integration job
  - Redocly validation of the generated spec
  - `bosun new` build and Docker checks for every event backend and for
    `new mono`
  - a tag-triggered release workflow
- **Postgres:** a two-relay outbox test runs against Postgres in CI, from the
  unreleased nested module `modules/outboxmod/pgtest` (#47).
- **Docs** (#70):
  - The README and getting-started guide now match the repo: the dependency
    claims, Go versions per module, the full CLI table, and a working
    getting-started.
  - The private-module and `GOPRIVATE` instructions are gone; Bosun is
    public.
  - `go.work.sum` is no longer tracked.
- **Docs site:** new favicon and header icon.

### Module Go versions

| Module | Minimum Go |
|---|---|
| root (`github.com/bluebeard63/bosun`), `cmd/bosun`, `eventamqpmod`, `eventredismod` | 1.22 |
| `eventnatsmod`, `stores3mod`, `traceotelmod` | 1.25 |

`eventnatsmod` and `stores3mod` need 1.25 only because of a
`golang.org/x/sys` requirement that is planned for removal in v1.0.0 (#102).
